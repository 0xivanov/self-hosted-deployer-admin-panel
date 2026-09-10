package portal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/noderouter"
)

// NodeRuntimeObservation comes from the assigned authenticated runtime, never
// customer app output. Settled means the operation has no outstanding provisioning
// or delayed activation that can change this result. Failed candidates must also
// be stopped. Healthy is a fresh check of the active candidate, not just the
// router's historical successful probe. The runtime must verify listener/artifact
// identity and actual toolchain/architecture before issuing this evidence.
type NodeRuntimeObservation struct {
	Routing          noderouter.State
	ToolchainSHA256  string
	Architecture     string
	Settled          bool
	CandidateStopped bool
	Healthy          bool
	ObservedAt       time.Time
}
type NodeRuntimeReader interface {
	InspectNodeRuntime(ctx context.Context, runtimeID, projectID, operationID string) (NodeRuntimeObservation, error)
}

func matchesDeployment(c noderouter.Candidate, j NodeDeployment, operation string) bool {
	return c.ProjectID == j.ProjectID && c.RuntimeID == j.RuntimeID && c.DeploymentID == j.ID && c.OperationID == operation && c.Revision == j.Revision && c.ArtifactSHA256 == j.ArtifactSHA256 && c.Backend != ""
}
func activeNodeDeploymentID(ctx context.Context, tx *sql.Tx, project string) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, "SELECT deployment_id FROM node_active_deployments WHERE project_id=?", project).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// ReconcileNodeDeployment records observed runtime facts, including after lease
// expiry or permission revocation. It never initiates activation. Recording an
// already-active route after revocation is audited explicitly rather than falsely
// labelling it cancelled. Stopping/reverting such a route needs a new authorized
// runtime operation. Failures preserve the previous active route and reference.
func (s *Store) ReconcileNodeDeployment(ctx context.Context, reader NodeRuntimeReader, project, id string) (bool, error) {
	if reader == nil {
		return false, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	j, err := scanNodeDeployment(tx.QueryRowContext(ctx, "SELECT "+nodeDeploymentColumns+" FROM node_deployments WHERE id=? AND project_id=?", id, project))
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrDenied
	}
	if err != nil {
		return false, err
	}
	var operation string
	var previous []byte
	if err = tx.QueryRowContext(ctx, "SELECT operation_id,result FROM node_deployments WHERE id=?", id).Scan(&operation, &previous); err != nil {
		return false, err
	}
	if (j.State == "succeeded" || j.State == "failed") && len(previous) > 0 {
		return false, tx.Commit()
	}
	if j.State != "running" || operation == "" {
		return false, ErrConflict
	}
	release, err := savedNodeRelease(ctx, tx, j.ReleaseID, project)
	if err != nil {
		return false, err
	}
	if release == nil {
		return false, ErrConflict
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	started := s.now()
	call, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	o, err := reader.InspectNodeRuntime(call, j.RuntimeID, project, operation)
	if err != nil {
		return false, err
	}
	if !o.Settled || !matchesDeployment(o.Routing.Fence, j, operation) || o.ToolchainSHA256 != release.ToolchainSHA256 || o.Architecture != release.Architecture || o.ObservedAt.Before(started) || o.ObservedAt.After(s.now()) {
		return false, ErrConflict
	}
	success := o.Routing.Status == "active"
	if success {
		if !o.Healthy || o.Routing.Active == nil || *o.Routing.Active != o.Routing.Fence {
			return false, ErrConflict
		}
	} else if o.Routing.Status != "failed" || !o.CandidateStopped {
		return false, ErrConflict
	}
	raw, err := json.Marshal(o)
	if err != nil {
		return false, err
	}
	tx, err = s.db.BeginTx(call, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var state, currentOperation string
	if err = tx.QueryRowContext(call, "SELECT state,operation_id FROM node_deployments WHERE id=? AND project_id=?", id, project).Scan(&state, &currentOperation); err != nil {
		return false, err
	}
	if state == "succeeded" || state == "failed" {
		return false, tx.Commit()
	}
	if state != "running" || currentOperation != operation {
		return false, ErrConflict
	}
	active, err := activeNodeDeploymentID(call, tx, project)
	if err != nil {
		return false, err
	}
	if !success {
		if active == "" {
			if o.Routing.Active != nil {
				return false, ErrConflict
			}
		} else {
			var prior []byte
			if err = tx.QueryRowContext(call, "SELECT result FROM node_deployments WHERE id=?", active).Scan(&prior); err != nil {
				return false, err
			}
			var recorded NodeRuntimeObservation
			if err = json.Unmarshal(prior, &recorded); err != nil {
				return false, err
			}
			if recorded.Routing.Active == nil || o.Routing.Active == nil || *recorded.Routing.Active != *o.Routing.Active {
				return false, ErrConflict
			}
		}
	}
	allowed, err := nodeDeploymentActor(call, tx, id)
	if err != nil {
		return false, err
	}
	final := "failed"
	if success {
		final = "succeeded"
		if _, err = tx.ExecContext(call, "INSERT INTO node_active_deployments(project_id,deployment_id,release_id) VALUES(?,?,?) ON CONFLICT(project_id) DO UPDATE SET deployment_id=excluded.deployment_id,release_id=excluded.release_id", project, id, j.ReleaseID); err != nil {
			return false, err
		}
		if active != "" && active != id {
			if _, err = tx.ExecContext(call, "DELETE FROM node_deployment_releases WHERE deployment_id=?", active); err != nil {
				return false, err
			}
		}
	} else {
		if _, err = tx.ExecContext(call, "DELETE FROM node_deployment_releases WHERE deployment_id=?", id); err != nil {
			return false, err
		}
	}
	if _, err = tx.ExecContext(call, "UPDATE node_deployments SET state=?,result=?,lease_hash='',lease_until=0 WHERE id=?", final, raw, id); err != nil {
		return false, err
	}
	var actor, workspace string
	if err = tx.QueryRowContext(call, "SELECT j.actor_id,p.workspace_id FROM node_deployments j JOIN projects p ON p.id=j.project_id WHERE j.id=?", id).Scan(&actor, &workspace); err != nil {
		return false, err
	}
	action := "node_deployment.reconciled_" + final + ":" + id
	if !allowed {
		action += ";submitter_revoked"
	}
	if err = audit(call, tx, actor, workspace, action, s.now().Unix()); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}
func (s *Store) ActiveNodeDeployment(ctx context.Context, token, project string) (*NodeDeployment, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, _, err = s.uploadProject(ctx, tx, token, project, true); err != nil {
		return nil, err
	}
	id, err := activeNodeDeploymentID(ctx, tx, project)
	if err != nil {
		return nil, err
	}
	if id == "" {
		return nil, tx.Commit()
	}
	j, err := scanNodeDeployment(tx.QueryRowContext(ctx, "SELECT "+nodeDeploymentColumns+" FROM node_deployments WHERE id=? AND project_id=?", id, project))
	if err != nil {
		return nil, err
	}
	return &j, tx.Commit()
}
