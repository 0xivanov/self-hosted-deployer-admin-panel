//go:build integration

package staticsite

import (
	"archive/zip"
	"bytes"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func archive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for name, value := range files {
		f, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Write([]byte(value)); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func TestReleasePublishRollbackRestart(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "site")
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	one, err := s.Publish(t.Context(), archive(t, map[string]string{"index.html": "first"}))
	if err != nil {
		t.Fatal(err)
	}
	two, err := s.Publish(t.Context(), archive(t, map[string]string{"index.html": "second"}))
	if err != nil || one == two {
		t.Fatal(two, err)
	}
	if _, err = s.Publish(t.Context(), []byte("broken")); err == nil || s.Active() != two {
		t.Fatal("failed publish changed active release", err)
	}
	if err = s.Rollback(t.Context(), one); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(root)
	if err != nil || reopened.Active() != one {
		t.Fatal("rollback did not survive restart", err)
	}
	h, err := reopened.Handler("site.example.test", false)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "https://site.example.test/", nil))
	if w.Code != 200 || w.Body.String() != "first" {
		t.Fatal(w.Code, w.Body.String())
	}
	if err = os.WriteFile(filepath.Join(root, two+".zip"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = reopened.Rollback(t.Context(), two); err == nil || reopened.Active() != one {
		t.Fatal("corrupt rollback accepted", err)
	}
}
func TestContentOriginAndPaths(t *testing.T) {
	t.Parallel()
	s, err := Open(filepath.Join(t.TempDir(), "site"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Publish(t.Context(), archive(t, map[string]string{"index.html": "<h1>Home</h1>", "app.js": "console.log('test')", "docs/index.html": "Docs"}))
	if err != nil {
		t.Fatal(err)
	}
	h, err := s.Handler("site.example.test", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, method, url, accept string
		code                      int
		body                      string
	}{
		{"home", "GET", "https://site.example.test/", "", 200, "<h1>Home</h1>"},
		{"directory", "GET", "https://site.example.test/docs/", "", 200, "Docs"},
		{"spa", "GET", "https://site.example.test/route", "text/html", 200, "<h1>Home</h1>"},
		{"asset missing", "GET", "https://site.example.test/missing.js", "text/html", 404, ""},
		{"no implicit spa", "GET", "https://site.example.test/route", "", 404, ""},
		{"foreign host", "GET", "https://portal.example.test/", "", 421, ""},
		{"traversal", "GET", "https://site.example.test/../index.html", "", 404, ""},
		{"encoded traversal", "GET", "https://site.example.test/%2e%2e/index.html", "", 404, ""},
		{"mutation", "POST", "https://site.example.test/", "", 405, ""},
		{"head", "HEAD", "https://site.example.test/", "", 200, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.url, nil)
			r.Header.Set("Accept", tc.accept)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.code || tc.body != "" && w.Body.String() != tc.body {
				t.Fatal(w.Code, w.Body.String())
			}
			if w.Header().Get("Set-Cookie") != "" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatal(w.Header())
			}
			if tc.method == "HEAD" && w.Body.Len() != 0 {
				t.Fatal("HEAD body")
			}
		})
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "https://site.example.test/", nil))
	etag := w.Header().Get("ETag")
	r := httptest.NewRequest("GET", "https://site.example.test/", nil)
	r.Header.Set("If-None-Match", etag)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if etag == "" || w.Code != 304 {
		t.Fatal(etag, w.Code)
	}
	r = httptest.NewRequest("GET", "https://site.example.test/", nil)
	r.Header.Set("Range", "bytes=0-3")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 206 || w.Body.String() != "<h1>" {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestConcurrentReadersSeeCompleteRelease(t *testing.T) {
	t.Parallel()
	s, err := Open(filepath.Join(t.TempDir(), "site"))
	if err != nil {
		t.Fatal(err)
	}
	first := strings.Repeat("a", 10000)
	second := strings.Repeat("b", 10000)
	one, err := s.Publish(t.Context(), archive(t, map[string]string{"index.html": first}))
	if err != nil {
		t.Fatal(err)
	}
	two, err := s.Publish(t.Context(), archive(t, map[string]string{"index.html": second}))
	if err != nil {
		t.Fatal(err)
	}
	h, err := s.Handler("site.example.test", false)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 15 {
				w := httptest.NewRecorder()
				h.ServeHTTP(w, httptest.NewRequest("GET", "https://site.example.test/", nil))
				if w.Code != 200 || (w.Body.String() != first && w.Body.String() != second) {
					t.Error("partial release response", w.Code)
				}
			}
		})
	}
	for range 5 {
		if err = s.Rollback(t.Context(), one); err != nil {
			t.Fatal(err)
		}
		if err = s.Rollback(t.Context(), two); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
}
func TestPrivatePathsAndIntegrity(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "site")
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Rollback(t.Context(), "../../secret"); err == nil {
		t.Fatal("unsafe release ID")
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err = os.WriteFile(outside, []byte(strings.Repeat("a", 64)), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(outside, filepath.Join(root, "active")); err != nil {
		t.Fatal(err)
	}
	if _, err = Open(root); err == nil {
		t.Fatal("symlink active pointer accepted")
	}
}

func TestRetentionAndActiveReleaseProtection(t *testing.T) {
	t.Parallel()
	s, err := Open(filepath.Join(t.TempDir(), "site"))
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for i := range MaxReleases {
		id, err := s.Publish(t.Context(), archive(t, map[string]string{"index.html": strings.Repeat("x", i+1)}))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if _, err = s.Publish(t.Context(), archive(t, map[string]string{"index.html": "over quota"})); err == nil {
		t.Fatal("retained release quota bypassed")
	}
	if s.Active() != ids[len(ids)-1] {
		t.Fatal("quota failure changed active release")
	}
	if err = s.Prune(s.Active()); err == nil {
		t.Fatal("active release deleted")
	}
	if err = s.Prune(ids[0]); err != nil {
		t.Fatal(err)
	}
	if err = s.Rollback(t.Context(), ids[0]); err == nil {
		t.Fatal("deleted release restored")
	}
	if _, err = s.Publish(t.Context(), archive(t, map[string]string{"index.html": "after prune"})); err != nil {
		t.Fatal(err)
	}
}
