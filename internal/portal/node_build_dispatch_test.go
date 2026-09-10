//go:build integration

package portal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type nodeSubmit func(context.Context, NodeExecutionRequest) error

func (f nodeSubmit) SubmitNodeExecution(ctx context.Context, r NodeExecutionRequest) error {
	return f(ctx, r)
}

func TestNodeDispatchIntentPrecedesRequestAndSurvivesUncertainty(t *testing.T) {
	t.Parallel()
	s, path, _, c, root, bundle := dependencyFixture(t)
	if err := s.BindNodeBuildDependencies(t.Context(), c.Job.ID, c.ExecutionID, c.Lease, root, bundle); err != nil {
		t.Fatal(err)
	}
	lost := errors.New("response lost")
	submit := nodeSubmit(func(ctx context.Context, r NodeExecutionRequest) error {
		var raw []byte
		if err := s.db.QueryRow("SELECT dispatch_intent FROM node_builds WHERE id=?", c.Job.ID).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var saved NodeExecutionRequest
		if err := json.Unmarshal(raw, &saved); err != nil {
			t.Fatal(err)
		}
		if saved.ExecutionID != c.ExecutionID || saved.Bundle != bundle || saved.Plan.SourceSHA256 != c.Job.Plan.SourceSHA256 || saved.ToolchainSHA256 != c.Job.ToolchainSHA256 || saved.NotAfter <= s.now().Unix() || !bytes.Equal(r.Archive, c.Archive) || len(saved.Archive) != 0 || bytes.Contains(raw, []byte(c.Lease)) {
			t.Fatal("incorrect durable execution identity")
		}
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 30*time.Second {
			t.Fatal("unbounded submit")
		}
		return lost
	})
	if err := s.DispatchNodeBuild(t.Context(), c.Job.ID, c.ExecutionID, c.Lease, root, submit); !errors.Is(err, lost) {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err = reopened.DispatchNodeBuild(t.Context(), c.Job.ID, c.ExecutionID, c.Lease, root, nodeSubmit(func(context.Context, NodeExecutionRequest) error {
		t.Fatal("uncertain request resubmitted after restart")
		return nil
	})); !errors.Is(err, ErrBuildConflict) {
		t.Fatal(err)
	}
	var state string
	if err = reopened.db.QueryRow("SELECT state FROM node_builds WHERE id=?", c.Job.ID).Scan(&state); err != nil || state != "running" {
		t.Fatal(state, err)
	}
}

func TestNodeDispatchConcurrentCallsSubmitOnce(t *testing.T) {
	t.Parallel()
	s, _, _, c, root, bundle := dependencyFixture(t)
	if err := s.BindNodeBuildDependencies(t.Context(), c.Job.ID, c.ExecutionID, c.Lease, root, bundle); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			results <- s.DispatchNodeBuild(t.Context(), c.Job.ID, c.ExecutionID, c.Lease, root, nodeSubmit(func(context.Context, NodeExecutionRequest) error { calls.Add(1); return nil }))
		})
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !errors.Is(err, ErrBuildConflict) {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 || success != 1 {
		t.Fatal(calls.Load(), success)
	}
}

func TestNodeDispatchRejectsInvalidInputsBeforeSubmission(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"unbound", "revoked", "expired", "corrupt", "source", "wrong_execution", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			s, _, a, c, root, bundle := dependencyFixture(t)
			ctx := t.Context()
			if mode != "unbound" {
				if err := s.BindNodeBuildDependencies(ctx, c.Job.ID, c.ExecutionID, c.Lease, root, bundle); err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "revoked":
				_, err := s.db.Exec("UPDATE users SET disabled=1 WHERE id=?", a.ID)
				if err != nil {
					t.Fatal(err)
				}
			case "expired":
				_, err := s.db.Exec("UPDATE node_builds SET lease_until=0 WHERE id=?", c.Job.ID)
				if err != nil {
					t.Fatal(err)
				}
			case "corrupt":
				f, err := root.OpenFile(bundle.Directory+"/bundle.json", os.O_WRONLY|os.O_TRUNC, 0600)
				if err != nil {
					t.Fatal(err)
				}
				_, err = f.Write([]byte("{}"))
				f.Close()
				if err != nil {
					t.Fatal(err)
				}
			case "source":
				_, err := s.db.Exec("UPDATE uploads SET archive=? WHERE id=?", []byte("changed"), c.Job.UploadID)
				if err != nil {
					t.Fatal(err)
				}
			case "wrong_execution":
				c.ExecutionID = "other"
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if err := s.DispatchNodeBuild(ctx, c.Job.ID, c.ExecutionID, c.Lease, root, nodeSubmit(func(context.Context, NodeExecutionRequest) error { t.Fatal("invalid build submitted"); return nil })); err == nil {
				t.Fatal("accepted invalid dispatch")
			}
			var raw []byte
			if err := s.db.QueryRow("SELECT dispatch_intent FROM node_builds WHERE id=?", c.Job.ID).Scan(&raw); err != nil || len(raw) != 0 {
				t.Fatal("invalid dispatch reserved", err)
			}
		})
	}
}

func TestNodeDispatchMigrationPreservesPreparedBuild(t *testing.T) {
	t.Parallel()
	s, path, _, c, root, bundle := dependencyFixture(t)
	if err := s.BindNodeBuildDependencies(t.Context(), c.Job.ID, c.ExecutionID, c.Lease, root, bundle); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("DROP TABLE node_deployment_releases; DROP TABLE node_deployments; DROP TABLE node_releases; ALTER TABLE node_builds DROP COLUMN dispatch_intent; PRAGMA user_version=19"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	saved, err := reopened.NodeBuildDependencies(t.Context(), c.Job.ID, c.ExecutionID, c.Lease)
	if err != nil || saved == nil || *saved != bundle {
		t.Fatal(saved, err)
	}
	if err = reopened.DispatchNodeBuild(t.Context(), c.Job.ID, c.ExecutionID, c.Lease, root, nodeSubmit(func(context.Context, NodeExecutionRequest) error { return nil })); err != nil {
		t.Fatal(err)
	}
}
