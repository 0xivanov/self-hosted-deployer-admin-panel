//go:build integration

package portal

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/registryimage"
)

func TestContainerEnvironmentHTTPReleaseScopeAndSecrecy(t *testing.T) {
	s, owner, session, project := containerProjectFixture(t)
	vault, err := NewContainerEnvironments(s, []byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	h, err := NewHTTP(s, HTTPOptions{ContainerHosting: true, ContainerEnvironments: vault, Origin: "https://portal.example.test", ContainerResolver: func(_ context.Context, _ string, ref string) (registryimage.Candidate, error) {
		calls++
		return containerCandidate(ref), nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf := httpLogin(t, h, owner.Email)
	// Decoded payload is legal but larger than the default 16KiB HTTP limit.
	values := map[string]string{"TOKEN": strings.Repeat("x", 8192), "SECOND": strings.Repeat("y", 8192), "EMPTY": ""}
	raw, _ := json.Marshal(map[string]any{"project": project.ID, "key": "environment-create-key", "label": "Production", "values": values})
	denied := portalRequest(h, "POST", "/api/container/environments", string(raw), h.origin, "", cookie)
	if denied.Code != 403 {
		t.Fatal("environment creation accepted without CSRF")
	}
	saved := portalRequest(h, "POST", "/api/container/environments", string(raw), h.origin, csrf, cookie)
	if saved.Code != 200 {
		t.Fatalf("create: %d %s", saved.Code, saved.Body.String())
	}
	var environment ContainerEnvironment
	if json.Unmarshal(saved.Body.Bytes(), &environment) != nil || environment.ID == "" {
		t.Fatal("missing saved environment")
	}
	releaseBody := `{"project":"` + project.ID + `","key":"environment-release-key","reference":"ghcr.io/acme/demo:tag","environment_id":"` + environment.ID + `","port":8080,"health_path":"/"}`
	result := portalRequest(h, "POST", "/api/container/releases", releaseBody, h.origin, csrf, cookie)
	if result.Code != 200 {
		t.Fatalf("release: %d %s", result.Code, result.Body.String())
	}
	retry := portalRequest(h, "POST", "/api/container/releases", releaseBody, h.origin, csrf, cookie)
	if retry.Code != 200 || calls != 1 {
		t.Fatal("release retry changed metadata")
	}
	history := portalRequest(h, "GET", "/api/container?project="+project.ID, "", "", "", cookie)
	if history.Code != 200 || !strings.Contains(history.Body.String(), `"environment_settings":true`) {
		t.Fatal("environment metadata missing")
	}
	for _, body := range []string{saved.Body.String(), result.Body.String(), history.Body.String()} {
		if strings.Contains(body, values["TOKEN"]) || strings.Contains(body, "ciphertext") || strings.Contains(body, `"values"`) {
			t.Fatal("environment values exposed")
		}
	}
	other, err := s.CreateProject(t.Context(), session.Token, owner.WorkspaceID, "other-environment", "container")
	if err != nil {
		t.Fatal(err)
	}
	denied = portalRequest(h, "POST", "/api/container/releases", strings.ReplaceAll(releaseBody, project.ID, other.ID), h.origin, csrf, cookie)
	if denied.Code != 403 || calls != 1 {
		t.Fatalf("foreign environment reached registry: status=%d calls=%d", denied.Code, calls)
	}
}
