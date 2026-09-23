//go:build integration

package portal

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildLogHTTPIsolationAndRedaction(t *testing.T) {
	s, _, a, _, release := deploymentFixture(t)
	evidence, _ := json.Marshal(map[string]string{"FailureLog": "Error: cannot find module x\nTOKEN=SECRET", "internal": "PRIVATE-EVIDENCE"})
	if _, err := s.db.Exec("UPDATE node_builds SET result=? WHERE id=?", evidence, release.BuildID); err != nil {
		t.Fatal(err)
	}
	h, err := NewHTTP(s, HTTPOptions{Origin: "https://portal.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	cookie, _ := httpLogin(t, h, a.Email)
	path := "/api/node/build-log?project=" + release.ProjectID + "&id=" + release.BuildID
	r := portalRequest(h, "GET", path, "", "", "", cookie)
	if r.Code != 200 || !strings.Contains(r.Body.String(), "cannot find module") || strings.Contains(r.Body.String(), "SECRET") || strings.Contains(r.Body.String(), "PRIVATE-EVIDENCE") {
		t.Fatal(r.Code, r.Body.String())
	}
	other, _ := verifiedAccount(t, s, "log-other@example.test")
	foreign, _ := httpLogin(t, h, other.Email)
	if r = portalRequest(h, "GET", path, "", "", "", foreign); r.Code != 403 {
		t.Fatal("foreign logs", r.Code)
	}
	if _, err = s.db.Exec("INSERT INTO memberships VALUES(?,?,'viewer')", other.ID, a.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if r = portalRequest(h, "GET", path, "", "", "", foreign); r.Code != 403 {
		t.Fatal("viewer logs", r.Code)
	}
	if r = portalRequest(h, "GET", "/api/node/build-log?project="+release.ProjectID+"&id="+strings.Repeat("0", 64), "", "", "", cookie); r.Code != 404 {
		t.Fatal("foreign build id", r.Code)
	}
}
