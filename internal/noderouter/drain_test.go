//go:build integration

package noderouter

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDrainWaitsAndGuardsWhileReplacementServes(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/health" {
			return
		}
		close(entered)
		<-release
		w.Write([]byte("finished"))
	}))
	defer old.Close()
	defer unblock()
	var healthy atomic.Int32
	healthy.Store(200)
	replacement := backend(t, "replacement", &healthy)
	config := testConfig(old.URL, replacement.URL)
	r, err := Open(filepath.Join(t.TempDir(), "private", "route.db"), config)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	a, b := candidate(config, 1, "blue", "1"), candidate(config, 2, "green", "2")
	if err = r.Activate(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "http://"+config.ContentHost+"/", nil)
	done := make(chan struct{})
	go func() { defer close(done); r.ServeHTTP(httptest.NewRecorder(), request) }()
	<-entered
	if err = r.Activate(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
	err = r.WithDrainedBackend(ctx, a, func(context.Context) error { t.Error("stopped with old request outstanding"); return nil })
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	served := httptest.NewRecorder()
	r.ServeHTTP(served, request)
	if served.Code != 200 || served.Body.String() != "replacement" {
		t.Fatal(served.Code, served.Body.String())
	}
	unblock()
	<-done
	drainCtx, cancelDrain := context.WithCancel(t.Context())
	defer cancelDrain()
	called := false
	err = r.WithDrainedBackend(drainCtx, a, func(context.Context) error {
		called = true
		// Cancellation must not release the activation guard while stop is running.
		cancelDrain()
		served := httptest.NewRecorder()
		r.ServeHTTP(served, request)
		if served.Code != 200 || served.Body.String() != "replacement" {
			t.Fatal(served.Code, served.Body.String())
		}
		activationCtx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
		defer cancel()
		// Activation cannot take the guarded control connection or probe old backend.
		if err := r.Activate(activationCtx, candidate(config, 3, "blue", "3")); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("unguarded activation: %v", err)
		}
		return nil
	})
	if err != nil || !called {
		t.Fatal(err, called)
	}
	// A new operation can reuse the backend after the guard finishes.
	if err = r.Activate(t.Context(), candidate(config, 3, "blue", "3")); err != nil {
		t.Fatal(err)
	}
	if err = r.WithDrainedBackend(t.Context(), a, func(context.Context) error { t.Error("retired operation allowed stop of reused backend"); return nil }); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
}

func TestDrainWaitsForHealthProbe(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	pending := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-release }))
	defer pending.Close()
	defer unblock()
	var healthy atomic.Int32
	healthy.Store(200)
	aServer := backend(t, "active", &healthy)
	config := testConfig(aServer.URL, pending.URL)
	r, err := Open(filepath.Join(t.TempDir(), "private", "route.db"), config)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err = r.Activate(t.Context(), candidate(config, 1, "blue", "1")); err != nil {
		t.Fatal(err)
	}
	c := candidate(config, 2, "green", "2")
	done := make(chan error, 1)
	go func() { done <- r.Activate(t.Context(), c) }()
	<-entered
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
	defer cancel()
	if err = r.WithDrainedBackend(ctx, c, func(context.Context) error { t.Error("probe not drained"); return nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	unblock()
	if err = <-done; !errors.Is(err, ErrRetired) {
		t.Fatal(err)
	}
	called := false
	if err = r.WithDrainedBackend(t.Context(), c, func(context.Context) error { called = true; return nil }); err != nil || !called {
		t.Fatal(err, called)
	}
}
