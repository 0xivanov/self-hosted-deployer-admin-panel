//go:build linux

package nodebuild

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/npmfetch"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/projectarchive"
)

type GuestRequest struct {
	Plan             Plan
	ToolchainSHA256  string
	SourcePath       string
	DependenciesPath string
	Bundle           npmfetch.Bundle
	WorkDirectory    string
	NotAfter         int64
}

// RunGuest performs the offline build stage INSIDE an already isolated Linux VM.
// The trusted controller must provision the pinned toolchain, private input and
// output volumes, an exclusive unprivileged user, resource limits and a separate
// network namespace with no external interfaces. This function is not a VM
// isolation boundary. Return values and logs are untrusted guest output. Before
// exporting the build tree, the controller must stop the entire VM and inspect
// its output volume independently; successful npm exit is not retirement proof.
func RunGuest(ctx context.Context, r GuestRequest, log io.Writer) (Source, error) {
	if os.Geteuid() == 0 || r.Plan.Architecture != runtime.GOARCH || log == nil || !filepath.IsAbs(r.WorkDirectory) || !filepath.IsAbs(r.DependenciesPath) || !filepath.IsAbs(r.SourcePath) {
		return Source{}, ErrPlan
	}
	// Unprivileged services cannot inspect PID 1's namespace on hardened guests.
	// Verify the visible interfaces here; the controller owns namespace creation.
	interfaces, err := net.Interfaces()
	if err != nil || len(interfaces) == 0 {
		return Source{}, errors.New("isolated guest network namespace required")
	}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagLoopback == 0 {
			return Source{}, errors.New("external guest interface present")
		}
	}
	pin, err := hex.DecodeString(r.ToolchainSHA256)
	if err != nil || len(pin) != 32 || hex.EncodeToString(pin) != r.ToolchainSHA256 {
		return Source{}, ErrPlan
	}
	remaining := time.Until(time.Unix(r.NotAfter, 0))
	if remaining <= 0 || remaining > time.Minute {
		return Source{}, errors.New("build deadline expired or invalid")
	}
	ctx, cancel := context.WithDeadline(ctx, time.Unix(r.NotAfter, 0))
	defer cancel()
	f, err := os.Open(r.SourcePath)
	if err != nil {
		return Source{}, err
	}
	archive, err := io.ReadAll(io.LimitReader(f, projectarchive.MaxCompressed+1))
	f.Close()
	if err != nil {
		return Source{}, err
	}
	// Recompute allowed commands from source rather than executing request commands.
	plan, err := Prepare(ctx, archive, r.Plan.SourceSHA256, Settings{Architecture: runtime.GOARCH})
	if err != nil {
		return Source{}, err
	}
	if !reflect.DeepEqual(plan, r.Plan) {
		plan, err = Prepare(ctx, archive, r.Plan.SourceSHA256, Settings{Architecture: runtime.GOARCH, SkipBuild: true})
		if err != nil || !reflect.DeepEqual(plan, r.Plan) {
			return Source{}, ErrPlan
		}
	}
	dependencies, err := os.OpenRoot(r.DependenciesPath)
	if err != nil {
		return Source{}, err
	}
	defer dependencies.Close()
	manifest, err := npmfetch.VerifyBundle(ctx, dependencies, r.Bundle, plan.SourceSHA256)
	if err != nil {
		return Source{}, err
	}
	locked, err := npmfetch.FromSource(ctx, archive, plan.SourceSHA256)
	if err != nil {
		return Source{}, err
	}
	if len(locked.Tarballs) != len(manifest.Tarballs) {
		return Source{}, npmfetch.ErrBundle
	}
	expected := map[string]string{}
	for _, item := range locked.Tarballs {
		expected[item.URL] = item.Integrity
	}
	for _, item := range manifest.Tarballs {
		if expected[item.URL] != item.Integrity {
			return Source{}, npmfetch.ErrBundle
		}
	}
	work, err := os.OpenRoot(r.WorkDirectory)
	if err != nil {
		return Source{}, err
	}
	defer work.Close()
	source, err := ExtractSource(ctx, archive, plan.SourceSHA256, work)
	if err != nil {
		return Source{}, err
	}
	// Fresh per-run state avoids inherited npm configuration or cache contents.
	home := "home-" + strings.TrimPrefix(source.Directory, "source-")
	if err = work.Mkdir(home, 0700); err != nil {
		return source, err
	}
	for _, name := range []string{"user.npmrc", "global.npmrc"} {
		f, err := work.OpenFile(home+"/"+name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return source, err
		}
		if err = f.Close(); err != nil {
			return source, err
		}
	}
	bin := "/opt/deployer-node/toolchains/" + r.ToolchainSHA256 + "/bin"
	app := filepath.Join(r.WorkDirectory, source.Directory)
	homePath := filepath.Join(r.WorkDirectory, home)
	env := []string{"PATH=" + bin + ":/usr/bin:/bin", "HOME=" + homePath, "CI=true", "npm_config_offline=true", "npm_config_cache=" + homePath + "/cache", "npm_config_userconfig=" + homePath + "/user.npmrc", "npm_config_globalconfig=" + homePath + "/global.npmrc"}
	output := &guestLog{destination: log, remaining: 1 << 20}
	run := func(args ...string) error {
		cmd := exec.CommandContext(ctx, bin+"/npm", args...)
		cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = app, env, output, output
		cmd.WaitDelay = time.Second
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("offline npm step failed: %w", err)
		}
		return nil
	}
	seen := map[string]bool{}
	for _, item := range manifest.Tarballs {
		if seen[item.File] {
			continue
		}
		seen[item.File] = true
		if err = run("cache", "add", filepath.Join(r.DependenciesPath, r.Bundle.Directory, item.File), "--offline", "--ignore-scripts", "--no-audit", "--no-fund"); err != nil {
			return source, err
		}
	}
	for _, step := range plan.Steps {
		if err = run(step.Args...); err != nil {
			return source, err
		}
	}
	return source, ctx.Err()
}

type guestLog struct {
	destination io.Writer
	remaining   int
}

func (w *guestLog) Write(p []byte) (int, error) {
	n := len(p)
	if w.remaining > 0 {
		keep := min(len(p), w.remaining)
		written, err := w.destination.Write(p[:keep])
		w.remaining -= written
		if err != nil {
			return written, err
		}
		if written != keep {
			return written, io.ErrShortWrite
		}
	}
	return n, nil
}
