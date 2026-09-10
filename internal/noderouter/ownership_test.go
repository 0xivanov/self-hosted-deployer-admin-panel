//go:build integration

package noderouter

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func TestServingOwnershipSurvivesCloseUntilRequestFinishes(t *testing.T) {
	t.Parallel()
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/health" {
			return
		}
		close(entered)
		<-release
		w.Write([]byte("old response completed"))
	}))
	defer old.Close()
	// Unblock before server shutdown even if an assertion fails.
	defer unblock()
	var healthy atomic.Int32
	healthy.Store(200)
	next := backend(t, "replacement", &healthy)
	config := testConfig(old.URL, next.URL)
	database := filepath.Join(t.TempDir(), "private", "route.db")
	first, err := Open(database, config)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(database, config)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	a, b := candidate(config, 1, "blue", "1"), candidate(config, 2, "green", "2")
	if err = first.Activate(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	if err = second.Activate(t.Context(), b); !errors.Is(err, ErrServingOwned) {
		t.Fatalf("second owner: %v", err)
	}
	req := httptest.NewRequest("GET", "http://"+config.ContentHost+"/", nil)
	denied := httptest.NewRecorder()
	second.ServeHTTP(denied, req)
	if denied.Code != 503 {
		t.Fatal(denied.Code)
	}
	response := httptest.NewRecorder()
	go func() { defer close(done); first.ServeHTTP(response, req) }()
	<-entered
	if err = first.Activate(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	// A non-serving handle can still permanently fence the old operation.
	if err = second.FenceRetirement(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	if err = second.Activate(t.Context(), b); !errors.Is(err, ErrServingOwned) {
		t.Fatalf("transferred with outstanding request: %v", err)
	}
	if err = first.Activate(t.Context(), b); !errors.Is(err, os.ErrClosed) {
		t.Fatal(err)
	}
	unblock()
	<-done
	if response.Code != 200 || response.Body.String() != "old response completed" {
		t.Fatal(response.Code, response.Body.String())
	}
	if err = second.Activate(t.Context(), b); err != nil {
		t.Fatalf("handover: %v", err)
	}
	served := httptest.NewRecorder()
	second.ServeHTTP(served, req)
	if served.Code != 200 || served.Body.String() != "replacement" {
		t.Fatal(served.Code, served.Body.String())
	}
}

func TestServingOwnerRejectsUnsafeLock(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"symlink", "hardlink", "public"} {
		t.Run(kind, func(t *testing.T) {
			var healthy atomic.Int32
			healthy.Store(200)
			a, b := backend(t, "a", &healthy), backend(t, "b", &healthy)
			config := testConfig(a.URL, b.URL)
			database := filepath.Join(t.TempDir(), "private", "route.db")
			r, err := Open(database, config)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			target := filepath.Join(filepath.Dir(database), "target")
			if err = os.WriteFile(target, []byte("unchanged"), 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "symlink":
				err = os.Symlink(target, database+".serve.lock")
			case "hardlink":
				err = os.Link(target, database+".serve.lock")
			case "public":
				err = os.WriteFile(database+".serve.lock", nil, 0644)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = r.Activate(t.Context(), candidate(config, 1, "blue", "1")); err == nil {
				t.Fatal("unsafe owner lock accepted")
			}
			data, err := os.ReadFile(target)
			if err != nil || string(data) != "unchanged" {
				t.Fatal(string(data), err)
			}
		})
	}
}
