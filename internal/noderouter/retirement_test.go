//go:build integration

package noderouter

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestRetirementFenceSurvivesRestartAndPreservesServingRoute(t *testing.T) {
	t.Parallel()
	var status atomic.Int32
	status.Store(200)
	blue, green := backend(t, "blue", &status), backend(t, "green", &status)
	config := testConfig(blue.URL, green.URL)
	path := filepath.Join(t.TempDir(), "private", "router.db")
	r, err := Open(path, config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { r.Close() }()
	first := candidate(config, 1, "blue", "1")
	if err = r.Activate(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	if err = r.FenceRetirement(t.Context(), first); !errors.Is(err, ErrConflict) {
		t.Fatal("active route retired", err)
	}
	sameBackend := candidate(config, 2, "blue", "1")
	if err = r.FenceRetirement(t.Context(), sameBackend); !errors.Is(err, ErrConflict) {
		t.Fatal("active backend retired", err)
	}
	second := candidate(config, 2, "green", "2")
	if err = r.Activate(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	if err = r.FenceRetirement(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
	r, err = Open(path, config)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.FenceRetirement(t.Context(), first); err != nil {
		t.Fatal("idempotent retirement", err)
	}
	changed := first
	changed.Revision = 9
	if err = r.FenceRetirement(t.Context(), changed); !errors.Is(err, ErrConflict) {
		t.Fatal("retirement identity changed", err)
	}
	if err = r.Activate(t.Context(), changed); !errors.Is(err, ErrRetired) {
		t.Fatal("retired operation resurrected", err)
	}
	front := httptest.NewServer(r)
	defer front.Close()
	if code, body, _ := fetch(t, front, config.ContentHost); code != 200 || body != "green" {
		t.Fatal("retirement broke current route", code, body)
	}
	// Reusing the old backend requires a new operation and a newer revision.
	replacement := candidate(config, 3, "blue", "1")
	if err = r.Activate(t.Context(), replacement); err != nil {
		t.Fatal(err)
	}
	if code, body, _ := fetch(t, front, config.ContentHost); code != 200 || body != "blue" {
		t.Fatal(code, body)
	}
}
func TestRetirementFenceStopsLateHealthProbe(t *testing.T) {
	t.Parallel()
	var status atomic.Int32
	status.Store(200)
	blue := backend(t, "blue", &status)
	entered, release := make(chan struct{}), make(chan struct{})
	green := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { close(entered); <-release; w.WriteHeader(200) }))
	defer green.Close()
	config := testConfig(blue.URL, green.URL)
	path := filepath.Join(t.TempDir(), "private", "router.db")
	r, err := Open(path, config)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	other, err := Open(path, config)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	first := candidate(config, 1, "blue", "1")
	if err = r.Activate(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	second := candidate(config, 2, "green", "2")
	result := make(chan error, 1)
	go func() { result <- r.Activate(t.Context(), second) }()
	<-entered
	if err = other.FenceRetirement(t.Context(), second); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	if err = <-result; !errors.Is(err, ErrRetired) {
		t.Fatal("late probe activated retired operation", err)
	}
	state, err := r.Snapshot(t.Context())
	if err != nil || state.Status != "failed" || state.Active == nil || *state.Active != first {
		t.Fatal(state, err)
	}
	if err = r.Activate(t.Context(), second); !errors.Is(err, ErrRetired) {
		t.Fatal(err)
	}
}
func TestRetirementBeforeActivationAndVersionOneMigration(t *testing.T) {
	t.Parallel()
	var status atomic.Int32
	status.Store(200)
	blue, green := backend(t, "blue", &status), backend(t, "green", &status)
	config := testConfig(blue.URL, green.URL)
	path := filepath.Join(t.TempDir(), "private", "router.db")
	r, err := Open(path, config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { r.Close() }()
	first := candidate(config, 1, "blue", "1")
	if err = r.Activate(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	if _, err = r.db.Exec("DROP TABLE retired_operations; PRAGMA user_version=1"); err != nil {
		t.Fatal(err)
	}
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
	r, err = Open(path, config)
	if err != nil {
		t.Fatal(err)
	}
	state, err := r.Snapshot(t.Context())
	if err != nil || state.Active == nil || *state.Active != first {
		t.Fatal("migration lost active route", state, err)
	}
	late := candidate(config, 3, "green", "3")
	if err = r.FenceRetirement(t.Context(), late); err != nil {
		t.Fatal(err)
	}
	if err = r.Activate(t.Context(), late); !errors.Is(err, ErrRetired) {
		t.Fatal("late activation allowed", err)
	}
	foreign := late
	foreign.ProjectID = candidate(config, 7, "green", "7").OperationID
	if err = r.FenceRetirement(t.Context(), foreign); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}
