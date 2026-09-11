//go:build integration

package portal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type runtimeSubmit func(context.Context, NodeRuntimeRequest) error

func (f runtimeSubmit) SubmitNodeRuntime(ctx context.Context, r NodeRuntimeRequest) error {
	return f(ctx, r)
}
func dispatchDeploymentFixture(t *testing.T) (*Store, string, Account, *NodeDeploymentClaim) {
	t.Helper()
	s, path, a, session, release := deploymentFixture(t)
	runtimeID := strings.Repeat("b", 64)
	if _, err := s.RequestNodeDeployment(t.Context(), session.Token, release.ProjectID, release.BuildID, "runtime-dispatch-test", runtimeID); err != nil {
		t.Fatal(err)
	}
	c, err := s.ClaimNodeDeployment(t.Context(), release.ProjectID, runtimeID, release.ToolchainSHA256, release.Architecture)
	if err != nil || c == nil {
		t.Fatal(c, err)
	}
	return s, path, a, c
}
func TestNodeRuntimeDispatchPersistsBeforeLostReply(t *testing.T) {
	t.Parallel()
	s, path, _, c := dispatchDeploymentFixture(t)
	lost := errors.New("lost runtime reply")
	err := s.DispatchNodeDeployment(t.Context(), c.Job.ID, c.OperationID, c.Lease, runtimeSubmit(func(ctx context.Context, r NodeRuntimeRequest) error {
		var raw []byte
		if err := s.db.QueryRow("SELECT dispatch_intent FROM node_deployments WHERE id=?", c.Job.ID).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var saved NodeRuntimeRequest
		if err := json.Unmarshal(raw, &saved); err != nil {
			t.Fatal(err)
		}
		if saved.OperationID != c.OperationID || saved.ProjectID != c.Job.ProjectID || saved.RuntimeID != c.Job.RuntimeID || saved.DeploymentID != c.Job.ID || saved.ReleaseID != c.Release.BuildID || saved.Revision != c.Job.Revision || saved.ToolchainSHA256 != c.Release.ToolchainSHA256 || saved.Architecture != c.Release.Architecture || saved.ArtifactSHA256 != c.Job.ArtifactSHA256 || saved.ActivateBefore <= s.now().Unix() || len(saved.Archive) != 0 || bytes.Contains(raw, []byte(c.Lease)) || !bytes.Equal(r.Archive, c.Archive) {
			t.Fatal("invalid persisted handoff identity")
		}
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 30*time.Second {
			t.Fatal("unbounded dispatch")
		}
		return lost
	}))
	if !errors.Is(err, lost) {
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
	if err = s.DispatchNodeDeployment(t.Context(), c.Job.ID, c.OperationID, c.Lease, runtimeSubmit(func(context.Context, NodeRuntimeRequest) error { t.Error("lost dispatch repeated"); return nil })); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	var state string
	if err = s.db.QueryRow("SELECT state FROM node_deployments WHERE id=?", c.Job.ID).Scan(&state); err != nil || state != "running" {
		t.Fatal(state, err)
	}
}
func TestNodeRuntimeDispatchConcurrentSubmitOnce(t *testing.T) {
	t.Parallel()
	s, _, _, c := dispatchDeploymentFixture(t)
	var calls atomic.Int32
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			results <- s.DispatchNodeDeployment(t.Context(), c.Job.ID, c.OperationID, c.Lease, runtimeSubmit(func(context.Context, NodeRuntimeRequest) error { calls.Add(1); return nil }))
		})
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrConflict) {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 || successes != 1 {
		t.Fatal(calls.Load(), successes)
	}
}
func TestNodeRuntimeDispatchRejectsInvalidAuthorityAndArchive(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"lease", "expired", "revoked", "archive"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s, _, a, c := dispatchDeploymentFixture(t)
			switch name {
			case "lease":
				c.Lease = strings.Repeat("0", 64)
			case "expired":
				if _, err := s.db.Exec("UPDATE node_deployments SET lease_until=0 WHERE id=?", c.Job.ID); err != nil {
					t.Fatal(err)
				}
			case "revoked":
				if _, err := s.db.Exec("UPDATE users SET disabled=1 WHERE id=?", a.ID); err != nil {
					t.Fatal(err)
				}
			case "archive":
				if _, err := s.db.Exec("UPDATE node_releases SET archive=zeroblob(compressed_bytes) WHERE build_id=?", c.Job.ReleaseID); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.DispatchNodeDeployment(t.Context(), c.Job.ID, c.OperationID, c.Lease, runtimeSubmit(func(context.Context, NodeRuntimeRequest) error { t.Error("invalid dispatch sent"); return nil })); err == nil {
				t.Fatal("invalid dispatch accepted")
			}
			var raw []byte
			if err := s.db.QueryRow("SELECT dispatch_intent FROM node_deployments WHERE id=?", c.Job.ID).Scan(&raw); err != nil || len(raw) != 0 {
				t.Fatal(string(raw), err)
			}
		})
	}
}
func TestNodeRuntimeDispatchMigrationFencesLegacyRunning(t *testing.T) {
	t.Parallel()
	s, path, _, c := dispatchDeploymentFixture(t)
	if _, err := s.db.Exec("ALTER TABLE node_deployments DROP COLUMN dispatch_intent; PRAGMA user_version=23"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.DispatchNodeDeployment(t.Context(), c.Job.ID, c.OperationID, c.Lease, runtimeSubmit(func(context.Context, NodeRuntimeRequest) error {
		t.Error("legacy running operation dispatched")
		return nil
	})); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	var version int
	if err = s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 24 {
		t.Fatal(version, err)
	}
}
