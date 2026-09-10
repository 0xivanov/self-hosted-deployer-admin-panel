//go:build integration

package portal

import (
	"context"
	"errors"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodebuild"
	"testing"
	"time"
)

type nodeExecutionFunc func(context.Context, string) (NodeExecutionObservation, error)

func (f nodeExecutionFunc) InspectNodeExecution(ctx context.Context, id string) (NodeExecutionObservation, error) {
	return f(ctx, id)
}

func TestNodeBuildRecoveryRequiresRetiredMatchingExecution(t *testing.T) {
	t.Parallel()
	s, _, _, session, j := buildClaimFixture(t)
	ctx := t.Context()
	claim, err := s.ClaimNodeBuild(ctx, j.ProjectID, j.ToolchainSHA256, "arm64")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	evidence := func() NodeExecutionObservation {
		return NodeExecutionObservation{ExecutionID: claim.ExecutionID, SourceSHA256: j.Plan.SourceSHA256, ToolchainSHA256: j.ToolchainSHA256, Architecture: "arm64", Outcome: "failed", Retired: true, ObservedAt: s.now()}
	}
	good := nodeExecutionFunc(func(context.Context, string) (NodeExecutionObservation, error) { calls++; return evidence(), nil })
	if _, err = s.ReconcileNodeBuildFailure(ctx, good, j.ProjectID, j.ID); !errors.Is(err, ErrBuildLease) || calls != 0 {
		t.Fatal(err, calls)
	}
	now := s.now().Add(2 * time.Minute)
	s.now = func() time.Time { return now }
	for _, tc := range []struct {
		name   string
		change func(*NodeExecutionObservation)
	}{
		{"not retired", func(o *NodeExecutionObservation) { o.Retired = false }},
		{"other execution", func(o *NodeExecutionObservation) { o.ExecutionID = "other" }},
		{"other source", func(o *NodeExecutionObservation) { o.SourceSHA256 = "other" }},
		{"other toolchain", func(o *NodeExecutionObservation) { o.ToolchainSHA256 = "other" }},
		{"other architecture", func(o *NodeExecutionObservation) { o.Architecture = "amd64" }},
		{"success without artifact", func(o *NodeExecutionObservation) { o.Outcome = "succeeded" }},
		{"stale", func(o *NodeExecutionObservation) { o.ObservedAt = now.Add(-time.Second) }},
		{"future", func(o *NodeExecutionObservation) { o.ObservedAt = now.Add(time.Second) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := nodeExecutionFunc(func(context.Context, string) (NodeExecutionObservation, error) {
				o := evidence()
				tc.change(&o)
				return o, nil
			})
			if changed, err := s.ReconcileNodeBuildFailure(ctx, reader, j.ProjectID, j.ID); !errors.Is(err, ErrBuildConflict) || changed {
				t.Fatal(changed, err)
			}
		})
	}
	changed, err := s.ReconcileNodeBuildFailure(ctx, good, j.ProjectID, j.ID)
	if err != nil || !changed {
		t.Fatal(changed, err)
	}
	changed, err = s.ReconcileNodeBuildFailure(ctx, good, j.ProjectID, j.ID)
	if err != nil || changed || calls != 1 {
		t.Fatal(changed, err, calls)
	}
	var state, lease string
	var result []byte
	if err = s.db.QueryRow("SELECT state,lease_hash,result FROM node_builds WHERE id=?", j.ID).Scan(&state, &lease, &result); err != nil || state != "failed" || lease != "" || len(result) == 0 {
		t.Fatal(state, err)
	}
	next, err := s.RequestNodeBuild(ctx, session.Token, j.ProjectID, j.UploadID, "retry-after-retired-execution", NodeBuildAssignment{Settings: nodebuild.Settings{Architecture: "arm64"}, ToolchainSHA256: j.ToolchainSHA256})
	if err != nil || next.State != "queued" || next.ID == j.ID {
		t.Fatal(next, err)
	}
	if _, err = s.RenewNodeBuildLease(ctx, j.ID, claim.ExecutionID, claim.Lease); !errors.Is(err, ErrBuildLease) {
		t.Fatal(err)
	}

}
func TestNodeBuildRecoveryFailureAndRevocation(t *testing.T) {
	t.Parallel()
	s, path, a, _, j := buildClaimFixture(t)
	ctx := t.Context()
	c, err := s.ClaimNodeBuild(ctx, j.ProjectID, j.ToolchainSHA256, "arm64")
	if err != nil {
		t.Fatal(err)
	}
	now := s.now().Add(2 * time.Minute)
	s.now = func() time.Time { return now }
	unavailable := nodeExecutionFunc(func(context.Context, string) (NodeExecutionObservation, error) {
		return NodeExecutionObservation{}, errors.New("executor unavailable")
	})
	if changed, err := s.ReconcileNodeBuildFailure(ctx, unavailable, j.ProjectID, j.ID); err == nil || changed {
		t.Fatal(changed, err)
	}
	if _, err = s.db.Exec("UPDATE users SET disabled=1 WHERE id=?", a.ID); err != nil {
		t.Fatal(err)
	}
	good := nodeExecutionFunc(func(context.Context, string) (NodeExecutionObservation, error) {
		return NodeExecutionObservation{ExecutionID: c.ExecutionID, SourceSHA256: j.Plan.SourceSHA256, ToolchainSHA256: j.ToolchainSHA256, Architecture: "arm64", Outcome: "cancelled", Retired: true, ObservedAt: now}, nil
	})
	if changed, err := s.ReconcileNodeBuildFailure(ctx, good, j.ProjectID, j.ID); err != nil || !changed {
		t.Fatal(changed, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var state string
	var result []byte
	if err = reopened.db.QueryRow("SELECT state,result FROM node_builds WHERE id=?", j.ID).Scan(&state, &result); err != nil || state != "cancelled" || len(result) == 0 {
		t.Fatal(state, err)
	}
}
