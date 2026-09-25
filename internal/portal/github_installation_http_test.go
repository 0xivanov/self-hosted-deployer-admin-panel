package portal

import (
	"context"
	"encoding/json"
	"testing"
)

type githubInstallationHTTPProvider struct {
	githubHTTPProvider
	calls int
}

func (p *githubInstallationHTTPProvider) InstallationURL(context.Context) (string, error) {
	p.calls++
	return "https://github.com/apps/launchstead/installations/new", nil
}
func TestGitHubInstallationGuideRequiresProjectOwner(t *testing.T) {
	s, owner, _, client, _, project := projectClientFixture(t)
	provider := &githubInstallationHTTPProvider{}
	h, err := NewHTTP(s, HTTPOptions{Origin: "https://portal.example.test", GitHubApp: provider, GitHubOAuth: provider})
	if err != nil {
		t.Fatal(err)
	}
	ownerCookie, _ := httpLogin(t, h, owner.Email)
	otherCookie, _ := httpLogin(t, h, client.Email)
	path := "/api/github/installation?project=" + project.ID
	if w := portalRequest(h, "GET", path, "", "", "", otherCookie); w.Code != 403 || provider.calls != 0 {
		t.Fatal("non-owner requested provider metadata", w.Code, provider.calls)
	}
	w := portalRequest(h, "GET", path, "", "", "", ownerCookie)
	var response struct {
		URL string `json:"url"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil || response.URL != "https://github.com/apps/launchstead/installations/new" || provider.calls != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
	var pending int
	if err = s.db.QueryRow("SELECT count(*) FROM github_link_states").Scan(&pending); err != nil || pending != 0 {
		t.Fatal("guide changed connection state", err)
	}
}
