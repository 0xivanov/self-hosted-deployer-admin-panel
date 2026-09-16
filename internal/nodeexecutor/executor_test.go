//go:build integration

package nodeexecutor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodebuild"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/npmfetch"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/portal"
)

func testConfig(t *testing.T) (Config, string, string) {
	t.Helper()
	d := t.TempDir()
	_ = os.Chmod(d, 0700)
	ex := filepath.Join(d, "executions")
	deps := filepath.Join(d, "deps")
	_ = os.Mkdir(ex, 0700)
	_ = os.Mkdir(deps, 0700)
	pc := filepath.Join(d, "config.json")
	ps := filepath.Join(d, "pipeline.sh")
	_ = os.WriteFile(pc, []byte(`{"TemplateDirectory":"/tmp/template","DependenciesDirectory":"`+deps+`","Launcher":"/tmp/launcher","Importer":"/tmp/importer"}`), 0600)
	_ = os.WriteFile(ps, []byte("#!/bin/sh\nsleep 0.15\n"), 0700)
	return Config{Project: strings.Repeat("a", 64), Toolchain: strings.Repeat("b", 64), Architecture: "arm64", Executions: ex, Dependencies: deps, PipelineConfig: pc, PipelineScript: ps, Python: "/bin/sh"}, d, ex
}
func TestNewTakesExclusiveLockAndCreatesIt(t *testing.T) {
	c, _, _ := testConfig(t)
	e, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if _, err = os.Stat(filepath.Join(c.Executions, ".executor.lock")); err != nil {
		t.Fatal(err)
	}
	if _, err = New(c); err == nil {
		t.Fatal("second executor acquired lock")
	}
}
func requestFor(id string) portal.NodeExecutionRequest {
	return portal.NodeExecutionRequest{ExecutionID: id, BuildID: strings.Repeat("c", 64), ProjectID: strings.Repeat("a", 64), Plan: nodebuild.Plan{Version: 1, SourceSHA256: strings.Repeat("d", 64), OS: "linux", Architecture: "arm64", NodeMajor: 24}, ToolchainSHA256: strings.Repeat("b", 64), Bundle: npmfetch.Bundle{Directory: "dependencies-" + strings.Repeat("e", 64), ManifestSHA256: strings.Repeat("f", 64)}, NotAfter: time.Now().Add(time.Minute).Unix()}
}
func TestReplayRejectsChangedIdentity(t *testing.T) {
	c, _, _ := testConfig(t)
	e, _ := New(c)
	defer e.Close()
	id := strings.Repeat("1", 64)
	q := requestFor(id)
	q.Archive = []byte("saved-source")
	hash := sha256.Sum256(q.Archive)
	q.Plan.SourceSHA256 = hex.EncodeToString(hash[:])
	original := q
	_ = e.root.Mkdir(id, 0700)
	r, _ := e.root.OpenRoot(id)
	raw, _ := json.Marshal(q)
	_ = write(r, "request.json", raw)
	r.Close()
	if err := e.SubmitNodeExecution(context.Background(), q); err != nil {
		t.Fatal("exact replay rejected", err)
	}
	q.Bundle.ManifestSHA256 = strings.Repeat("0", 64)
	if err := e.SubmitNodeExecution(context.Background(), q); err == nil {
		t.Fatal("changed bundle replay accepted")
	}
	q = original
	q.NotAfter = time.Now().Add(2 * time.Minute).Unix()
	if err := e.SubmitNodeExecution(context.Background(), q); err == nil {
		t.Fatal("changed deadline replay accepted")
	}
}
func TestUnsafeExecutionIDsRejected(t *testing.T) {
	c, _, _ := testConfig(t)
	e, _ := New(c)
	defer e.Close()
	if _, err := e.InspectNodeExecution(context.Background(), "../x"); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if _, _, err := e.ReadNodeArtifact(context.Background(), "../x"); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
}
func TestCloseDrainsActivePipeline(t *testing.T) {
	c, _, _ := testConfig(t)
	e, _ := New(c)
	id := strings.Repeat("2", 64)
	cmd := exec.Command("/bin/sh", "-c", "sleep 0.15")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	e.running, e.active, e.done = cmd, id, make(chan struct{})
	done := e.done
	e.mu.Unlock()
	go func() { _ = cmd.Wait(); e.mu.Lock(); close(done); e.mu.Unlock() }()
	started := time.Now()
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	if time.Since(started) < 100*time.Millisecond {
		t.Fatal("Close did not drain runner")
	}
}
