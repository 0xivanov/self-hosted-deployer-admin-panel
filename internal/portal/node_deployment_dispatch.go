package portal

import (
	"context"
	"encoding/json"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodeartifact"
)

// NodeRuntimeRequest is immutable trusted worker input. The archive travels
// separately from the durable intent and contains no worker/session credential.
// ActivateBefore bounds staging/activation, not the lifetime of a healthy site.
// A runtime must reject expired submissions and never activate after this time;
// uncertain activation or cleanup must be reconciled, never assumed stopped.
type NodeRuntimeRequest struct {
	OperationID, DeploymentID, ProjectID, RuntimeID, ReleaseID string
	ArtifactSHA256, ToolchainSHA256, Architecture              string
	Revision                                                   int64
	ActivateBefore                                             int64
	Archive                                                    []byte `json:"-"`
}

// NodeRuntimeSubmitter must durably deduplicate operation IDs, reject identity
// reuse, validate artifacts/pins, and retain start/retirement fences. Nil success
// means acceptance only; actual routing and service state require reconciliation.
// This is trusted worker access, not a browser-selectable provider or host URL.
type NodeRuntimeSubmitter interface {
	SubmitNodeRuntime(context.Context, NodeRuntimeRequest) error
}

// DispatchNodeDeployment commits the immutable request before one bounded submit
// attempt. No retry dispatches it again, including after a lost reply or restart.
// Running operations inherited from older schemas require reconciliation first.
func (s *Store) DispatchNodeDeployment(ctx context.Context, id, operation, lease string, runtime NodeRuntimeSubmitter) error {
	if runtime == nil || len(lease) != 64 || operation == "" {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM node_deployments WHERE id=? AND state='running' AND operation_id=? AND lease_hash=? AND lease_until>?", id, operation, digest(lease), s.now().Unix()).Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return ErrBuildLease
	}
	allowed, err := nodeDeploymentActor(ctx, tx, id)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrDenied
	}
	var existing []byte
	if err = tx.QueryRowContext(ctx, "SELECT dispatch_intent FROM node_deployments WHERE id=?", id).Scan(&existing); err != nil {
		return err
	}
	if len(existing) > 0 {
		return ErrConflict
	}
	job, err := scanNodeDeployment(tx.QueryRowContext(ctx, "SELECT "+nodeDeploymentColumns+" FROM node_deployments WHERE id=?", id))
	if err != nil {
		return err
	}
	release, err := savedNodeRelease(ctx, tx, job.ReleaseID, job.ProjectID)
	if err != nil {
		return err
	}
	if release == nil || release.ArtifactSHA256 != job.ArtifactSHA256 {
		return ErrConflict
	}
	request := NodeRuntimeRequest{OperationID: operation, DeploymentID: id, ProjectID: job.ProjectID, RuntimeID: job.RuntimeID, ReleaseID: job.ReleaseID, ArtifactSHA256: job.ArtifactSHA256, ToolchainSHA256: release.ToolchainSHA256, Architecture: release.Architecture, Revision: job.Revision, ActivateBefore: s.now().Add(time.Minute).Unix()}
	if err = tx.QueryRowContext(ctx, "SELECT r.archive FROM node_releases r JOIN node_deployment_releases ref ON ref.release_id=r.build_id WHERE ref.deployment_id=? AND r.build_id=?", id, job.ReleaseID).Scan(&request.Archive); err != nil {
		return err
	}
	if _, err = nodeartifact.Validate(ctx, request.Archive, request.ArtifactSHA256); err != nil {
		return err
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE node_deployments SET dispatch_intent=?,lease_until=? WHERE id=?", raw, request.ActivateBefore, id); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	call, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err = call.Err(); err != nil {
		return err
	}
	return runtime.SubmitNodeRuntime(call, request)
}
