package portal

import (
	"encoding/json"
	"testing"
)

type githubImportHTTPProvider struct {
	githubHTTPProvider
	githubWorkerSource
}

func TestGitHubImportHTTPQueuesOnceAndReportsUpload(t *testing.T) {
	s, owner, session, client, _, project := projectClientFixture(t)
	attempt := githubConnectionFixture(t, s, session.Token, project.ID)
	if _, err := s.SaveGitHubConnection(t.Context(), session.Token, attempt, githubAccessFixture(), "main", "", false); err != nil {
		t.Fatal(err)
	}
	provider := &githubImportHTTPProvider{githubWorkerSource: githubWorkerSource{archive: githubWorkerArchive(t)}}
	h, err := NewHTTP(s, HTTPOptions{Origin: "https://portal.example.test", GitHubApp: provider, GitHubOAuth: provider})
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf := httpLogin(t, h, owner.Email)
	body := `{"project":"` + project.ID + `","request_key":"import-http-0000001"}`
	if w := portalRequest(h, "POST", "/api/github/imports", body, h.origin, "", cookie); w.Code != 403 {
		t.Fatal("missing csrf", w.Code)
	}
	var first string
	for range 2 {
		w := portalRequest(h, "POST", "/api/github/imports", body, h.origin, csrf, cookie)
		var result struct{ Import GitHubImport }
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil {
			t.Fatal(w.Code, w.Body.String())
		}
		if first == "" {
			first = result.Import.ID
		} else if result.Import.ID != first {
			t.Fatal("duplicate job")
		}
	}
	other, otherCSRF := httpLogin(t, h, client.Email)
	if w := portalRequest(h, "GET", "/api/github/imports?project="+project.ID, "", "", "", other); w.Code != 403 {
		t.Fatal("cross-user history", w.Code)
	}
	if w := portalRequest(h, "POST", "/api/github/imports", body, h.origin, otherCSRF, other); w.Code != 403 {
		t.Fatal("cross-user request", w.Code)
	}
	if worked, err := s.WorkGitHubImport(t.Context(), provider); err != nil || !worked {
		t.Fatal(worked, err)
	}
	w := portalRequest(h, "GET", "/api/github/imports?project="+project.ID, "", "", "", cookie)
	var result struct{ Imports []GitHubImport }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Imports) != 1 || result.Imports[0].State != "succeeded" || result.Imports[0].UploadID == "" {
		t.Fatal(w.Code, w.Body.String())
	}
}
