package portal

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRenameProjectPermissionsAndStableIdentity(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	owner, session := verifiedAccount(t, s, "rename-owner@example.test")
	_, outsider := verifiedAccount(t, s, "rename-other@example.test")
	dev, developer := verifiedAccount(t, s, "rename-dev@example.test")
	viewer, viewSession := verifiedAccount(t, s, "rename-viewer@example.test")
	for _, m := range []struct{ user, role string }{{dev.ID, "developer"}, {viewer.ID, "viewer"}} {
		if _, err := s.db.Exec("INSERT INTO memberships VALUES(?,?,?)", m.user, owner.WorkspaceID, m.role); err != nil {
			t.Fatal(err)
		}
	}
	p, err := s.CreateProject(ctx, session.Token, owner.WorkspaceID, "Original", "static")
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{outsider.Token, viewSession.Token} {
		if _, err = s.RenameProject(ctx, token, p.ID, "Denied"); !errors.Is(err, ErrDenied) {
			t.Fatalf("unauthorized rename: %v", err)
		}
	}
	for _, name := range []string{" ", strings.Repeat("x", 101)} {
		if _, err = s.RenameProject(ctx, session.Token, p.ID, name); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid name: %v", err)
		}
	}
	renamed, err := s.RenameProject(ctx, developer.Token, p.ID, " New name ")
	if err != nil || renamed.ID != p.ID || renamed.WorkspaceID != p.WorkspaceID || renamed.Kind != p.Kind || renamed.Name != "New name" {
		t.Fatalf("rename: %+v %v", renamed, err)
	}
	if _, err = s.RenameProject(ctx, session.Token, p.ID, "New name"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateProject(ctx, session.Token, owner.WorkspaceID, "Taken", "static"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RenameProject(ctx, session.Token, p.ID, "Taken"); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate: %v", err)
	}
	if _, err = s.DeleteProject(ctx, session.Token, p.ID, "Original"); !errors.Is(err, ErrProjectNameMismatch) {
		t.Fatalf("old delete confirmation: %v", err)
	}
	if _, err = s.DeleteProject(ctx, session.Token, p.ID, "New name"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RenameProject(ctx, session.Token, p.ID, "Too late"); !errors.Is(err, ErrProjectDeleting) {
		t.Fatalf("deleting rename: %v", err)
	}
}

func TestHTTPRenameProjectRequiresCSRF(t *testing.T) {
	s, _ := newStore(t)
	owner, session := verifiedAccount(t, s, "rename-http@example.test")
	p, err := s.CreateProject(context.Background(), session.Token, owner.WorkspaceID, "Before", "static")
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewHTTP(s, HTTPOptions{Origin: "https://portal.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf := httpLogin(t, h, owner.Email)
	body := `{"project":"` + p.ID + `","name":"After"}`
	if w := portalRequest(h, "POST", "/api/projects/rename", body, h.origin, "", cookie); w.Code != 403 {
		t.Fatalf("missing csrf: %d", w.Code)
	}
	if w := portalRequest(h, "POST", "/api/projects/rename", body, h.origin, csrf, cookie); w.Code != 200 {
		t.Fatalf("rename: %d %s", w.Code, w.Body.String())
	}
	got, err := s.GetProject(context.Background(), session.Token, p.ID)
	if err != nil || got.Name != "After" {
		t.Fatalf("persisted rename: %+v %v", got, err)
	}
}
