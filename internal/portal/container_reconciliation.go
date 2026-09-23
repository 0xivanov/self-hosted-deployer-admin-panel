package portal

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// ContainerRuntimeObservation is trusted worker evidence, never browser input.
// Failed means the candidate cannot activate now or later and the previous
// release, if any, remains healthy. Elapsed time alone cannot prove failure.
type ContainerRuntimeObservation struct {
	DeploymentID string
	ReleaseID    string
	RuntimeID    string
	Revision     int64
	State        string
	ObservedAt   time.Time
}
type ContainerRuntimeObserver interface {
	InspectContainerRuntime(context.Context, ContainerRuntimeRequest) (ContainerRuntimeObservation, error)
}

func decodeContainerIntent(raw []byte, d ContainerDeployment) (ContainerRuntimeRequest, error) {
	var q ContainerRuntimeRequest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&q); err != nil {
		return q, ErrInvalid
	}
	// Canonical re-encoding also rejects trailing JSON and unexpected storage edits.
	encoded, err := json.Marshal(q)
	if err != nil || !bytes.Equal(raw, encoded) {
		return q, ErrInvalid
	}
	if q.Deployment != d || q.Release.ID != d.ReleaseID || q.Release.ProjectID != d.ProjectID || q.ActivateBefore <= 0 || !validContainerCandidate(q.Release.Input, q.Release.Image) {
		return q, ErrInvalid
	}
	normalized, err := normalizeContainerInput(q.Release.Input)
	if err != nil || normalized != q.Release.Input {
		return q, ErrInvalid
	}
	if q.Previous != nil && (q.Previous.ProjectID != d.ProjectID || !validContainerCandidate(q.Previous.Input, q.Previous.Image)) {
		return q, ErrInvalid
	}
	if q.Previous != nil {
		normalized, err := normalizeContainerInput(q.Previous.Input)
		if err != nil || normalized != q.Previous.Input {
			return q, ErrInvalid
		}
	}
	return q, nil
}

// ReconcileContainerDeployment reads the committed request, observes outside
// the database transaction, and applies only fresh, matching terminal evidence.
// Pending/failed observations never trigger a second deployment submission.
func (s *Store) ReconcileContainerDeployment(ctx context.Context, project, runtimeID string, runtime ContainerRuntimeObserver) (bool, error) {
	if runtime == nil {
		return false, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	d, err := scanContainerDeployment(tx.QueryRowContext(ctx, "SELECT "+containerDeploymentColumns+" FROM container_deployments WHERE project_id=? AND runtime_id=? AND state='running' ORDER BY revision LIMIT 1", project, runtimeID))
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var raw []byte
	if err = tx.QueryRowContext(ctx, "SELECT dispatch_intent FROM container_deployments WHERE id=?", d.ID).Scan(&raw); err != nil {
		return false, err
	}
	q, err := decodeContainerIntent(raw, d)
	if err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	call, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	observation, err := runtime.InspectContainerRuntime(call, q)
	if err != nil {
		return true, err
	}
	if err = call.Err(); err != nil {
		return true, err
	}
	now := s.now()
	if observation.DeploymentID != d.ID || observation.ReleaseID != d.ReleaseID || observation.RuntimeID != d.RuntimeID || observation.Revision != d.Revision || observation.ObservedAt.After(now) || now.Sub(observation.ObservedAt) > time.Minute {
		return true, ErrInvalid
	}
	if observation.State == "pending" {
		return true, nil
	}
	if observation.State != "succeeded" && observation.State != "failed" {
		return true, ErrInvalid
	}
	tx, err = s.db.BeginTx(ctx, nil)
	if err != nil {
		return true, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, "UPDATE container_deployments SET state=? WHERE id=? AND state='running' AND dispatch_intent=?", observation.State, d.ID, raw)
	if err != nil {
		return true, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return true, err
	}
	if changed != 1 {
		return true, ErrConflict
	}
	var actor, workspace string
	if err = tx.QueryRowContext(ctx, "SELECT d.actor_id,p.workspace_id FROM container_deployments d JOIN projects p ON p.id=d.project_id WHERE d.id=?", d.ID).Scan(&actor, &workspace); err != nil {
		return true, err
	}
	if err = audit(ctx, tx, actor, workspace, "container_deployment."+observation.State+":"+d.ID, now.Unix()); err != nil {
		return true, err
	}
	return true, tx.Commit()
}

type ContainerRuntime interface {
	ContainerRuntimeSubmitter
	ContainerRuntimeObserver
}

// WorkContainerDeployment reconciles existing work before admitting a queued
// request. The caller must hold the fleet's exclusive worker lock.
func (s *Store) WorkContainerDeployment(ctx context.Context, project, runtimeID string, runtime ContainerRuntime) (bool, error) {
	worked, err := s.ReconcileContainerDeployment(ctx, project, runtimeID, runtime)
	if worked || err != nil {
		return worked, err
	}
	return s.DispatchContainerDeployment(ctx, project, runtimeID, runtime)
}
