//go:build integration

package portal

import (
	"context"
	"encoding/json"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/registryimage"
	"strings"
	"testing"
)

func TestContainerPrivateHTTPCreateCheckAndKeepCredentialsOutOfHistory(t *testing.T) {
	s, owner, session, project := containerProjectFixture(t)
	vault, err := NewContainerCredentials(s, []byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	h, err := NewHTTP(s, HTTPOptions{ContainerHosting: true, ContainerCredentials: vault, Origin: "https://portal.example.test", ContainerRegistryResolver: func(_ context.Context, reference string, c registryimage.Credentials) (registryimage.Candidate, error) {
		calls++
		if c.Username != "robot" || c.Password != "private-token" {
			t.Fatal("wrong resolver credentials")
		}
		return containerCandidate(reference), nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf := httpLogin(t, h, owner.Email)
	body := `{"project":"` + project.ID + `","key":"private-credential-key","label":"GHCR access","registry":"ghcr.io","username":"robot","password":"private-token"}`
	denied := portalRequest(h, "POST", "/api/container/credentials", body, h.origin, "", cookie)
	if denied.Code != 403 {
		t.Fatalf("missing CSRF accepted: %d", denied.Code)
	}
	saved := portalRequest(h, "POST", "/api/container/credentials", body, h.origin, csrf, cookie)
	if saved.Code != 200 {
		t.Fatalf("credential creation: %d %s", saved.Code, saved.Body.String())
	}
	var credential ContainerCredential
	if err := json.Unmarshal(saved.Body.Bytes(), &credential); err != nil {
		t.Fatal(err)
	}
	releaseBody := `{"project":"` + project.ID + `","key":"private-release-request-key","reference":"ghcr.io/acme/demo:tag","credential_id":"` + credential.ID + `","port":8080,"health_path":"/"}`
	result := portalRequest(h, "POST", "/api/container/releases", releaseBody, h.origin, csrf, cookie)
	if result.Code != 200 {
		t.Fatalf("private release: %d %s", result.Code, result.Body.String())
	}
	retry := portalRequest(h, "POST", "/api/container/releases", releaseBody, h.origin, csrf, cookie)
	if retry.Code != 200 || calls != 1 {
		t.Fatal("retry performed another credential lookup/network check")
	}
	history := portalRequest(h, "GET", "/api/container?project="+project.ID, "", "", "", cookie)
	if history.Code != 200 || !strings.Contains(history.Body.String(), `"private_images":true`) || !strings.Contains(history.Body.String(), credential.ID) {
		t.Fatal("private release metadata missing")
	}
	for _, raw := range []string{saved.Body.String(), result.Body.String(), history.Body.String()} {
		if strings.Contains(raw, "private-token") || strings.Contains(raw, "robot") || strings.Contains(raw, "ciphertext") {
			t.Fatal("login information exposed")
		}
	}
	// A credential on another website is not usable, even by the same owner.
	other, err := s.CreateProject(t.Context(), session.Token, owner.WorkspaceID, "other-container", "container")
	if err != nil {
		t.Fatal(err)
	}
	foreignBody := strings.ReplaceAll(releaseBody, project.ID, other.ID)
	denied = portalRequest(h, "POST", "/api/container/releases", foreignBody, h.origin, csrf, cookie)
	if denied.Code != 400 || calls != 1 {
		t.Fatalf("foreign credential reached registry: status=%d calls=%d", denied.Code, calls)
	}
}
