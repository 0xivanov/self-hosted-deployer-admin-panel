//go:build linux

package containerbuild

import (
	"context"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodebuild"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/portal"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRejectsInvalidAssignment(t *testing.T) {
	if _, err := New(Config{Project: "bad", ToolchainSHA256: strings.Repeat("a", 64), Architecture: "arm64", StateDirectory: t.TempDir(), DependenciesDirectory: t.TempDir(), Image: "builder"}); err == nil {
		t.Fatal("accepted invalid project")
	}
}
func TestReplayIdentityIsFenced(t *testing.T) {
	e, err := New(Config{Project: strings.Repeat("a", 64), ToolchainSHA256: strings.Repeat("b", 64), Architecture: "arm64", StateDirectory: t.TempDir(), DependenciesDirectory: t.TempDir(), Image: "builder"})
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("c", 64)
	q := portal.NodeExecutionRequest{ExecutionID: id, ProjectID: e.cfg.Project, Plan: nodebuild.Plan{Architecture: "arm64", SourceSHA256: strings.Repeat("d", 64)}, ToolchainSHA256: e.cfg.ToolchainSHA256, NotAfter: 9999999999}
	if err = e.save(id, record{Request: q, State: "submitted"}); err != nil {
		t.Fatal(err)
	}
	q.Plan.SourceSHA256 = strings.Repeat("e", 64)
	if err = e.SubmitNodeExecution(context.Background(), q); err == nil {
		t.Fatal("replay identity accepted")
	}
}

func TestFailedBuildCapturesSanitizedOutputBeforeCleanup(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\ncase \"$1\" in\ninspect) printf '%s' '{\"Status\":\"exited\",\"ExitCode\":1}' ;;\nlogs) printf '%s\\n' 'Error: missing entry point' 'TOKEN=PRIVATE-CREDENTIAL' ;;\n*) exit 1 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	e := &Executor{cfg: Config{Project: strings.Repeat("a", 64), StateDirectory: t.TempDir()}}
	id := strings.Repeat("c", 64)
	r := record{State: "submitted", Container: "synthetic-test", Cleaned: true, Request: portal.NodeExecutionRequest{ExecutionID: id}}
	if err := e.save(id, r); err != nil {
		t.Fatal(err)
	}
	observation, err := e.InspectNodeExecution(t.Context(), id)
	if err != nil || !strings.Contains(observation.FailureLog, "missing entry point") || strings.Contains(observation.FailureLog, "PRIVATE-CREDENTIAL") {
		t.Fatal(observation, err)
	}
	saved, err := e.load(id)
	if err != nil || saved.FailureLog != observation.FailureLog || saved.State != "retired" {
		t.Fatal("output not retained", err)
	}
}
