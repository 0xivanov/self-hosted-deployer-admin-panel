//go:build integration

package portal

import (
	"archive/zip"
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func testArchive(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	f, err := z.Create("index.html")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write([]byte("<h1>Test</h1>")); err != nil {
		t.Fatal(err)
	}
	if err = z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestUploadsPersistenceAndIsolation(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, path := newStore(t)
	a, session := verifiedAccount(t, s, "uploads@example.test")
	_, other := verifiedAccount(t, s, "other-upload@example.test")
	p, err := s.CreateProject(ctx, session.Token, a.WorkspaceID, "site", "static")
	if err != nil {
		t.Fatal(err)
	}
	data := testArchive(t)
	u, err := s.SaveUpload(ctx, session.Token, p.ID, data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveUpload(ctx, other.Token, p.ID, data); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	if _, err = s.Uploads(ctx, other.Token, p.ID); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	if err = s.DeleteUpload(ctx, other.Token, p.ID, u.ID); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	if _, err = s.SaveUpload(ctx, session.Token, p.ID, []byte("bad")); !errors.Is(err, ErrArchive) {
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
	list, err := s.Uploads(ctx, session.Token, p.ID)
	if err != nil || len(list) != 1 || list[0] != u {
		t.Fatal(list, err)
	}
	var stored []byte
	if err = s.db.QueryRow("SELECT archive FROM uploads WHERE id=?", u.ID).Scan(&stored); err != nil || !bytes.Equal(stored, data) {
		t.Fatal("archive persistence", err)
	}
	if err = s.DeleteUpload(ctx, session.Token, p.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	list, err = s.Uploads(ctx, session.Token, p.ID)
	if err != nil || len(list) != 0 {
		t.Fatal(list, err)
	}
}

func TestUploadQuotaConcurrentAndRoleRevocation(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, _ := newStore(t)
	a, session := verifiedAccount(t, s, "quota@example.test")
	p, err := s.CreateProject(ctx, session.Token, a.WorkspaceID, "site", "static")
	if err != nil {
		t.Fatal(err)
	}
	data := testArchive(t)
	for range WorkspaceUploadCount - 1 {
		if _, err = s.SaveUpload(ctx, session.Token, p.ID, data); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Go(func() { _, err := s.SaveUpload(ctx, session.Token, p.ID, data); results <- err })
	}
	wg.Wait()
	close(results)
	success, quota := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrQuota) {
			quota++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || quota != 1 {
		t.Fatal(success, quota)
	}
	list, err := s.Uploads(ctx, session.Token, p.ID)
	if err != nil || len(list) != WorkspaceUploadCount {
		t.Fatal(len(list), err)
	}
	if err = s.DeleteUpload(ctx, session.Token, p.ID, list[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveUpload(ctx, session.Token, p.ID, data); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("UPDATE memberships SET role='viewer' WHERE user_id=?", a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Uploads(ctx, session.Token, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveUpload(ctx, session.Token, p.ID, data); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	if err = s.DeleteUpload(ctx, session.Token, p.ID, list[1].ID); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
}

func TestUploadHTTPBoundary(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, _ := newStore(t)
	a, session := verifiedAccount(t, s, "http-upload@example.test")
	b, bs := verifiedAccount(t, s, "foreign-upload@example.test")
	p, err := s.CreateProject(ctx, session.Token, a.WorkspaceID, "site", "static")
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := s.CreateProject(ctx, bs.Token, b.WorkspaceID, "site", "static")
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewHTTP(s, HTTPOptions{Origin: "https://portal.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf := httpLogin(t, h, a.Email)
	for _, tc := range []struct {
		name, project, csrf, typ string
		data                     []byte
		code                     int
	}{
		{"valid", p.ID, csrf, "application/zip", testArchive(t), 200},
		{"foreign", foreign.ID, csrf, "application/zip", testArchive(t), 403},
		{"csrf", p.ID, "", "application/zip", testArchive(t), 403},
		{"type", p.ID, csrf, "text/plain", testArchive(t), 415},
		{"invalid", p.ID, csrf, "application/zip", []byte("not zip"), 400},
		{"oversized", p.ID, csrf, "application/zip", make([]byte, 10<<20+1), 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, h.origin+"/api/uploads?project="+tc.project, bytes.NewReader(tc.data))
			r.AddCookie(cookie)
			r.Header.Set("Origin", h.origin)
			r.Header.Set("X-CSRF-Token", tc.csrf)
			r.Header.Set("Content-Type", tc.typ)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.code {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
	list, err := s.Uploads(ctx, session.Token, p.ID)
	if err != nil || len(list) != 1 {
		t.Fatal(list, err)
	}
}

func TestUploadByteQuotaAcrossProjects(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, _ := newStore(t)
	a, session := verifiedAccount(t, s, "bytes@example.test")
	first, err := s.CreateProject(ctx, session.Token, a.WorkspaceID, "first", "static")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateProject(ctx, session.Token, a.WorkspaceID, "second", "static")
	if err != nil {
		t.Fatal(err)
	}
	// Seed retained archive usage without constructing ten large ZIP fixtures.
	for range 10 {
		if _, err = s.db.Exec("INSERT INTO uploads VALUES(?,?,?,1,1,zeroblob(?),1)", randomToken(), first.ID, "fixture", 10<<20); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.SaveUpload(ctx, session.Token, second.ID, testArchive(t)); !errors.Is(err, ErrQuota) {
		t.Fatal(err)
	}
	list, err := s.Uploads(ctx, session.Token, second.ID)
	if err != nil || len(list) != 0 {
		t.Fatal(list, err)
	}
}

func TestUploadMigrationPreservesExistingAccount(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, path := newStore(t)
	a, session := verifiedAccount(t, s, "migration@example.test")
	p, err := s.CreateProject(ctx, session.Token, a.WorkspaceID, "legacy", "static")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("DROP TABLE billing_customers; DROP TABLE billing_events; DROP TABLE publications; DROP TABLE publication_jobs; DROP TABLE uploads; PRAGMA user_version=3"); err != nil {
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
	got, err := s.GetProject(ctx, session.Token, p.ID)
	if err != nil || got != p {
		t.Fatal(got, err)
	}
	if _, err = s.SaveUpload(ctx, session.Token, p.ID, testArchive(t)); err != nil {
		t.Fatal(err)
	}
}
