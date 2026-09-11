//go:build integration

package portal

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodebuild"
)

func TestNodeHTTPControlsAndTenantBoundary(t *testing.T) {
	t.Parallel()
	s, _, a, _, release := deploymentFixture(t)
	config := map[string]NodeProjectConfig{release.ProjectID: {RuntimeID: strings.Repeat("b", 64), Build: NodeBuildAssignment{Settings: nodebuild.Settings{Architecture: release.Architecture}, ToolchainSHA256: release.ToolchainSHA256}}}
	h, err := NewHTTP(s, HTTPOptions{Origin: "https://portal.example.test", NodeProjects: config})
	if err != nil {
		t.Fatal(err)
	}
	delete(config, release.ProjectID)
	cookie, csrf := httpLogin(t, h, a.Email)
	path := "/api/node?project=" + release.ProjectID
	w := portalRequest(h, "GET", path, "", "", "", cookie)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"available":true`) || !strings.Contains(w.Body.String(), release.BuildID) {
		t.Fatal(w.Code, w.Body.String())
	}
	body := `{"project":"` + release.ProjectID + `","release":"` + release.BuildID + `","key":"node-http-deploy-key"}`
	if w = portalRequest(h, "POST", "/api/node/deployments", body, h.origin, "", cookie); w.Code != 403 {
		t.Fatal("missing CSRF", w.Code)
	}
	other, _ := verifiedAccount(t, s, "node-http-other@example.test")
	otherCookie, otherCSRF := httpLogin(t, h, other.Email)
	if w = portalRequest(h, "GET", path, "", "", "", otherCookie); w.Code != 403 {
		t.Fatal("foreign history", w.Code)
	}
	if w = portalRequest(h, "POST", "/api/node/deployments", body, h.origin, otherCSRF, otherCookie); w.Code != 403 {
		t.Fatal("foreign deploy", w.Code)
	}
	if _, err = s.db.Exec("INSERT INTO memberships VALUES(?,?,'viewer')", other.ID, a.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if w = portalRequest(h, "POST", "/api/node/deployments", body, h.origin, otherCSRF, otherCookie); w.Code != 403 {
		t.Fatal("viewer deploy", w.Code)
	}
	w = portalRequest(h, "POST", "/api/node/deployments", body, h.origin, csrf, cookie)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var job NodeDeployment
	if err = json.Unmarshal(w.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	if job.RuntimeID != strings.Repeat("b", 64) || job.State != "queued" {
		t.Fatal(job)
	}
	if w = portalRequest(h, "POST", "/api/node/deployments", body, h.origin, csrf, cookie); w.Code != 200 {
		t.Fatal("idempotent retry", w.Code)
	}
	cancel := `{"project":"` + release.ProjectID + `","id":"` + job.ID + `"}`
	if w = portalRequest(h, "POST", "/api/node/deployments/cancel", cancel, h.origin, csrf, cookie); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var upload string
	if err = s.db.QueryRow("SELECT upload_id FROM node_builds WHERE id=?", release.BuildID).Scan(&upload); err != nil {
		t.Fatal(err)
	}
	buildBody := `{"project":"` + release.ProjectID + `","upload":"` + upload + `","key":"node-http-build-key"}`
	w = portalRequest(h, "POST", "/api/node/builds", buildBody, h.origin, csrf, cookie)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var build NodeBuild
	if err = json.Unmarshal(w.Body.Bytes(), &build); err != nil {
		t.Fatal(err)
	}
	if build.State != "queued" || build.ToolchainSHA256 != release.ToolchainSHA256 {
		t.Fatal(build)
	}
	cancel = `{"project":"` + release.ProjectID + `","id":"` + build.ID + `"}`
	if w = portalRequest(h, "POST", "/api/node/builds/cancel", cancel, h.origin, csrf, cookie); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	disabled, err := NewHTTP(s, HTTPOptions{Origin: h.origin})
	if err != nil {
		t.Fatal(err)
	}
	if w = portalRequest(disabled, "POST", "/api/node/builds", buildBody, h.origin, csrf, cookie); w.Code != 403 {
		t.Fatal("unassigned build", w.Code)
	}
}
