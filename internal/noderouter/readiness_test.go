//go:build integration

package noderouter

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestWaitHealthyKeepsCurrentRouteWhileBackendStarts(t *testing.T) {
	t.Parallel()
	var healthy atomic.Int32
	healthy.Store(200)
	old := backend(t, "old", &healthy)
	var probes atomic.Int32
	next := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if probes.Add(1) == 1 {
			w.WriteHeader(503)
		}
	}))
	defer next.Close()
	config := testConfig(old.URL, next.URL)
	router, err := Open(filepath.Join(t.TempDir(), "private", "router.db"), config)
	if err != nil {
		t.Fatal(err)
	}
	defer router.Close()
	first, second := candidate(config, 1, "blue", "1"), candidate(config, 2, "green", "2")
	if err = router.Activate(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	if err = router.WaitHealthy(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	state, err := router.Snapshot(t.Context())
	if err != nil || state.Active == nil || *state.Active != first || probes.Load() != 2 {
		t.Fatal(state, err, probes.Load())
	}
	if err = router.Activate(t.Context(), second); err != nil {
		t.Fatal(err)
	}
}
