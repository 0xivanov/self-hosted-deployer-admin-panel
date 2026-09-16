//go:build linux

package containerbuild

import (
	"context"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodebuild"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/portal"
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
