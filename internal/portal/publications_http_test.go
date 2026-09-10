//go:build integration

package portal

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPublicationHTTPAssignmentAndRoles(t *testing.T) {
	t.Parallel()
	store, _, a, _, p, u := publicationFixture(t)
	other, _ := verifiedAccount(t, store, "http-foreign@example.test")
	sites := map[string]string{p.ID: "https://fixture.example.net"}
	h, err := NewHTTP(store, HTTPOptions{Origin: "https://portal.example.test", PublicationSites: sites})
	if err != nil {
		t.Fatal(err)
	}
	delete(sites, p.ID) // Configuration is copied, not shared with callers.
	cookie, csrf := httpLogin(t, h, a.Email)
	path := "/api/publications?project=" + p.ID
	w := portalRequest(h, "GET", path, "", "", "", cookie)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"available":true`) {
		t.Fatal(w.Code, w.Body.String())
	}
	disabled, err := NewHTTP(store, HTTPOptions{Origin: h.origin})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"project":"` + p.ID + `","upload":"` + u.ID + `","key":"http-request-key"}`
	if w = portalRequest(disabled, "POST", "/api/publications", body, h.origin, csrf, cookie); w.Code != 403 {
		t.Fatal("unassigned publish accepted", w.Code)
	}
	if w = portalRequest(h, "POST", "/api/publications", body, h.origin, "", cookie); w.Code != 403 {
		t.Fatal("missing csrf accepted", w.Code)
	}
	otherCookie, otherCSRF := httpLogin(t, h, other.Email)
	if w = portalRequest(h, "POST", "/api/publications", body, h.origin, otherCSRF, otherCookie); w.Code != 403 {
		t.Fatal("foreign publish accepted", w.Code)
	}
	if w = portalRequest(h, "GET", path, "", "", "", otherCookie); w.Code != 403 {
		t.Fatal("foreign history exposed", w.Code)
	}
	if _, err = store.db.Exec("INSERT INTO memberships VALUES(?,?,'viewer')", other.ID, a.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if w = portalRequest(h, "GET", path, "", "", "", otherCookie); w.Code != 200 {
		t.Fatal("viewer history denied", w.Code)
	}
	if w = portalRequest(h, "POST", "/api/publications", body, h.origin, otherCSRF, otherCookie); w.Code != 403 {
		t.Fatal("viewer publish accepted", w.Code)
	}
	w = portalRequest(h, "POST", "/api/publications", body, h.origin, csrf, cookie)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var j PublicationJob
	if err = json.Unmarshal(w.Body.Bytes(), &j); err != nil {
		t.Fatal(err)
	}
	repeated := portalRequest(h, "POST", "/api/publications", body, h.origin, csrf, cookie)
	if repeated.Code != 200 || repeated.Body.String() != w.Body.String() {
		t.Fatal("retry changed job", repeated.Code, repeated.Body.String())
	}
	claim, err := store.ClaimPublication(t.Context(), p.ID)
	if err != nil || claim == nil {
		t.Fatal(err)
	}
	if err = store.FinishPublication(t.Context(), j.ID, claim.Lease, claim.SHA256, true); err != nil {
		t.Fatal(err)
	}
	w = portalRequest(h, "GET", path, "", "", "", cookie)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"active":"`+j.ID+`"`) {
		t.Fatal(w.Code, w.Body.String())
	}

}
func TestPublicationOriginConfig(t *testing.T) {
	t.Parallel()
	store, _ := newStore(t)
	for _, tc := range []struct{ name, url string }{
		{"same host", "https://portal.example.test:9999"}, {"plaintext", "http://site.example.net"}, {"credentials", "https://user:password@site.example.net"}, {"path", "https://site.example.net/path"}, {"script", "javascript:alert(1)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewHTTP(store, HTTPOptions{Origin: "https://portal.example.test", PublicationSites: map[string]string{strings.Repeat("a", 64): tc.url}}); err == nil {
				t.Fatal("unsafe content URL")
			}
		})
	}
}

func TestHistoryRetainsOlderActiveRelease(t *testing.T) {
	t.Parallel()
	s, _, a, session, p, u := publicationFixture(t)
	ctx := t.Context()
	j, err := s.RequestPublication(ctx, session.Token, p.ID, u.ID, "old-active-request")
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.ClaimPublication(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.FinishPublication(ctx, j.ID, c.Lease, c.SHA256, true); err != nil {
		t.Fatal(err)
	}
	for revision := 2; revision <= 102; revision++ {
		id := randomToken()
		if _, err = s.db.Exec("INSERT INTO publication_jobs(id,project_id,upload_id,actor_id,request_key,revision,state,created_at) VALUES(?,?,?,?,?,?,'failed',1)", id, p.ID, u.ID, a.ID, id, revision); err != nil {
			t.Fatal(err)
		}
	}
	jobs, active, err := s.PublicationJobs(ctx, session.Token, p.ID)
	if err != nil || active != j.ID || len(jobs) != 101 || jobs[len(jobs)-1].ID != j.ID {
		t.Fatal("active release omitted", len(jobs), active, err)
	}
}
