//go:build integration

package portal

import (
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodebuild"
	"strings"
	"testing"
)

func TestNodeBuildProgressReadsPersistedStages(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	a, session := verifiedAccount(t, s, "build-progress@example.test")
	p, err := s.CreateProject(ctx, session.Token, a.WorkspaceID, "Progress", "node")
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.SaveUpload(ctx, session.Token, p.ID, nodeUploadFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	j, err := s.RequestNodeBuild(ctx, session.Token, p.ID, u.ID, strings.Repeat("k", 16), NodeBuildAssignment{Settings: nodebuild.Settings{Architecture: "arm64"}, ToolchainSHA256: strings.Repeat("a", 64)})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ state, intent, result, phase string }{
		{"running", "", "", "preparing"},
		{"running", `{"internal":"SECRET"}`, "", "submitted"},
		{"failed", "", `{"reason":"preparation_expired_before_dispatch"}`, "failed"},
	} {
		if _, err = s.db.Exec("UPDATE node_builds SET state=?,dispatch_intent=?,result=? WHERE id=?", tc.state, []byte(tc.intent), []byte(tc.result), j.ID); err != nil {
			t.Fatal(err)
		}
		builds, err := s.NodeBuilds(ctx, session.Token, p.ID)
		if err != nil || len(builds) != 1 {
			t.Fatalf("read: %v", err)
		}
		if builds[0].Phase != tc.phase || builds[0].Message == "" || strings.Contains(builds[0].Message, "SECRET") {
			t.Fatalf("stage: %+v", builds[0])
		}
	}
}
