//go:build integration

package portal

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDeploymentProgressReachesCustomerWithoutInternalEvidence(t *testing.T) {
	s, _, a, session, release := deploymentFixture(t)
	ctx := t.Context()
	job, err := s.RequestNodeDeployment(ctx, session.Token, release.ProjectID, release.BuildID, strings.Repeat("k", 16), strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewHTTP(s, HTTPOptions{Origin: "https://portal.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	cookie, _ := httpLogin(t, h, a.Email)
	for _, tc := range []struct{ state, intent, phase string }{{"queued", "", "queued"}, {"running", "", "preparing"}, {"running", `{"private":"SECRET"}`, "verifying"}, {"failed", `{"private":"SECRET"}`, "failed"}} {
		if _, err = s.db.Exec("UPDATE node_deployments SET state=?,dispatch_intent=? WHERE id=?", tc.state, []byte(tc.intent), job.ID); err != nil {
			t.Fatal(err)
		}
		response := portalRequest(h, "GET", "/api/node?project="+release.ProjectID, "", "", "", cookie)
		if response.Code != 200 {
			t.Fatal(response.Code, response.Body.String())
		}
		var body struct {
			Deployments []NodeDeployment `json:"deployments"`
		}
		if err = json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if len(body.Deployments) != 1 || body.Deployments[0].Phase != tc.phase || body.Deployments[0].Message == "" {
			t.Fatalf("missing phase: %s", response.Body.String())
		}
		if strings.Contains(response.Body.String(), "SECRET") || strings.Contains(response.Body.String(), "dispatch_intent") {
			t.Fatal("internal evidence leaked")
		}
	}
}
