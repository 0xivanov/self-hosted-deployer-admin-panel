//go:build integration

package portal

import (
	"bytes"
	"errors"
	"testing"
)

func TestDownloadUploadIsolationAndIntegrity(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	owner, session := verifiedAccount(t, s, "export-owner@example.test")
	_, other := verifiedAccount(t, s, "export-other@example.test")
	viewer, viewSession := verifiedAccount(t, s, "export-view@example.test")
	dev, devSession := verifiedAccount(t, s, "export-dev@example.test")
	for _, m := range []struct{ user, role string }{{viewer.ID, "viewer"}, {dev.ID, "developer"}} {
		if _, err := s.db.Exec("INSERT INTO memberships VALUES(?,?,?)", m.user, owner.WorkspaceID, m.role); err != nil {
			t.Fatal(err)
		}
	}
	p, err := s.CreateProject(ctx, session.Token, owner.WorkspaceID, "Export", "static")
	if err != nil {
		t.Fatal(err)
	}
	q, err := s.CreateProject(ctx, session.Token, owner.WorkspaceID, "Other project", "static")
	if err != nil {
		t.Fatal(err)
	}
	data := testArchive(t)
	u, err := s.SaveUpload(ctx, session.Token, p.ID, data)
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{session.Token, devSession.Token} {
		got, err := s.DownloadUpload(ctx, token, p.ID, u.ID)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("export differs: %v", err)
		}
	}
	for _, token := range []string{other.Token, viewSession.Token} {
		if _, err := s.DownloadUpload(ctx, token, p.ID, u.ID); !errors.Is(err, ErrDenied) {
			t.Fatalf("unauthorized export: %v", err)
		}
	}
	if _, err := s.DownloadUpload(ctx, session.Token, q.ID, u.ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("wrong project: %v", err)
	}
	h, err := NewHTTP(s, HTTPOptions{Origin: "https://portal.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf := httpLogin(t, h, owner.Email)
	body := `{"project":"` + p.ID + `","id":"` + u.ID + `"}`
	if w := portalRequest(h, "POST", "/api/uploads/download", body, h.origin, "", cookie); w.Code != 403 {
		t.Fatalf("CSRF: %d", w.Code)
	}
	w := portalRequest(h, "POST", "/api/uploads/download", body, h.origin, csrf, cookie)
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), data) || w.Header().Get("Content-Type") != "application/zip" || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Disposition") == "" {
		t.Fatalf("download headers/body: %d %v", w.Code, w.Header())
	}
	if _, err = s.db.Exec("UPDATE uploads SET sha256='corrupt' WHERE id=?", u.ID); err != nil {
		t.Fatal(err)
	}
	if data, err := s.DownloadUpload(ctx, session.Token, p.ID, u.ID); err == nil || data != nil {
		t.Fatal("corrupt archive exported")
	}
	if _, err = s.DeleteProject(ctx, session.Token, p.ID, p.Name); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DownloadUpload(ctx, session.Token, p.ID, u.ID); !errors.Is(err, ErrProjectDeleting) {
		t.Fatalf("deleting export: %v", err)
	}
}
