package portal

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type NodeDeploymentRuntime interface {
	NodeRuntimeReader
	NodeRuntimeSubmitter
}

// WorkNodeDeployment handles one assigned project's queued or running operation.
// Running work is only observed, never reclaimed or resubmitted. The runtime
// provider remains responsible for durable acceptance and execution isolation.
func (s *Store) WorkNodeDeployment(ctx context.Context, project, runtimeID, toolchain, architecture string, provider NodeDeploymentRuntime) (bool, error) {
	if provider == nil {
		return false, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	job, err := scanNodeDeployment(s.db.QueryRowContext(ctx, "SELECT "+nodeDeploymentColumns+" FROM node_deployments WHERE project_id=? AND runtime_id=? AND state='running'", project, runtimeID))
	if err == nil {
		return s.ReconcileNodeDeployment(ctx, provider, project, job.ID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	claim, err := s.ClaimNodeDeployment(ctx, project, runtimeID, toolchain, architecture)
	if err != nil || claim == nil {
		return false, err
	}
	if err = s.DispatchNodeDeployment(ctx, claim.Job.ID, claim.OperationID, claim.Lease, provider); err != nil {
		return true, err
	}
	_, err = s.ReconcileNodeDeployment(ctx, provider, project, claim.Job.ID)
	return true, err
}
