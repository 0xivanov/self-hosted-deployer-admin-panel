package portal

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/githubdeploy"
)

type githubHTTPProvider struct {
	state, verifier string
	exchangeCalls   int
	duringExchange  func()
}

func (p *githubHTTPProvider) AuthorizationURL(state, verifier string) (string, error) {
	p.state = state
	p.verifier = verifier
	return "https://github.com/login/oauth/authorize?state=" + url.QueryEscape(state), nil
}
func (p *githubHTTPProvider) Exchange(_ context.Context, code, verifier string) (githubdeploy.OAuthToken, error) {
	p.exchangeCalls++
	if code != "provider-code" || verifier != p.verifier {
		return githubdeploy.OAuthToken{}, githubdeploy.ErrGitHubOAuth
	}
	if p.duringExchange != nil {
		p.duringExchange()
	}
	return githubdeploy.OAuthToken{AccessToken: "temporary-user-token"}, nil
}
func (p *githubHTTPProvider) VerifyRepositoryAccess(_ context.Context, token string, installation, repository int64) (githubdeploy.RepositoryAccess, error) {
	if token != "temporary-user-token" || installation != 20 || repository != 30 {
		return githubdeploy.RepositoryAccess{}, githubdeploy.ErrGitHubAccess
	}
	return githubAccessFixture(), nil
}
func (p *githubHTTPProvider) ListAccessibleRepositories(_ context.Context, token string) ([]githubdeploy.RepositoryAccess, error) {
	if token != "temporary-user-token" {
		return nil, githubdeploy.ErrGitHubAccess
	}
	return []githubdeploy.RepositoryAccess{githubAccessFixture()}, nil
}

func TestGitHubHTTPConnectionAndDisconnect(t *testing.T) {
	s, owner, _, _, _, project := projectClientFixture(t)
	provider := &githubHTTPProvider{}
	h, err := NewHTTP(s, HTTPOptions{Origin: "https://portal.example.test", GitHubApp: provider, GitHubOAuth: provider})
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf := httpLogin(t, h, owner.Email)
	post := func(path, body string) int {
		t.Helper()
		w := portalRequest(h, "POST", path, body, h.origin, csrf, cookie)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "temporary-user-token") {
			t.Fatal("token leak")
		}
		return w.Code
	}
	body := `{"project":"` + project.ID + `"}`
	if w := portalRequest(h, "POST", "/api/github/start", body, h.origin, "", cookie); w.Code != 403 {
		t.Fatal("missing csrf accepted")
	}
	post("/api/github/start", body)
	callback := `{"code":"provider-code","state":"` + provider.state + `"}`
	post("/api/github/callback", callback)
	if w := portalRequest(h, "POST", "/api/github/callback", callback, h.origin, csrf, cookie); w.Code != 409 || provider.exchangeCalls != 1 {
		t.Fatal("callback replay", w.Code, provider.exchangeCalls)
	}
	w := portalRequest(h, "GET", "/api/github/repositories?project="+project.ID, "", "", "", cookie)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "developer/website") {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := portalRequest(h, "POST", "/api/github/connect", `{"project":"`+project.ID+`","installation_id":20,"repository_id":30,"branch":"../invalid","directory":""}`, h.origin, csrf, cookie); w.Code != 400 {
		t.Fatal("invalid selection", w.Code, w.Body.String())
	}
	post("/api/github/connect", `{"project":"`+project.ID+`","installation_id":20,"repository_id":30,"branch":"main","directory":""}`)
	w = portalRequest(h, "GET", "/api/github/connection?project="+project.ID, "", "", "", cookie)
	var got struct {
		Connection GitHubConnection
		Pending    bool
	}
	if json.Unmarshal(w.Body.Bytes(), &got) != nil || !got.Connection.Connected || got.Connection.DeployOnPush || got.Pending {
		t.Fatal(w.Body.String())
	}
	post("/api/github/disconnect", body)
	w = portalRequest(h, "GET", "/api/github/connection?project="+project.ID, "", "", "", cookie)
	if json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Connection.Connected {
		t.Fatal(w.Body.String())
	}
}
func TestGitHubHTTPRejectsWrongSessionAndLateExchange(t *testing.T) {
	s, owner, session, client, _, project := projectClientFixture(t)
	provider := &githubHTTPProvider{}
	h, err := NewHTTP(s, HTTPOptions{Origin: "https://portal.example.test", GitHubApp: provider, GitHubOAuth: provider})
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf := httpLogin(t, h, owner.Email)
	other, otherCSRF := httpLogin(t, h, client.Email)
	body := `{"project":"` + project.ID + `"}`
	w := portalRequest(h, "POST", "/api/github/start", body, h.origin, csrf, cookie)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	callback := `{"code":"provider-code","state":"` + provider.state + `"}`
	w = portalRequest(h, "POST", "/api/github/callback", callback, h.origin, otherCSRF, other)
	if w.Code != 409 || provider.exchangeCalls != 0 {
		t.Fatal("cross-session callback", w.Code)
	}
	provider.duringExchange = func() {
		if err := s.DisconnectGitHub(t.Context(), session.Token, project.ID); err != nil {
			t.Fatal(err)
		}
	}
	w = portalRequest(h, "POST", "/api/github/callback", callback, h.origin, csrf, cookie)
	if w.Code != 409 {
		t.Fatal("late response restored selection", w.Code, w.Body.String())
	}
	h.githubFlows.Lock()
	count := len(h.githubFlows.selections)
	h.githubFlows.Unlock()
	if count != 0 {
		t.Fatal("stale credential retained")
	}
}
func TestGitHubCallbackLandingAndFeatureDisabled(t *testing.T) {
	s, _ := newStore(t)
	h, err := NewHTTP(s, HTTPOptions{Origin: "https://portal.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	w := portalRequest(h, "GET", "/github/callback?code=provider-code&state=random", "", "", "", nil)
	if w.Code != http.StatusOK || w.Header().Get("Referrer-Policy") != "no-referrer" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, w.Header())
	}
	if strings.Contains(w.Body.String(), "provider-code") {
		t.Fatal("callback reflected")
	}
	if _, err = NewHTTP(s, HTTPOptions{Origin: "https://portal.example.test", GitHubApp: &githubHTTPProvider{}}); err == nil {
		t.Fatal("incomplete config accepted")
	}
}
