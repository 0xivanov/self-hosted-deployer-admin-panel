//go:build linux

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodelaunch"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/noderouter"
)

func openFixturePool(a nodelaunch.Assignment) (*nodelaunch.Pool, error) {
	return nodelaunch.OpenPool("/var/lib/deployer-node-lab/pool/state.db", nodelaunch.PoolConfig{ProjectID: a.ProjectID, RuntimeID: a.RuntimeID, ToolchainSHA256: a.ToolchainSHA256, Architecture: a.Architecture, Slots: []nodelaunch.Slot{{UID: 60000, Port: 31877}, {UID: 60001, Port: 31878}}})
}
func fixtureID() string {
	var entropy [32]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(entropy[:])
}

// Trusted disposable fixture only. The replacement is a synthetic HTTP server;
// the retired backend is the actual restricted Node/systemd service.
func retireRoutedFixture(ctx context.Context, gate *nodelaunch.ControlGate, a nodelaunch.Assignment) error {
	pool, err := openFixturePool(a)
	if err != nil {
		return err
	}
	defer pool.Close()
	before, err := pool.Lookup(ctx, a.OperationID)
	if err != nil {
		return err
	}
	if before.Assignment != a || before.State != "starting" || before.Installed == nil {
		return fmt.Errorf("missing installed start authorization")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:31878")
	if err != nil {
		return err
	}
	replacement := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("replacement")) }))
	replacement.Listener.Close()
	replacement.Listener = listener
	replacement.Start()
	defer replacement.Close()
	router, err := noderouter.Open("/var/lib/deployer-node-lab/routes/"+a.OperationID+".db", noderouter.Config{ProjectID: a.ProjectID, RuntimeID: a.RuntimeID, ContentHost: "site.example.test", HealthPath: "/health", Backends: map[string]string{"node": "http://127.0.0.1:31877", "replacement": "http://127.0.0.1:31878"}})
	if err != nil {
		return err
	}
	defer router.Close()
	candidate := noderouter.Candidate{ProjectID: a.ProjectID, RuntimeID: a.RuntimeID, OperationID: a.OperationID, DeploymentID: fixtureID(), ArtifactSHA256: a.ArtifactSHA256, Revision: 1, Backend: "node"}
	if err = router.Activate(ctx, candidate); err != nil {
		return err
	}
	request := httptest.NewRequest("GET", "http://site.example.test/health", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 200 {
		return fmt.Errorf("Node proxy failed: %d", response.Code)
	}
	next := candidate
	next.OperationID = fixtureID()
	next.DeploymentID = fixtureID()
	next.Backend = "replacement"
	next.Revision = 2
	if err = router.Activate(ctx, next); err != nil {
		return err
	}
	// Linux may briefly retain a reaping process after systemd reports stopped.
	deadline := time.Now().Add(3 * time.Second)
	var retired nodelaunch.Reservation
	for {
		retired, err = nodelaunch.RetireRoutedNode(ctx, pool, gate, router, candidate)
		if err == nil {
			break
		}
		if !errors.Is(err, nodelaunch.ErrRetirement) || time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	if retired.State != "retired" || retired.Retirement == nil {
		return fmt.Errorf("retirement not recorded")
	}
	if err = pool.Close(); err != nil {
		return err
	}
	pool, err = openFixturePool(a)
	if err != nil {
		return err
	}
	defer pool.Close()
	reused := a
	reused.UID = 0
	reused.Port = 0
	reused.OperationID = fixtureID()
	reused.ReleaseDirectory = "release-" + reused.OperationID
	reserved, err := pool.Reserve(ctx, reused)
	if err != nil {
		return err
	}
	if reserved.Assignment.UID != a.UID || reserved.Assignment.Port != a.Port {
		return fmt.Errorf("slot was not reusable")
	}
	if _, err = nodelaunch.RetireRoutedNode(ctx, pool, gate, router, candidate); err != nil {
		return err
	}
	current, err := pool.Lookup(ctx, reused.OperationID)
	if err != nil || current.State != "reserved" {
		return fmt.Errorf("old retry affected new reservation: %v", err)
	}
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 200 || response.Body.String() != "replacement" {
		return fmt.Errorf("replacement stopped serving")
	}
	// Release the unused test reservation through the same verified workflow so
	// the durable shared pool remains usable by the next rehearsal.
	cleanup := candidate
	cleanup.OperationID = reused.OperationID
	cleanup.DeploymentID = fixtureID()
	cleanup.Revision = 3
	if _, err = nodelaunch.RetireRoutedNode(ctx, pool, gate, router, cleanup); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"routed_live_retirement": "passed", "operation": a.OperationID, "receipt": retired.Retirement, "slot_reused": true, "old_retry_safe": true, "replacement_serving": true})
}
