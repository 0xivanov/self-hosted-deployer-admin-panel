//go:build integration

package portal

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/registryimage"
)

func TestContainerHTTPOwnerPrepareListPublishCancel(t *testing.T) {
	s, owner, _, project := containerProjectFixture(t)
	runtimeID := strings.Repeat("a", 64)
	calls := 0
	h, err := NewHTTP(s, HTTPOptions{
		ContainerHosting: true,
		ContainerProjects: map[string]ContainerProjectConfig{
			project.ID: {RuntimeID: runtimeID},
		},
		ContainerResolver: func(_ context.Context, _, _ string) (registryimage.Candidate, error) {
			calls++
			return containerCandidate("ghcr.io/acme/demo:tag"), nil
		},
		Origin: "https://portal.example.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf := httpLogin(t, h, owner.Email)

	path := "/api/container?project=" + project.ID
	w := portalRequest(h, "GET", path, "", "", "", cookie)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"available":true`) {
		t.Fatalf("initial container state: %d %s", w.Code, w.Body.String())
	}

	releaseBody := `{"project":"` + project.ID + `","key":"container-http-prepare-key","reference":"ghcr.io/acme/demo:tag","port":8080,"health_path":"/healthz"}`
	w = portalRequest(h, "POST", "/api/container/releases", releaseBody, h.origin, csrf, cookie)
	if w.Code != 200 {
		t.Fatalf("prepare release: %d %s", w.Code, w.Body.String())
	}
	var release ContainerRelease
	if err := json.Unmarshal(w.Body.Bytes(), &release); err != nil {
		t.Fatal(err)
	}
	if release.ProjectID != project.ID || release.Input.Reference != "ghcr.io/acme/demo:tag" || calls != 1 {
		t.Fatalf("release: %+v resolver calls=%d", release, calls)
	}

	w = portalRequest(h, "GET", path, "", "", "", cookie)
	if w.Code != 200 || !strings.Contains(w.Body.String(), release.ID) {
		t.Fatalf("release list: %d %s", w.Code, w.Body.String())
	}

	deployBody := `{"project":"` + project.ID + `","release":"` + release.ID + `","key":"container-http-publish-key"}`
	w = portalRequest(h, "POST", "/api/container/deployments", deployBody, h.origin, csrf, cookie)
	if w.Code != 200 {
		t.Fatalf("publish: %d %s", w.Code, w.Body.String())
	}
	var deployment ContainerDeployment
	if err := json.Unmarshal(w.Body.Bytes(), &deployment); err != nil {
		t.Fatal(err)
	}
	if deployment.RuntimeID != runtimeID || deployment.ReleaseID != release.ID || deployment.State != "queued" {
		t.Fatalf("deployment: %+v", deployment)
	}

	cancelBody := `{"project":"` + project.ID + `","id":"` + deployment.ID + `"}`
	w = portalRequest(h, "POST", "/api/container/deployments/cancel", cancelBody, h.origin, csrf, cookie)
	if w.Code != 200 {
		t.Fatalf("cancel: %d %s", w.Code, w.Body.String())
	}
	w = portalRequest(h, "GET", path, "", "", "", cookie)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"cancelled"`) {
		t.Fatalf("cancelled deployment list: %d %s", w.Code, w.Body.String())
	}
}

func TestContainerHTTPAuthorizationAndCSRF(t *testing.T) {
	s, owner, _, project := containerProjectFixture(t)
	runtimeID := strings.Repeat("b", 64)
	calls := 0
	h, err := NewHTTP(s, HTTPOptions{
		ContainerHosting:  true,
		ContainerProjects: map[string]ContainerProjectConfig{project.ID: {RuntimeID: runtimeID}},
		ContainerResolver: func(_ context.Context, _, _ string) (registryimage.Candidate, error) {
			calls++
			return containerCandidate("ghcr.io/acme/demo:tag"), nil
		},
		Origin: "https://portal.example.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	cookie, _ := httpLogin(t, h, owner.Email)
	body := `{"project":"` + project.ID + `","key":"container-http-auth-key","reference":"ghcr.io/acme/demo:tag","port":8080,"health_path":"/healthz"}`
	if w := portalRequest(h, "POST", "/api/container/releases", body, h.origin, "", cookie); w.Code != 403 {
		t.Fatalf("missing csrf accepted: %d", w.Code)
	}
	if calls != 0 {
		t.Fatalf("resolver called for csrf failure: %d", calls)
	}

	foreign, _ := verifiedAccount(t, s, "container-http-foreign@example.test")
	foreignCookie, foreignCSRF := httpLogin(t, h, foreign.Email)
	if w := portalRequest(h, "GET", "/api/container?project="+project.ID, "", "", "", foreignCookie); w.Code != 403 {
		t.Fatalf("foreign list accepted: %d", w.Code)
	}
	if w := portalRequest(h, "POST", "/api/container/releases", body, h.origin, foreignCSRF, foreignCookie); w.Code != 403 {
		t.Fatalf("foreign prepare accepted: %d", w.Code)
	}
	if calls != 0 {
		t.Fatalf("resolver called for foreign request: %d", calls)
	}
	if _, err := s.db.Exec("INSERT INTO memberships VALUES(?,?,?)", foreign.ID, owner.WorkspaceID, "viewer"); err != nil {
		t.Fatal(err)
	}
	if w := portalRequest(h, "POST", "/api/container/releases", body, h.origin, foreignCSRF, foreignCookie); w.Code != 403 {
		t.Fatalf("viewer prepare accepted: %d", w.Code)
	}
	if calls != 0 {
		t.Fatalf("resolver called for viewer request: %d", calls)
	}
}

func TestContainerHTTPRuntimeIDAndFeatureGates(t *testing.T) {
	s, owner, _, project := containerProjectFixture(t)
	runtimeID := strings.Repeat("c", 64)
	h, err := NewHTTP(s, HTTPOptions{
		ContainerHosting:  true,
		ContainerProjects: map[string]ContainerProjectConfig{project.ID: {RuntimeID: runtimeID}},
		ContainerResolver: func(_ context.Context, _, _ string) (registryimage.Candidate, error) {
			return containerCandidate("ghcr.io/acme/demo:tag"), nil
		},
		Origin: "https://portal.example.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf := httpLogin(t, h, owner.Email)
	prepare := `{"project":"` + project.ID + `","key":"container-http-runtime-prepare-key","reference":"ghcr.io/acme/demo:tag","port":8080,"health_path":"/healthz"}`
	w := portalRequest(h, "POST", "/api/container/releases", prepare, h.origin, csrf, cookie)
	if w.Code != 200 {
		t.Fatalf("prepare runtime test release: %d %s", w.Code, w.Body.String())
	}
	var release ContainerRelease
	if err := json.Unmarshal(w.Body.Bytes(), &release); err != nil {
		t.Fatal(err)
	}
	unknownRuntime := `{"project":"` + project.ID + `","runtime_id":"` + strings.Repeat("d", 64) + `","release":"` + release.ID + `","key":"container-http-runtime-key"}`
	if w = portalRequest(h, "POST", "/api/container/deployments", unknownRuntime, h.origin, csrf, cookie); w.Code != 400 {
		t.Fatalf("browser runtime_id accepted: %d %s", w.Code, w.Body.String())
	}

	disabled, err := NewHTTP(s, HTTPOptions{Origin: h.origin})
	if err != nil {
		t.Fatal(err)
	}
	validBody := `{"project":"` + project.ID + `","key":"container-http-disabled-key","reference":"ghcr.io/acme/demo:tag","port":8080,"health_path":"/healthz"}`
	if w := portalRequest(disabled, "POST", "/api/container/releases", validBody, h.origin, csrf, cookie); w.Code != 409 {
		t.Fatalf("disabled feature status: %d %s", w.Code, w.Body.String())
	}
}

func TestContainerHTTPUnassignedDeployAndResolverPlatformError(t *testing.T) {
	s, owner, _, project := containerProjectFixture(t)
	calls := 0
	h, err := NewHTTP(s, HTTPOptions{
		ContainerHosting: true,
		ContainerResolver: func(_ context.Context, _, _ string) (registryimage.Candidate, error) {
			calls++
			return containerCandidate("ghcr.io/acme/demo:tag"), nil
		},
		Origin: "https://portal.example.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf := httpLogin(t, h, owner.Email)
	body := `{"project":"` + project.ID + `","key":"container-http-unassigned-key","reference":"ghcr.io/acme/demo:tag","port":8080,"health_path":"/healthz"}`
	if w := portalRequest(h, "POST", "/api/container/releases", body, h.origin, csrf, cookie); w.Code != 200 {
		t.Fatalf("unassigned prepare rejected: %d %s", w.Code, w.Body.String())
	}
	if calls != 1 {
		t.Fatalf("resolver calls: %d", calls)
	}
	var release ContainerRelease
	w := portalRequest(h, "POST", "/api/container/releases", body, h.origin, csrf, cookie)
	if err := json.Unmarshal(w.Body.Bytes(), &release); err != nil {
		t.Fatal(err)
	}
	deploy := `{"project":"` + project.ID + `","release":"` + release.ID + `","key":"container-http-unassigned-deploy-key"}`
	if w = portalRequest(h, "POST", "/api/container/deployments", deploy, h.origin, csrf, cookie); w.Code != 409 {
		t.Fatalf("unassigned deploy status: %d %s", w.Code, w.Body.String())
	}

	platform, err := NewHTTP(s, HTTPOptions{
		ContainerHosting:  true,
		ContainerProjects: map[string]ContainerProjectConfig{project.ID: {RuntimeID: strings.Repeat("e", 64)}},
		ContainerResolver: func(_ context.Context, _, _ string) (registryimage.Candidate, error) {
			return registryimage.Candidate{}, registryimage.ErrPlatform
		},
		Origin: h.origin,
	})
	if err != nil {
		t.Fatal(err)
	}
	platformCookie, platformCSRF := httpLogin(t, platform, owner.Email)
	platformBody := `{"project":"` + project.ID + `","key":"container-http-platform-key","reference":"ghcr.io/acme/demo:tag","port":8080,"health_path":"/healthz"}`
	w = portalRequest(platform, "POST", "/api/container/releases", platformBody, platform.origin, platformCSRF, platformCookie)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "Linux ARM64") {
		t.Fatalf("platform error: %d %s", w.Code, w.Body.String())
	}
}
