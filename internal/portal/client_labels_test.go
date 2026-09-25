package portal

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestClientLabelPermissionsAndIsolation(t *testing.T) {
	s, _ := newStore(t)
	owner, session := verifiedAccount(t, s, "label-owner@example.test")
	_, outsider := verifiedAccount(t, s, "label-other@example.test")
	dev, developer := verifiedAccount(t, s, "label-dev@example.test")
	viewer, viewSession := verifiedAccount(t, s, "label-view@example.test")
	client, clientSession := verifiedAccount(t, s, "label-client@example.test")
	for _, m := range []struct{ user, role string }{{dev.ID, "developer"}, {viewer.ID, "viewer"}} {
		if _, err := s.db.Exec("INSERT INTO memberships VALUES(?,?,?)", m.user, owner.WorkspaceID, m.role); err != nil {
			t.Fatal(err)
		}
	}
	p, err := s.CreateProject(t.Context(), session.Token, owner.WorkspaceID, "Website", "static")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ChangeProjectClient(t.Context(), session.Token, p.ID, client.Email, true); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{outsider.Token, viewSession.Token, clientSession.Token} {
		if _, err = s.SetProjectClientLabel(t.Context(), token, p.ID, "Denied"); !errors.Is(err, ErrDenied) {
			t.Fatalf("unauthorized label: %v", err)
		}
	}
	for _, label := range []string{strings.Repeat("x", 101), "bad\nlabel", string([]byte{0xff})} {
		if _, err = s.SetProjectClientLabel(t.Context(), session.Token, p.ID, label); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid label: %v", err)
		}
	}
	got, err := s.SetProjectClientLabel(t.Context(), developer.Token, p.ID, " Acme <private> ")
	if err != nil || got.ClientLabel != "Acme <private>" || got.Name != p.Name || got.ID != p.ID {
		t.Fatalf("label: %+v %v", got, err)
	}
	projects, err := s.Projects(t.Context(), viewSession.Token, owner.WorkspaceID)
	if err != nil || len(projects) != 1 || projects[0].ClientLabel != got.ClientLabel {
		t.Fatalf("workspace list: %+v %v", projects, err)
	}
	renamed, err := s.RenameProject(t.Context(), session.Token, p.ID, "New name")
	if err != nil || renamed.ClientLabel != got.ClientLabel {
		t.Fatalf("rename preserved: %+v %v", renamed, err)
	}
	shared, err := s.SharedWebsites(t.Context(), clientSession.Token)
	if err != nil || len(shared) != 1 {
		t.Fatalf("shared: %+v %v", shared, err)
	}
	body, _ := json.Marshal(shared)
	if strings.Contains(string(body), "client_label") || strings.Contains(string(body), "Acme") {
		t.Fatalf("label exposed to client: %s", body)
	}
	got, err = s.SetProjectClientLabel(t.Context(), session.Token, p.ID, " ")
	if err != nil || got.ClientLabel != "" {
		t.Fatalf("clear: %+v %v", got, err)
	}
	if _, err = s.DeleteProject(t.Context(), session.Token, p.ID, "New name"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetProjectClientLabel(t.Context(), session.Token, p.ID, "Late"); !errors.Is(err, ErrProjectDeleting) {
		t.Fatalf("deleting: %v", err)
	}
}

func TestClientLabelMigrationAndHTTP(t *testing.T) {
	s, path := newStore(t)
	owner, session := verifiedAccount(t, s, "label-http@example.test")
	p, err := s.CreateProject(t.Context(), session.Token, owner.WorkspaceID, "Existing", "static")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("DROP TABLE project_client_labels; PRAGMA user_version=53"); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.GetProject(t.Context(), session.Token, p.ID)
	if err != nil || got.ID != p.ID || got.ClientLabel != "" {
		t.Fatalf("migration: %+v %v", got, err)
	}
	h, err := NewHTTP(s, HTTPOptions{Origin: "https://portal.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf := httpLogin(t, h, owner.Email)
	body := `{"project":"` + p.ID + `","label":"Acme"}`
	if w := portalRequest(h, "POST", "/api/projects/client-label", body, h.origin, "", cookie); w.Code != 403 {
		t.Fatalf("csrf: %d", w.Code)
	}
	if w := portalRequest(h, "POST", "/api/projects/client-label", body, h.origin, csrf, cookie); w.Code != 200 {
		t.Fatalf("set: %d %s", w.Code, w.Body.String())
	}
	if w := portalRequest(h, "GET", "/api/projects?workspace="+owner.WorkspaceID, "", h.origin, csrf, cookie); w.Code != 200 || !strings.Contains(w.Body.String(), `"client_label":"Acme"`) {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	got, err = s.GetProject(t.Context(), session.Token, p.ID)
	if err != nil || got.ClientLabel != "Acme" {
		t.Fatalf("persist: %+v %v", got, err)
	}
}
