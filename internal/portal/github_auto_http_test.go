package portal

import "testing"

func TestGitHubAutoDeploymentHTTPRequiresOwnerCSRFAndRuntime(t *testing.T) {
	s, owner, session, client, _, p := projectClientFixture(t)
	defer s.Close()
	if _, err := s.SaveGitHubConnection(t.Context(), session.Token, githubConnectionFixture(t, s, session.Token, p.ID), githubAccessFixture(), "main", "", false); err != nil {
		t.Fatal(err)
	}
	provider := &githubImportHTTPProvider{}
	opts := HTTPOptions{Origin: "https://portal.example.test", GitHubApp: provider, GitHubOAuth: provider, GitHubWebhookSecret: testGitHubWebhookSecret, GitHubAutoDeploy: true}
	h, err := NewHTTP(s, opts)
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf := httpLogin(t, h, owner.Email)
	body := `{"project":"` + p.ID + `","enabled":true}`
	if w := portalRequest(h, "POST", "/api/github/auto-deploy", body, h.origin, "", cookie); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := portalRequest(h, "POST", "/api/github/auto-deploy", body, h.origin, csrf, cookie); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	opts.PublicationSites = map[string]string{p.ID: "https://content.example.test"}
	h, err = NewHTTP(s, opts)
	if err != nil {
		t.Fatal(err)
	}
	if w := portalRequest(h, "POST", "/api/github/auto-deploy", body, h.origin, csrf, cookie); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	other, otherCSRF := httpLogin(t, h, client.Email)
	if w := portalRequest(h, "POST", "/api/github/auto-deploy", body, h.origin, otherCSRF, other); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := portalRequest(h, "GET", "/api/github/deployments?project="+p.ID, "", "", "", other); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := portalRequest(h, "GET", "/api/github/deployments?project="+p.ID, "", "", "", cookie); w.Code != 200 {
		t.Fatal(w.Code)
	}
	opts.GitHubAutoDeploy = false
	h, err = NewHTTP(s, opts)
	if err != nil {
		t.Fatal(err)
	}
	if w := portalRequest(h, "POST", "/api/github/auto-deploy", body, h.origin, csrf, cookie); w.Code != 404 {
		t.Fatal(w.Code)
	}
}
