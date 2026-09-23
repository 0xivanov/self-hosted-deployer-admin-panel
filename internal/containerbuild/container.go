//go:build linux

// Package containerbuild is the Linux controller for one Docker build per
// execution. Docker is a shared-kernel boundary, not a VM isolation claim.
package containerbuild

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/buildlog"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodeartifact"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodebuild"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/npmfetch"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/portal"
)

type Config struct{ Project, ToolchainSHA256, Architecture, Listen, Host, TokenFile, TLSCert, TLSKey, StateDirectory, DependenciesDirectory, Image string }
type Executor struct {
	cfg      Config
	mu       sync.Mutex
	draining bool
	lock     *os.File
	cancel   context.CancelFunc
	done     chan struct{}
}
type record struct {
	FailureLog string                      `json:"failure_log,omitempty"`
	Request    portal.NodeExecutionRequest `json:"request"`
	State      string                      `json:"state"`
	Outcome    string                      `json:"outcome,omitempty"`
	Cleaned    bool                        `json:"cleaned,omitempty"`
	Artifact   string                      `json:"artifact,omitempty"`
	Container  string                      `json:"container"`
	Output     string                      `json:"output"`
}

var ErrExecutor = errors.New("container build executor unavailable")

func New(cfg Config) (*Executor, error) {
	if !validID(cfg.Project) || !validID(cfg.ToolchainSHA256) || (cfg.Architecture != "amd64" && cfg.Architecture != "arm64") || cfg.Image == "" || !filepath.IsAbs(cfg.StateDirectory) || !filepath.IsAbs(cfg.DependenciesDirectory) {
		return nil, ErrExecutor
	}
	if err := os.MkdirAll(cfg.StateDirectory, 0700); err != nil {
		return nil, ErrExecutor
	}
	lock, err := os.OpenFile(filepath.Join(cfg.StateDirectory, "controller.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, ErrExecutor
	}
	ctx, cancel := context.WithCancel(context.Background())
	e := &Executor{cfg: cfg, lock: lock, cancel: cancel, done: make(chan struct{})}
	go e.watch(ctx)
	return e, nil
}
func validID(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && hex.EncodeToString(b) == s
}
func (e *Executor) path(id string) string { return filepath.Join(e.cfg.StateDirectory, id+".json") }
func (e *Executor) load(id string) (record, error) {
	if !validID(id) {
		return record{}, ErrExecutor
	}
	b, err := os.ReadFile(e.path(id))
	if err != nil {
		return record{}, err
	}
	var r record
	err = json.Unmarshal(b, &r)
	return r, err
}
func (e *Executor) save(id string, r record) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	tmp := e.path(id) + ".pending"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	if ce := f.Close(); err == nil {
		err = ce
	}
	if err != nil {
		return err
	}
	if err = os.Rename(tmp, e.path(id)); err != nil {
		return err
	}
	d, err := os.Open(e.cfg.StateDirectory)
	if err != nil {
		return err
	}
	err = d.Sync()
	_ = d.Close()
	return err
}
func (e *Executor) SubmitNodeExecution(ctx context.Context, q portal.NodeExecutionRequest) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.draining {
		return ErrExecutor
	}
	if !validID(q.ExecutionID) || q.ProjectID != e.cfg.Project || q.ToolchainSHA256 != e.cfg.ToolchainSHA256 || q.Plan.Architecture != e.cfg.Architecture || len(q.Archive) == 0 {
		return ErrExecutor
	}
	if old, err := e.load(q.ExecutionID); err == nil {
		replay := q
		replay.Archive = nil
		persisted := old.Request
		persisted.Archive = nil
		if !reflect.DeepEqual(persisted, replay) {
			return ErrExecutor
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrExecutor
	}
	if q.NotAfter <= time.Now().Unix() || q.NotAfter > time.Now().Add(5*time.Minute).Unix() || !strings.HasPrefix(q.Bundle.Directory, "dependencies-") || !validID(strings.TrimPrefix(q.Bundle.Directory, "dependencies-")) || !validID(q.Bundle.ManifestSHA256) {
		return ErrExecutor
	}
	if sum := sha256.Sum256(q.Archive); hex.EncodeToString(sum[:]) != q.Plan.SourceSHA256 {
		return ErrExecutor
	}
	plan, err := nodebuild.Prepare(ctx, q.Archive, q.Plan.SourceSHA256, nodebuild.Settings{Architecture: e.cfg.Architecture})
	if err != nil || !reflect.DeepEqual(plan, q.Plan) {
		plan, err = nodebuild.Prepare(ctx, q.Archive, q.Plan.SourceSHA256, nodebuild.Settings{Architecture: e.cfg.Architecture, SkipBuild: true})
	}
	if err != nil || !reflect.DeepEqual(plan, q.Plan) {
		return ErrExecutor
	}
	deps, err := os.OpenRoot(e.cfg.DependenciesDirectory)
	if err != nil {
		return ErrExecutor
	}
	dl := npmfetch.NewClient()
	built, err := npmfetch.DownloadBundle(ctx, q.Archive, q.Plan.SourceSHA256, deps, dl)
	if err == nil && built.ManifestSHA256 != q.Bundle.ManifestSHA256 {
		err = ErrExecutor
	}
	if err == nil {
		err = deps.Rename(built.Directory, q.Bundle.Directory)
	}
	if err == nil {
		_, err = npmfetch.VerifyBundle(ctx, deps, q.Bundle, q.Plan.SourceSHA256)
	}
	_ = deps.Close()
	if err != nil {
		return ErrExecutor
	}
	if err = os.Chown(e.cfg.DependenciesDirectory, 60000, 60000); err != nil {
		return err
	}
	if err = filepath.WalkDir(filepath.Join(e.cfg.DependenciesDirectory, q.Bundle.Directory), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Lchown(p, 60000, 60000)
	}); err != nil {
		return err
	}
	r := record{Request: q, State: "submitted", Container: "deployer-node-build-" + q.ExecutionID, Output: filepath.Join(e.cfg.StateDirectory, "output-"+q.ExecutionID)}
	if err := os.WriteFile(filepath.Join(e.cfg.StateDirectory, q.ExecutionID+".zip"), q.Archive, 0600); err != nil {
		return ErrExecutor
	}
	g := nodebuild.GuestRequest{Plan: q.Plan, ToolchainSHA256: q.ToolchainSHA256, SourcePath: "/work/source.zip", DependenciesPath: "/work/dependencies", Bundle: q.Bundle, WorkDirectory: "/work/output", NotAfter: q.NotAfter}
	jb, _ := json.Marshal(g)
	if err := os.WriteFile(filepath.Join(e.cfg.StateDirectory, q.ExecutionID+".request.json"), jb, 0600); err != nil {
		return ErrExecutor
	}
	if err := exec.CommandContext(ctx, "chown", "60000:60000", filepath.Join(e.cfg.StateDirectory, q.ExecutionID+".zip"), filepath.Join(e.cfg.StateDirectory, q.ExecutionID+".request.json")).Run(); err != nil {
		return ErrExecutor
	}
	if err := os.Mkdir(r.Output, 0700); err != nil {
		return ErrExecutor
	}
	if err := e.save(q.ExecutionID, r); err != nil {
		return ErrExecutor
	}
	return e.start(ctx, q.ExecutionID, r)
}

// Shutdown prevents new submissions, asks every submitted container to stop,
// and waits for Docker to report an exited state.
func (e *Executor) watch(ctx context.Context) {
	defer close(e.done)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			entries, _ := os.ReadDir(e.cfg.StateDirectory)
			for _, entry := range entries {
				id := strings.TrimSuffix(entry.Name(), ".json")
				if !validID(id) {
					continue
				}
				probe, cancel := context.WithTimeout(ctx, 10*time.Second)
				_, _ = e.InspectNodeExecution(probe, id)
				cancel()
			}
		}
	}
}
func (e *Executor) Shutdown(ctx context.Context) error {
	e.mu.Lock()
	e.draining = true
	e.mu.Unlock()
	e.cancel()
	select {
	case <-e.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	entries, err := os.ReadDir(e.cfg.StateDirectory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		id := strings.TrimSuffix(entry.Name(), ".json")
		if !validID(id) {
			continue
		}
		r, err := e.load(id)
		if err != nil {
			return err
		}
		if r.State != "submitted" {
			continue
		}
		state, err := e.containerState(ctx, r.Container)
		if err != nil {
			return err
		}
		if state.Status != "exited" {
			if err = exec.CommandContext(ctx, "docker", "kill", r.Container).Run(); err != nil {
				return err
			}
			state, err = e.containerState(ctx, r.Container)
			if err != nil {
				return err
			}
		}
		if state.Status != "exited" {
			return ErrExecutor
		}
		r.State = "retired"
		r.Outcome = "failed"
		if state.ExitCode == 0 {
			r.Outcome = "succeeded"
		}
		if err = e.save(id, r); err != nil {
			return err
		}
	}
	return e.lock.Close()
}
func (e *Executor) start(ctx context.Context, id string, r record) error {
	src := filepath.Join(e.cfg.StateDirectory, id+".zip")
	out := r.Output
	request := filepath.Join(e.cfg.StateDirectory, id+".request.json")
	image := filepath.Join(e.cfg.StateDirectory, id+".output.ext4")
	if err := exec.CommandContext(ctx, "truncate", "-s", "256M", image).Run(); err != nil {
		return ErrExecutor
	}
	if err := exec.CommandContext(ctx, "mkfs.ext4", "-F", image).Run(); err != nil {
		return ErrExecutor
	}
	if err := exec.CommandContext(ctx, "mount", "-o", "loop", image, out).Run(); err != nil {
		return ErrExecutor
	}
	if err := exec.CommandContext(ctx, "chown", "60000:60000", out).Run(); err != nil {
		return ErrExecutor
	}
	if err := os.Chmod(out, 0700); err != nil {
		return err
	}
	args := []string{"run", "-d", "--name", r.Container, "--network", "none", "--user", "60000:60000", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--read-only", "--memory", "512m", "--pids-limit", "128", "--cpus", "1", "--restart", "no", "--log-opt", "max-size=1m", "--log-opt", "max-file=1", "--tmpfs", "/tmp:rw,nosuid,nodev,size=32m", "-v", src + ":/work/source.zip:ro", "-v", out + ":/work/output:rw", "-v", e.cfg.DependenciesDirectory + ":/work/dependencies:ro", "-v", request + ":/work/request.json:ro", e.cfg.Image, "/usr/local/bin/node-build-guest", "/work/request.json"}
	if err := exec.CommandContext(ctx, "docker", args...).Run(); err != nil {
		return ErrExecutor
	}
	return nil
}

type dockerState struct {
	Status   string
	ExitCode int
}

func (e *Executor) containerState(ctx context.Context, name string) (dockerState, error) {
	var s dockerState
	b, err := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{json .State}}", name).Output()
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(b, &s)
	return s, err
}
func (e *Executor) InspectNodeExecution(ctx context.Context, id string) (portal.NodeExecutionObservation, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, err := e.load(id)
	if err != nil || r.Request.ExecutionID != id {
		return portal.NodeExecutionObservation{}, ErrExecutor
	}
	if r.State == "submitted" {
		state, err := e.containerState(ctx, r.Container)
		if err != nil {
			return portal.NodeExecutionObservation{}, ErrExecutor
		}
		timedOut := false
		if state.Status != "exited" && time.Now().Unix() >= r.Request.NotAfter {
			if err = exec.CommandContext(ctx, "docker", "kill", r.Container).Run(); err != nil {
				return portal.NodeExecutionObservation{}, ErrExecutor
			}
			timedOut = true
			state, err = e.containerState(ctx, r.Container)
			if err != nil {
				return portal.NodeExecutionObservation{}, ErrExecutor
			}
		}
		if state.Status == "exited" {
			r.State = "retired"
			r.Outcome = "failed"
			if !timedOut && state.ExitCode == 0 {
				r.Outcome = "succeeded"
			}
			if r.Outcome == "failed" {
				logCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				var output buildlog.Tail
				cmd := exec.CommandContext(logCtx, "docker", "logs", "--tail", "200", r.Container)
				cmd.Stdout = &output
				cmd.Stderr = &output
				if cmd.Run() == nil {
					r.FailureLog = buildlog.Sanitize(string(output.Data))
				}
				cancel()
			}
			if err = e.save(id, r); err != nil {
				return portal.NodeExecutionObservation{}, err
			}
		}
	}
	if r.State == "retired" && r.Outcome == "failed" && !r.Cleaned {
		if e.cleanup(ctx, id, r) == nil {
			r.Cleaned = true
			if err = e.save(id, r); err != nil {
				return portal.NodeExecutionObservation{}, err
			}
		}
	}
	outcome := r.Outcome
	if outcome == "" {
		outcome = "running"
	}
	return portal.NodeExecutionObservation{ProjectID: e.cfg.Project, ExecutionID: id, SourceSHA256: r.Request.Plan.SourceSHA256, ToolchainSHA256: r.Request.ToolchainSHA256, Architecture: r.Request.Plan.Architecture, Outcome: outcome, FailureLog: buildlog.Sanitize(r.FailureLog), Retired: r.State == "retired", ObservedAt: time.Now()}, nil
}
func (e *Executor) ReadNodeArtifact(ctx context.Context, id string) (portal.NodeArtifactObservation, []byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !validID(id) {
		return portal.NodeArtifactObservation{}, nil, ErrExecutor
	}
	r, err := e.load(id)
	if err != nil || r.State != "retired" || r.Outcome != "succeeded" {
		return portal.NodeArtifactObservation{}, nil, ErrExecutor
	}
	if r.Artifact != "" {
		a, err := os.ReadFile(r.Artifact)
		if err != nil {
			return portal.NodeArtifactObservation{}, nil, ErrExecutor
		}
		sum := sha256.Sum256(a)
		return portal.NodeArtifactObservation{NodeExecutionObservation: portal.NodeExecutionObservation{ProjectID: e.cfg.Project, ExecutionID: id, SourceSHA256: r.Request.Plan.SourceSHA256, ToolchainSHA256: r.Request.ToolchainSHA256, Architecture: r.Request.Plan.Architecture, Outcome: "succeeded", Retired: true, ObservedAt: time.Now()}, ArtifactSHA256: hex.EncodeToString(sum[:]), DependencyManifestSHA256: r.Request.Bundle.ManifestSHA256}, a, nil
	}
	m, _ := filepath.Glob(filepath.Join(r.Output, "source-*"))
	if len(m) != 1 {
		return portal.NodeArtifactObservation{}, nil, ErrExecutor
	}
	info, err := os.Lstat(m[0])
	if err != nil || !info.IsDir() {
		return portal.NodeArtifactObservation{}, nil, ErrExecutor
	}
	if err = exec.CommandContext(ctx, "mount", "-o", "remount,ro", r.Output).Run(); err != nil {
		return portal.NodeArtifactObservation{}, nil, err
	}
	root, err := os.OpenRoot(m[0])
	if err != nil {
		return portal.NodeArtifactObservation{}, nil, ErrExecutor
	}
	defer root.Close()
	a, manifest, err := nodeartifact.Export(ctx, root)
	if err != nil {
		return portal.NodeArtifactObservation{}, nil, ErrExecutor
	}
	sum := sha256.Sum256(a)
	o := portal.NodeArtifactObservation{NodeExecutionObservation: portal.NodeExecutionObservation{ProjectID: e.cfg.Project, ExecutionID: id, SourceSHA256: r.Request.Plan.SourceSHA256, ToolchainSHA256: r.Request.ToolchainSHA256, Architecture: r.Request.Plan.Architecture, Outcome: "succeeded", Retired: true, ObservedAt: time.Now()}, ArtifactSHA256: hex.EncodeToString(sum[:]), DependencyManifestSHA256: r.Request.Bundle.ManifestSHA256}
	_ = manifest
	artifactPath := filepath.Join(e.cfg.StateDirectory, id+".artifact.zip")
	f, err := os.OpenFile(artifactPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return portal.NodeArtifactObservation{}, nil, ErrExecutor
	}
	if _, err = f.Write(a); err == nil {
		err = f.Sync()
	}
	if ce := f.Close(); err == nil {
		err = ce
	}
	if err != nil {
		return portal.NodeArtifactObservation{}, nil, ErrExecutor
	}
	r.Artifact = artifactPath
	if err = e.save(id, r); err != nil {
		return portal.NodeArtifactObservation{}, nil, ErrExecutor
	}
	root.Close()
	if e.cleanup(ctx, id, r) == nil {
		r.Cleaned = true
		if err = e.save(id, r); err != nil {
			return portal.NodeArtifactObservation{}, nil, err
		}
	}
	return o, a, nil
}

// Retirement is durable before cleanup. No request can restart this ID.
func (e *Executor) cleanup(ctx context.Context, id string, r record) error {
	_ = exec.CommandContext(ctx, "docker", "rm", r.Container).Run()
	mounted := exec.CommandContext(ctx, "mountpoint", "-q", r.Output).Run()
	if mounted == nil {
		if err := exec.CommandContext(ctx, "umount", r.Output).Run(); err != nil {
			return err
		}
	} else {
		var x *exec.ExitError
		if !errors.As(mounted, &x) || (x.ExitCode() != 1 && x.ExitCode() != 32) {
			return mounted
		}
	}
	for _, path := range []string{filepath.Join(e.cfg.StateDirectory, id+".output.ext4"), filepath.Join(e.cfg.StateDirectory, id+".zip"), filepath.Join(e.cfg.StateDirectory, id+".request.json")} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := os.RemoveAll(r.Output); err != nil {
		return err
	}
	if strings.HasPrefix(r.Request.Bundle.Directory, "dependencies-") && validID(strings.TrimPrefix(r.Request.Bundle.Directory, "dependencies-")) {
		return os.RemoveAll(filepath.Join(e.cfg.DependenciesDirectory, r.Request.Bundle.Directory))
	}
	return nil
}

var _ portal.NodeBuildExecutor = (*Executor)(nil)
