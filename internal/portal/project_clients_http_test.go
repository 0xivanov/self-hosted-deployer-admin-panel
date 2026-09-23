package portal

import "testing"

func TestProjectClientsHTTPExplicitGrantAndCSRF(t *testing.T) {
	s, owner, ownerSession, client, clientSession, p := projectClientFixture(t)
	h, err := NewHTTP(s, HTTPOptions{Origin: "https://portal.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf := httpLogin(t, h, owner.Email)
	body := `{"project":"` + p.ID + `","email":"` + client.Email + `","grant":true}`
	if w := portalRequest(h, "POST", "/api/project-clients", body, h.origin, "", cookie); w.Code != 403 {
		t.Fatalf("missing csrf: %d %s", w.Code, w.Body.String())
	}
	if w := portalRequest(h, "POST", "/api/project-clients", body, h.origin, csrf, cookie); w.Code != 200 {
		t.Fatalf("explicit grant: %d %s", w.Code, w.Body.String())
	}
	if w := portalRequest(h, "POST", "/api/project-clients", `{"project":"`+p.ID+`","email":"`+client.Email+`"}`, h.origin, csrf, cookie); w.Code != 400 {
		t.Fatalf("missing grant choice: %d %s", w.Code, w.Body.String())
	}
	clients, err := s.ProjectClients(t.Context(), ownerSession.Token, p.ID)
	if err != nil || len(clients) != 1 || clients[0].Email != client.Email {
		t.Fatalf("grant persistence: %#v %v", clients, err)
	}
	clientCookie, _ := httpLogin(t, h, client.Email)
	if w := portalRequest(h, "POST", "/api/project-clients", body, h.origin, csrfFor(clientCookie.Value), clientCookie); w.Code != 403 {
		t.Fatalf("client mutating grant: %d %s", w.Code, w.Body.String())
	}
	if w := portalRequest(h, "GET", "/api/project-clients?project="+p.ID, "", "", "", clientCookie); w.Code != 403 {
		t.Fatalf("client listing owner data: %d %s", w.Code, w.Body.String())
	}
	if w := portalRequest(h, "GET", "/api/projects?workspace="+owner.WorkspaceID, "", "", "", clientCookie); w.Code != 403 {
		t.Fatalf("client workspace API access: %d %s", w.Code, w.Body.String())
	}
	for _, path := range []string{
		"/api/uploads?project=" + p.ID,
		"/api/project-domains?project=" + p.ID,
		"/api/runtime-logs?project=" + p.ID,
		"/api/node?project=" + p.ID,
		"/api/publications?project=" + p.ID,
	} {
		if w := portalRequest(h, "GET", path, "", "", "", clientCookie); w.Code != 403 {
			t.Fatalf("client API access %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	if websites, err := s.SharedWebsites(t.Context(), clientSession.Token); err != nil || len(websites) != 1 || websites[0].ID != p.ID {
		t.Fatalf("client summary access: %#v %v", websites, err)
	}
}
