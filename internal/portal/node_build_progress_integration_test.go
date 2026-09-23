//go:build integration

package portal

import (
	"encoding/json"
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
	h, err := NewHTTP(s, HTTPOptions{Origin: "https://portal.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	cookie, _ := httpLogin(t, h, a.Email)
	other, _ := verifiedAccount(t, s, "foreign-progress@example.test")
	otherCookie, _ := httpLogin(t, h, other.Email)
	path := "/api/node?project=" + p.ID
	if response := portalRequest(h, "GET", path, "", "", "", otherCookie); response.Code != 403 {
		t.Fatalf("foreign progress: %d", response.Code)
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
		response := portalRequest(h, "GET", path, "", "", "", cookie)
		if response.Code != 200 {
			t.Fatalf("progress HTTP: %d", response.Code)
		}
		var body struct {
			Builds []map[string]any `json:"builds"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if len(body.Builds) != 1 || body.Builds[0]["phase"] != tc.phase || body.Builds[0]["message"] != builds[0].Message {
			t.Fatalf("missing customer progress: %s", response.Body.String())
		}
		for _, key := range []string{"plan", "dispatch_intent", "toolchain_sha256", "result"} {
			if _, exists := body.Builds[0][key]; exists {
				t.Fatalf("internal field exposed: %s", key)
			}
		}
		if strings.Contains(response.Body.String(), "SECRET") {
			t.Fatal("internal evidence exposed")
		}
	}
}
