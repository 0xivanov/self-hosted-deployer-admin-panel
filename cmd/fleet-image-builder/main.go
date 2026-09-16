// fleet-image-builder packages validated bytes without executing customer code.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodeartifact"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/projectarchive"
)

const registry = "10.8.0.1:5000"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func id(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && hex.EncodeToString(b) == s
}
func run() error {
	if len(os.Args) != 5 {
		return errors.New("expected kind, project, digest, archive")
	}
	kind, project, digest := os.Args[1], os.Args[2], os.Args[3]
	if (kind != "node" && kind != "static") || !id(project) || !id(digest) {
		return errors.New("invalid release identity")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if filepath.Dir(os.Args[4]) != "/var/lib/launchstead-fleet" {
		return errors.New("archive outside release staging")
	}
	staging, err := os.OpenRoot("/var/lib/launchstead-fleet")
	if err != nil {
		return err
	}
	defer staging.Close()
	f, err := staging.Open(filepath.Base(os.Args[4]))
	if err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(f, nodeartifact.MaxCompressed+1))
	f.Close()
	if err != nil || len(data) > nodeartifact.MaxCompressed || fmt.Sprintf("%x", sha256.Sum256(data)) != digest {
		return errors.New("archive integrity mismatch")
	}
	baseBytes, err := os.ReadFile("/etc/launchstead-registry/runtime-image")
	if err != nil {
		return err
	}
	base := strings.TrimSpace(string(baseBytes))
	if !strings.HasPrefix(base, registry+"/runtime@sha256:") || !id(strings.TrimPrefix(base, registry+"/runtime@sha256:")) {
		return errors.New("invalid pinned runtime")
	}
	build, err := os.MkdirTemp("/var/lib/launchstead-image-builder", "image-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(build)
	helper, err := os.ReadFile("/opt/launchstead-fleet/fleet-content-arm64")
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(build, "content"), helper, 0755); err != nil {
		return err
	}
	dockerfile := "FROM scratch\nCOPY --chown=10001:10001 content /content\nCOPY --chown=10001:10001 site.zip /site.zip\nUSER 10001:10001\nEXPOSE 8080\nENTRYPOINT [\"/content\"]\n"
	if kind == "static" {
		if _, err = projectarchive.Validate(ctx, data, "static"); err != nil {
			return err
		}
		if err = os.WriteFile(filepath.Join(build, "site.zip"), data, 0644); err != nil {
			return err
		}
	} else {
		root, err := os.OpenRoot(build)
		if err != nil {
			return err
		}
		release, err := nodeartifact.Extract(ctx, data, digest, root)
		root.Close()
		if err != nil {
			return err
		}
		if err = os.Rename(filepath.Join(build, release.Directory), filepath.Join(build, "app")); err != nil {
			return err
		}
		dockerfile = "FROM " + base + "\nCOPY --chown=10001:10001 content /content\nCOPY --chown=10001:10001 app /app\nENV SITE_KIND=node\nUSER 10001:10001\nEXPOSE 8080\nENTRYPOINT [\"/content\"]\n"
	}
	if err = os.WriteFile(filepath.Join(build, "Dockerfile"), []byte(dockerfile), 0600); err != nil {
		return err
	}
	// Include operator runtime and entrypoint bytes in the tag so upgrades never
	// silently replace an earlier immutable release.
	identity := sha256.Sum256(append(append([]byte(kind+digest+base), helper...), []byte(dockerfile)...))
	tag := registry + "/sites/" + project + ":" + hex.EncodeToString(identity[:])
	for _, args := range [][]string{{"build", "--platform", "linux/arm64", "--network", "none", "--tag", tag, build}, {"push", tag}} {
		cmd := exec.CommandContext(ctx, "docker", args...)
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		if err = cmd.Run(); err != nil {
			return errors.New("release image packaging failed")
		}
	}
	output, err := exec.CommandContext(ctx, "docker", "image", "inspect", "--format", "{{json .RepoDigests}}", tag).Output()
	if err != nil {
		return err
	}
	var refs []string
	if json.Unmarshal(output, &refs) != nil {
		return errors.New("invalid image digest response")
	}
	for _, ref := range refs {
		prefix := registry + "/sites/" + project + "@sha256:"
		if strings.HasPrefix(ref, prefix) && id(strings.TrimPrefix(ref, prefix)) {
			return json.NewEncoder(os.Stdout).Encode(map[string]string{"image": ref})
		}
	}
	return errors.New("registry did not return immutable digest")
}
