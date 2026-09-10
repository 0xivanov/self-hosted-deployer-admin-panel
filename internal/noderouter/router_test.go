//go:build integration

package noderouter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testConfig(a, b string) Config {
	return Config{ProjectID: strings.Repeat("a", 64), RuntimeID: strings.Repeat("b", 64), ContentHost: "site.example.test", HealthPath: "/health", Backends: map[string]string{"blue": a, "green": b}}
}
func candidate(config Config, revision int64, backend, digest string) Candidate {
	return Candidate{ProjectID: config.ProjectID, RuntimeID: config.RuntimeID, DeploymentID: fmt.Sprintf("%064x", revision+100), OperationID: fmt.Sprintf("%064x", revision+200), Revision: revision, ArtifactSHA256: strings.Repeat(digest, 64), Backend: backend}
}
func backend(t *testing.T, name string, status *atomic.Int32) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(int(status.Load()))
			return
		}
		w.Header().Set("X-Seen-Forwarded-Proto", r.Header.Get("X-Forwarded-Proto"))
		w.Header().Set("X-Seen-Host", r.Host)
		io.WriteString(w, name)
	}))
	t.Cleanup(s.Close)
	return s
}
func fetch(t *testing.T, front *httptest.Server, host string) (int, string, http.Header) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, front.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	req.Header.Set("X-Forwarded-Proto", "spoofed")
	req.Header.Set("Forwarded", "for=spoofed")
	response, err := front.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, string(data), response.Header
}
func TestRoutingHealthFailureRestartAndRollback(t *testing.T) {
	t.Parallel()
	var statusA, statusB atomic.Int32
	statusA.Store(200)
	statusB.Store(503)
	a, b := backend(t, "release A", &statusA), backend(t, "release B", &statusB)
	config := testConfig(a.URL, b.URL)
	database := filepath.Join(t.TempDir(), "private", "route.db")
	router, err := Open(database, config)
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewTLSServer(router)
	if code, _, _ := fetch(t, front, config.ContentHost); code != 503 {
		t.Fatal(code)
	}
	first := candidate(config, 1, "blue", "1")
	if err = router.Activate(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	if code, text, headers := fetch(t, front, config.ContentHost); code != 200 || text != "release A" || headers.Get("X-Seen-Forwarded-Proto") != "https" || headers.Get("X-Seen-Host") != config.ContentHost {
		t.Fatal(code, text, headers)
	}
	if code, _, _ := fetch(t, front, "admin.example.test"); code != 421 {
		t.Fatal(code)
	}
	failed := candidate(config, 2, "green", "2")
	if err = router.Activate(t.Context(), failed); !errors.Is(err, ErrUnhealthy) {
		t.Fatal(err)
	}
	if code, text, _ := fetch(t, front, config.ContentHost); code != 200 || text != "release A" {
		t.Fatal("failed candidate changed traffic", code, text)
	}
	statusB.Store(200)
	if err = router.Activate(t.Context(), failed); !errors.Is(err, ErrUnhealthy) {
		t.Fatal("failed identity retried", err)
	}
	next := candidate(config, 3, "green", "2")
	if err = router.Activate(t.Context(), next); err != nil {
		t.Fatal(err)
	}
	if code, text, _ := fetch(t, front, config.ContentHost); code != 200 || text != "release B" {
		t.Fatal(code, text)
	}
	front.Close()
	if err = router.Close(); err != nil {
		t.Fatal(err)
	}
	router, err = Open(database, config)
	if err != nil {
		t.Fatal(err)
	}
	defer router.Close()
	front = httptest.NewTLSServer(router)
	defer front.Close()
	if code, text, _ := fetch(t, front, config.ContentHost); code != 200 || text != "release B" {
		t.Fatal("restart lost route", code, text)
	}
	if err = router.Activate(t.Context(), first); !errors.Is(err, ErrStale) {
		t.Fatal(err)
	}
	rollback := candidate(config, 4, "blue", "1")
	if err = router.Activate(t.Context(), rollback); err != nil {
		t.Fatal(err)
	}
	if code, text, _ := fetch(t, front, config.ContentHost); code != 200 || text != "release A" {
		t.Fatal("rollback failed", code, text)
	}
	conflict := rollback
	conflict.ArtifactSHA256 = strings.Repeat("3", 64)
	if err = router.Activate(t.Context(), conflict); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	changedSlot := candidate(config, 5, "blue", "3")
	if err = router.Activate(t.Context(), changedSlot); !errors.Is(err, ErrConflict) {
		t.Fatal("active listener reused for other bytes", err)
	}
	altered := config
	altered.ContentHost = "other.example.test"
	if reopened, err := Open(database, altered); !errors.Is(err, ErrConflict) || reopened != nil {
		if reopened != nil {
			reopened.Close()
		}
		t.Fatal("assignment changed on reopen", err)
	}
}
func TestLateHealthyCandidateCannotReplaceNewerRoute(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}), make(chan struct{})
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
			}
			return
		}
		io.WriteString(w, "old")
	}))
	defer a.Close()
	var status atomic.Int32
	status.Store(200)
	b := backend(t, "new", &status)
	config := testConfig(a.URL, b.URL)
	router, err := Open(filepath.Join(t.TempDir(), "private", "route.db"), config)
	if err != nil {
		t.Fatal(err)
	}
	defer router.Close()
	if err = router.Activate(t.Context(), candidate(config, 1, "green", "2")); err != nil {
		t.Fatal(err)
	}
	front := httptest.NewTLSServer(router)
	defer front.Close()
	result := make(chan error, 1)
	go func() { result <- router.Activate(t.Context(), candidate(config, 2, "blue", "1")) }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("health probe did not start")
	}
	if code, text, _ := fetch(t, front, config.ContentHost); code != 200 || text != "new" {
		close(release)
		t.Fatal("pending probe interrupted the live route", code, text)
	}
	if err = router.Activate(t.Context(), candidate(config, 3, "green", "2")); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	if err = <-result; !errors.Is(err, ErrStale) {
		t.Fatal(err)
	}
	state, err := router.Snapshot(t.Context())
	if err != nil || state.Active == nil || state.Active.Revision != 3 {
		t.Fatal(state, err)
	}
}
func TestCancelledPendingProbeCanRecoverAndRedirectsFail(t *testing.T) {
	t.Parallel()
	var redirectCalls atomic.Int32
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirectCalls.Add(1); w.WriteHeader(200) }))
	defer b.Close()
	var mode atomic.Int32
	entered := make(chan struct{}, 1)
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch mode.Load() {
		case 0:
			entered <- struct{}{}
			<-r.Context().Done()
		case 1:
			w.WriteHeader(200)
		case 2:
			http.Redirect(w, r, b.URL, http.StatusFound)
		}
	}))
	defer a.Close()
	config := testConfig(a.URL, b.URL)
	database := filepath.Join(t.TempDir(), "private", "route.db")
	router, err := Open(database, config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	first := candidate(config, 1, "blue", "1")
	go func() { result <- router.Activate(ctx, first) }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("probe missing")
	}
	cancel()
	if err = <-result; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	state, err := router.Snapshot(t.Context())
	if err != nil || state.Active != nil || state.Status != "pending" {
		t.Fatal(state, err)
	}
	if err = router.Close(); err != nil {
		t.Fatal(err)
	}
	router, err = Open(database, config)
	if err != nil {
		t.Fatal(err)
	}
	defer router.Close()
	mode.Store(1)
	if err = router.Activate(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	mode.Store(2)
	second := candidate(config, 2, "blue", "1")
	if err = router.Activate(t.Context(), second); !errors.Is(err, ErrUnhealthy) {
		t.Fatal(err)
	}
	if redirectCalls.Load() != 0 {
		t.Fatal("health redirect followed")
	}
	state, err = router.Snapshot(t.Context())
	if err != nil || state.Active == nil || state.Active.Revision != 1 {
		t.Fatal(state, err)
	}
}

func TestRouterRejectsUnsafeAssignments(t *testing.T) {
	t.Parallel()
	for _, target := range []string{"http://example.test:3000", "http://169.254.169.254:80", "http://127.0.0.1:080", "http://user:secret@127.0.0.1:3000", "http://127.0.0.1:3000/path", "http://127.0.0.1:3000?x=1"} {
		t.Run(target, func(t *testing.T) {
			config := testConfig(target, "http://127.0.0.1:4000")
			router, err := Open(filepath.Join(t.TempDir(), "private", "route.db"), config)
			if router != nil {
				router.Close()
			}
			if !errors.Is(err, ErrInvalid) {
				t.Fatal(err)
			}
		})
	}
}
