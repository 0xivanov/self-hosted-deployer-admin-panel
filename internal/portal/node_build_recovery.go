package portal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// NodeExecutionObservation is evidence from the trusted executor control plane,
// never a report from customer code. Retired means the operation cannot start or
// resume, including a delayed create request. Merely not finding a VM is NOT proof.
type NodeExecutionObservation struct {
	// ProjectID is required by the remote executor transport to verify scope.
	ProjectID       string
	ExecutionID     string
	SourceSHA256    string
	ToolchainSHA256 string
	Architecture    string
	Outcome         string // succeeded requires NodeArtifactObservation and archive validation
	Retired         bool
	ObservedAt      time.Time
}
type NodeExecutionReader interface {
	InspectNodeExecution(context.Context, string) (NodeExecutionObservation, error)
}

// ReconcileNodeBuildFailure frees an expired pending build only after fresh,
// identity-matched evidence proves the executor operation has been retired.
// Cleanup remains possible when the submitter has lost workspace permissions.
// There is no redispatch and no success/hosting activation through this method.
func (s *Store) ReconcileNodeBuildFailure(ctx context.Context, p NodeExecutionReader, project, id string) (bool, error) {
	if p == nil {
		return false, ErrInvalid
	}
	job, err := scanNodeBuild(s.db.QueryRowContext(ctx, "SELECT "+nodeBuildColumns+" FROM node_builds WHERE id=? AND project_id=?", id, project))
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrDenied
	}
	if err != nil {
		return false, err
	}
	var execution string
	var until int64
	var previous []byte
	if err = s.db.QueryRowContext(ctx, "SELECT execution_id,lease_until,result FROM node_builds WHERE id=?", id).Scan(&execution, &until, &previous); err != nil {
		return false, err
	}
	if (job.State == "failed" || job.State == "cancelled") && len(previous) > 0 {
		return false, nil
	}
	if job.State != "running" || execution == "" {
		return false, ErrBuildConflict
	}
	if until > s.now().Unix() {
		return false, ErrBuildLease
	}
	started := s.now()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	observation, err := p.InspectNodeExecution(ctx, execution)
	if err != nil {
		return false, err
	}
	if observation.ExecutionID != execution || observation.SourceSHA256 != job.Plan.SourceSHA256 || observation.ToolchainSHA256 != job.ToolchainSHA256 || observation.Architecture != job.Plan.Architecture || !observation.Retired || (observation.Outcome != "failed" && observation.Outcome != "cancelled") || observation.ObservedAt.Before(started) || observation.ObservedAt.After(s.now()) {
		return false, ErrBuildConflict
	}
	raw, err := json.Marshal(observation)
	if err != nil {
		return false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	// Recheck after network access. Another reconciler may have completed the job.
	var state, currentExecution string
	var currentUntil int64
	if err = tx.QueryRowContext(ctx, "SELECT state,execution_id,lease_until FROM node_builds WHERE id=? AND project_id=?", id, project).Scan(&state, &currentExecution, &currentUntil); err != nil {
		return false, err
	}
	if state != "running" || currentExecution != execution || currentUntil > s.now().Unix() {
		return false, ErrBuildConflict
	}
	if _, err = tx.ExecContext(ctx, "UPDATE node_builds SET state=?,result=?,lease_hash='',lease_until=0 WHERE id=?", observation.Outcome, raw, id); err != nil {
		return false, err
	}
	var actor, workspace string
	if err = tx.QueryRowContext(ctx, "SELECT j.actor_id,p.workspace_id FROM node_builds j JOIN projects p ON p.id=j.project_id WHERE j.id=?", id).Scan(&actor, &workspace); err != nil {
		return false, err
	}
	if err = audit(ctx, tx, actor, workspace, "node_build.reconciled_"+observation.Outcome+":"+id, s.now().Unix()); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}
