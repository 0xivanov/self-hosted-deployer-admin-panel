//go:build linux && integration

package nodelaunch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/noderouter"
)

func TestRoutedRetirementKeepsOccupiedListenerAndRecovers(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires dedicated Linux root VM")
	}
	if err := hostNamespaces(); err != nil {
		t.Fatal(err)
	}
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("old")) }))
	defer old.Close()
	replacement := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("replacement")) }))
	defer replacement.Close()
	port, _ := strconv.Atoi(strings.TrimPrefix(old.URL, "http://127.0.0.1:"))
	otherPort, _ := strconv.Atoi(strings.TrimPrefix(replacement.URL, "http://127.0.0.1:"))
	entropy := make([]byte, 32)
	if _, err := rand.Read(entropy); err != nil {
		t.Fatal(err)
	}
	a := assignment()
	a.OperationID = hex.EncodeToString(entropy)
	a.ReleaseDirectory = "release-" + a.OperationID
	a.Architecture = runtime.GOARCH
	a.UID = 0
	a.Port = 0
	config := PoolConfig{ProjectID: a.ProjectID, RuntimeID: a.RuntimeID, ToolchainSHA256: a.ToolchainSHA256, Architecture: a.Architecture, Slots: []Slot{{60003, port}, {60004, otherPort}}}
	directory := t.TempDir()
	pool, err := OpenPool(filepath.Join(directory, "pool", "state.db"), config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	reserved, err := pool.Reserve(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	a = reserved.Assignment
	gatePath := filepath.Join(directory, "gate")
	if err = os.Mkdir(gatePath, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(gatePath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	gate, err := OpenControlGate(root)
	if err != nil {
		t.Fatal(err)
	}
	router, err := noderouter.Open(filepath.Join(directory, "routing", "state.db"), noderouter.Config{ProjectID: a.ProjectID, RuntimeID: a.RuntimeID, ContentHost: "site.example.test", HealthPath: "/health", Backends: map[string]string{"old": old.URL, "replacement": replacement.URL}})
	if err != nil {
		t.Fatal(err)
	}
	defer router.Close()
	c := noderouter.Candidate{ProjectID: a.ProjectID, RuntimeID: a.RuntimeID, OperationID: a.OperationID, DeploymentID: strings.Repeat("d", 64), ArtifactSHA256: a.ArtifactSHA256, Revision: 1, Backend: "old"}
	if err = router.Activate(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	next := c
	next.OperationID = strings.Repeat("e", 64)
	next.DeploymentID = strings.Repeat("f", 64)
	next.Revision = 2
	next.Backend = "replacement"
	if err = router.Activate(t.Context(), next); err != nil {
		t.Fatal(err)
	}
	unit, _ := Render(a)
	// This test never installs a real service for this random operation. Remove
	// its mask after all assertions; production retirement records are retained.
	defer func() {
		cmd := exec.Command("/usr/bin/systemctl", "unmask", "--", unit.Name)
		if err := cmd.Run(); err != nil {
			t.Error(err)
		}
	}()
	wrong := c
	wrong.ArtifactSHA256 = strings.Repeat("0", 64)
	if _, err = RetireRoutedNode(t.Context(), pool, gate, router, wrong); !errors.Is(err, ErrAssignment) {
		t.Fatal(err)
	}
	current, err := pool.Lookup(t.Context(), a.OperationID)
	if err != nil || current.State != "reserved" {
		t.Fatal(current, err)
	}
	if _, err = RetireRoutedNode(t.Context(), pool, gate, router, c); !errors.Is(err, ErrRetirement) {
		t.Fatalf("listener should prevent release: %v", err)
	}
	current, err = pool.Lookup(t.Context(), a.OperationID)
	if err != nil || current.State != "retiring" || current.Retirement != nil {
		t.Fatal(current, err)
	}
	old.Close()
	result, err := RetireRoutedNode(t.Context(), pool, gate, router, c)
	if err != nil || result.State != "retired" || result.Retirement == nil {
		t.Fatal(result, err)
	}
	// Reopen the durable pool and reuse its freed slot with a new operation.
	if err = pool.Close(); err != nil {
		t.Fatal(err)
	}
	pool, err = OpenPool(filepath.Join(directory, "pool", "state.db"), config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	fresh := a
	fresh.UID = 0
	fresh.Port = 0
	fresh.OperationID = strings.Repeat("9", 64)
	fresh.ReleaseDirectory = "release-" + fresh.OperationID
	reuse, err := pool.Reserve(t.Context(), fresh)
	if err != nil || reuse.Assignment.Port != port || reuse.Assignment.UID != a.UID {
		t.Fatal(reuse, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	retry, err := RetireRoutedNode(ctx, pool, gate, router, c)
	if err != nil || retry.State != "retired" {
		t.Fatal(retry, err)
	}
	still, err := pool.Lookup(t.Context(), fresh.OperationID)
	if err != nil || still.State != "reserved" {
		t.Fatal(still, err)
	}
	req := httptest.NewRequest("GET", "http://site.example.test/", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != 200 || response.Body.String() != "replacement" {
		t.Fatal(response.Code, response.Body.String())
	}
}
