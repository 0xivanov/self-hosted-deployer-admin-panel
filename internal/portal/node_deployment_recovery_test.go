//go:build integration

package portal

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/noderouter"
)

type runtimeReader func(context.Context, string, string, string) (NodeRuntimeObservation, error)

func (f runtimeReader) InspectNodeRuntime(ctx context.Context, runtimeID, projectID, operationID string) (NodeRuntimeObservation, error) {
	return f(ctx, runtimeID, projectID, operationID)
}
func routeCandidate(c *NodeDeploymentClaim, backend string) noderouter.Candidate {
	return noderouter.Candidate{ProjectID: c.Job.ProjectID, RuntimeID: c.Job.RuntimeID, DeploymentID: c.Job.ID, OperationID: c.OperationID, Revision: c.Job.Revision, ArtifactSHA256: c.Job.ArtifactSHA256, Backend: backend}
}
func TestNodeDeploymentReconcilesActualRouterAndKeepsActiveArchive(t *testing.T) {
	t.Parallel()
	s, path, _, session, release := deploymentFixture(t)
	ctx := t.Context()
	runtimeID := strings.Repeat("b", 64)
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	defer a.Close()
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) }))
	defer b.Close()
	config := noderouter.Config{ProjectID: release.ProjectID, RuntimeID: runtimeID, ContentHost: "site.example.test", HealthPath: "/health", Backends: map[string]string{"blue": a.URL, "green": b.URL}}
	router, err := noderouter.Open(filepath.Join(t.TempDir(), "private", "router.db"), config)
	if err != nil {
		t.Fatal(err)
	}
	defer router.Close()
	stopped := false
	reads := 0
	reader := runtimeReader(func(ctx context.Context, runtime, project, operation string) (NodeRuntimeObservation, error) {
		reads++
		if runtime != runtimeID || project != release.ProjectID {
			t.Fatal("wrong runtime assignment")
		}
		state, e := router.Snapshot(ctx)
		if e != nil {
			return NodeRuntimeObservation{}, e
		}
		if state.Fence.OperationID != operation {
			t.Fatal("wrong operation")
		}
		healthy := false
		if state.Active != nil {
			req, e := http.NewRequestWithContext(ctx, "GET", config.Backends[state.Active.Backend]+"/health", nil)
			if e != nil {
				return NodeRuntimeObservation{}, e
			}
			response, e := (&http.Client{Timeout: time.Second}).Do(req)
			if e == nil {
				healthy = response.StatusCode == 200
				response.Body.Close()
			}
		}
		return NodeRuntimeObservation{Routing: state, ToolchainSHA256: release.ToolchainSHA256, Architecture: release.Architecture, Settled: true, CandidateStopped: stopped, Healthy: healthy, ObservedAt: s.now()}, nil
	})
	request := func(key string) *NodeDeploymentClaim {
		t.Helper()
		if _, err := s.RequestNodeDeployment(ctx, session.Token, release.ProjectID, release.BuildID, key, runtimeID); err != nil {
			t.Fatal(err)
		}
		c, e := s.ClaimNodeDeployment(ctx, release.ProjectID, runtimeID, release.ToolchainSHA256, release.Architecture)
		if e != nil || c == nil {
			t.Fatal(c, e)
		}
		return c
	}
	first := request("runtime-reconcile-first")
	if err = router.Activate(ctx, routeCandidate(first, "blue")); err != nil {
		t.Fatal(err)
	}
	if changed, err := s.ReconcileNodeDeployment(ctx, reader, release.ProjectID, first.Job.ID); err != nil || !changed {
		t.Fatal(changed, err)
	}
	active, err := s.ActiveNodeDeployment(ctx, session.Token, release.ProjectID)
	if err != nil || active == nil || active.ID != first.Job.ID {
		t.Fatal(active, err)
	}
	if _, err = s.RequestNodeDeployment(ctx, session.Token, release.ProjectID, release.BuildID, "unsupported-runtime-move", strings.Repeat("c", 64)); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	failed := request("runtime-reconcile-failed")
	if err = router.Activate(ctx, routeCandidate(failed, "green")); !errors.Is(err, noderouter.ErrUnhealthy) {
		t.Fatal(err)
	}
	if _, err = s.ReconcileNodeDeployment(ctx, reader, release.ProjectID, failed.Job.ID); !errors.Is(err, ErrConflict) {
		t.Fatal("failed candidate completed before stopping", err)
	}
	b.Close()
	stopped = true
	if changed, err := s.ReconcileNodeDeployment(ctx, reader, release.ProjectID, failed.Job.ID); err != nil || !changed {
		t.Fatal(changed, err)
	}
	active, err = s.ActiveNodeDeployment(ctx, session.Token, release.ProjectID)
	if err != nil || active.ID != first.Job.ID {
		t.Fatal("failed deployment replaced active", active, err)
	}
	next := request("runtime-reconcile-next")
	if err = router.Activate(ctx, routeCandidate(next, "blue")); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("UPDATE node_deployments SET lease_until=0 WHERE id=?", next.Job.ID); err != nil {
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
	if changed, err := s.ReconcileNodeDeployment(ctx, reader, release.ProjectID, next.Job.ID); err != nil || !changed {
		t.Fatal(changed, err)
	}
	before := reads
	if changed, err := s.ReconcileNodeDeployment(ctx, reader, release.ProjectID, next.Job.ID); err != nil || changed || reads != before {
		t.Fatal(changed, err, reads)
	}
	active, err = s.ActiveNodeDeployment(ctx, session.Token, release.ProjectID)
	if err != nil || active.ID != next.Job.ID {
		t.Fatal(active, err)
	}
	var refs int
	if err = s.db.QueryRow("SELECT count(*) FROM node_deployment_releases").Scan(&refs); err != nil || refs != 1 {
		t.Fatal("obsolete references retained", refs, err)
	}
	if err = s.DeleteNodeRelease(ctx, session.Token, release.ProjectID, release.BuildID); !errors.Is(err, ErrRetained) {
		t.Fatal(err)
	}
	// The active pointer independently protects its archive even if the pending
	// reference were lost by an operator repair or a future maintenance bug.
	if _, err = s.db.Exec("DELETE FROM node_deployment_releases"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("DELETE FROM node_releases WHERE build_id=?", release.BuildID); err == nil {
		t.Fatal("active archive lacks a database guard")
	}
	_, other := verifiedAccount(t, s, "other-active@example.test")
	if _, err = s.ActiveNodeDeployment(ctx, other.Token, release.ProjectID); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
}
func TestNodeDeploymentRejectsUnprovenRuntimeStateAndRecordsRevocation(t *testing.T) {
	t.Parallel()
	s, _, a, session, release := deploymentFixture(t)
	ctx := t.Context()
	runtimeID := strings.Repeat("b", 64)
	if _, err := s.RequestNodeDeployment(ctx, session.Token, release.ProjectID, release.BuildID, "runtime-proof-checks", runtimeID); err != nil {
		t.Fatal(err)
	}
	c, err := s.ClaimNodeDeployment(ctx, release.ProjectID, runtimeID, release.ToolchainSHA256, release.Architecture)
	if err != nil {
		t.Fatal(err)
	}
	good := func() NodeRuntimeObservation {
		candidate := routeCandidate(c, "blue")
		return NodeRuntimeObservation{Routing: noderouter.State{Fence: candidate, Status: "active", Active: &candidate}, ToolchainSHA256: release.ToolchainSHA256, Architecture: release.Architecture, Settled: true, Healthy: true, ObservedAt: s.now()}
	}
	for _, mode := range []string{"project", "runtime", "operation", "revision", "artifact", "active", "toolchain", "architecture", "pending", "unsettled", "unhealthy", "stale", "future", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			reader := runtimeReader(func(context.Context, string, string, string) (NodeRuntimeObservation, error) {
				o := good()
				switch mode {
				case "project":
					o.Routing.Fence.ProjectID = "other"
				case "runtime":
					o.Routing.Fence.RuntimeID = "other"
				case "operation":
					o.Routing.Fence.OperationID = "other"
				case "revision":
					o.Routing.Fence.Revision++
				case "artifact":
					o.Routing.Fence.ArtifactSHA256 = "other"
				case "active":
					o.Routing.Active = nil
				case "toolchain":
					o.ToolchainSHA256 = "other"
				case "architecture":
					o.Architecture = "amd64"
				case "pending":
					o.Routing.Status = "pending"
				case "unsettled":
					o.Settled = false
				case "unhealthy":
					o.Healthy = false
				case "stale":
					o.ObservedAt = s.now().Add(-time.Second)
				case "future":
					o.ObservedAt = s.now().Add(time.Second)
				case "unavailable":
					return o, errors.New("runtime unavailable")
				}
				return o, nil
			})
			if changed, err := s.ReconcileNodeDeployment(ctx, reader, release.ProjectID, c.Job.ID); err == nil || changed {
				t.Fatal(changed, err)
			}
			active, err := s.ActiveNodeDeployment(ctx, session.Token, release.ProjectID)
			if err != nil || active != nil {
				t.Fatal(active, err)
			}
		})
	}
	reader := runtimeReader(func(context.Context, string, string, string) (NodeRuntimeObservation, error) {
		if _, err := s.db.Exec("UPDATE users SET disabled=1 WHERE id=?", a.ID); err != nil {
			t.Fatal(err)
		}
		return good(), nil
	})
	if changed, err := s.ReconcileNodeDeployment(ctx, reader, release.ProjectID, c.Job.ID); err != nil || !changed {
		t.Fatal(changed, err)
	}
	var state string
	if err = s.db.QueryRow("SELECT state FROM node_deployments WHERE id=?", c.Job.ID).Scan(&state); err != nil || state != "succeeded" {
		t.Fatal("actual activation falsely cancelled", state, err)
	}
	if _, err = s.ActiveNodeDeployment(ctx, session.Token, release.ProjectID); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
}

func TestNodeDeploymentRecoveryMigratesPendingOperation(t *testing.T) {
	t.Parallel()
	s, path, _, session, release := deploymentFixture(t)
	ctx := t.Context()
	runtimeID := strings.Repeat("b", 64)
	j, err := s.RequestNodeDeployment(ctx, session.Token, release.ProjectID, release.BuildID, "migrate-pending-runtime", runtimeID)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimNodeDeployment(ctx, release.ProjectID, runtimeID, release.ToolchainSHA256, release.Architecture)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("DROP TABLE node_active_deployments; ALTER TABLE node_deployments DROP COLUMN result; ALTER TABLE node_deployments DROP COLUMN dispatch_intent; PRAGMA user_version=22"); err != nil {
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
	reader := runtimeReader(func(context.Context, string, string, string) (NodeRuntimeObservation, error) {
		return NodeRuntimeObservation{Routing: noderouter.State{Fence: routeCandidate(claim, "blue"), Status: "failed"}, ToolchainSHA256: release.ToolchainSHA256, Architecture: release.Architecture, Settled: true, CandidateStopped: true, ObservedAt: s.now()}, nil
	})
	if changed, err := s.ReconcileNodeDeployment(ctx, reader, release.ProjectID, j.ID); err != nil || !changed {
		t.Fatal(changed, err)
	}
	active, err := s.ActiveNodeDeployment(ctx, session.Token, release.ProjectID)
	if err != nil || active != nil {
		t.Fatal(active, err)
	}
	if err = s.DeleteNodeRelease(ctx, session.Token, release.ProjectID, release.BuildID); err != nil {
		t.Fatal("failed migrated operation kept archive pinned", err)
	}
}
