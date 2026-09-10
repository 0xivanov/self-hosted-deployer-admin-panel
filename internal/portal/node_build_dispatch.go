package portal

import (
	"context"
	"encoding/json"
	"os"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodebuild"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/npmfetch"
)

// NodeExecutionRequest is a trusted control-plane request, never customer input.
// Archive is transferred separately from the durable intent and never logged.
// The executor must verify all digests and its assigned toolchain, reject an
// expired request, and stop execution by NotAfter. Extension is not yet supported.
// Storage configuration determines where Bundle is read; it is not a guest path.
type NodeExecutionRequest struct {
	ExecutionID     string
	BuildID         string
	ProjectID       string
	Plan            nodebuild.Plan
	ToolchainSHA256 string
	Bundle          npmfetch.Bundle
	NotAfter        int64
	Archive         []byte `json:"-"`
}

// NodeExecutionSubmitter must durably deduplicate ExecutionID, reject mismatched
// identity reuses, and retain retirement tombstones against delayed submissions.
// Cancellation or a nil return does NOT prove termination or build success.
// Implementations must honor context cancellation and enforce VM isolation.
type NodeExecutionSubmitter interface {
	SubmitNodeExecution(context.Context, NodeExecutionRequest) error
}

// DispatchNodeBuild makes at most one submission attempt. Its immutable intent is
// committed before external access. Every uncertain outcome requires executor
// reconciliation, even if a crash occurred before the request left this process.
// This is worker-only access; no browser can supply an executor or storage root.
func (s *Store) DispatchNodeBuild(ctx context.Context, id, execution, lease string, jobs *os.Root, executor NodeExecutionSubmitter) error {
	if executor == nil || jobs == nil {
		return ErrInvalid
	}
	bundle, err := s.NodeBuildDependencies(ctx, id, execution, lease)
	if err != nil {
		return err
	}
	if bundle == nil {
		return ErrBuildConflict
	}
	// Recheck actual source and bundle bytes, including the source's dependency set.
	if err = s.BindNodeBuildDependencies(ctx, id, execution, lease, jobs, *bundle); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	job, err := s.leasedNodeBuild(ctx, tx, id, execution, lease)
	if err != nil {
		return err
	}
	var existing []byte
	if err = tx.QueryRowContext(ctx, "SELECT dispatch_intent FROM node_builds WHERE id=?", id).Scan(&existing); err != nil {
		return err
	}
	if len(existing) > 0 {
		return ErrBuildConflict
	}
	request := NodeExecutionRequest{ExecutionID: execution, BuildID: id, ProjectID: job.ProjectID, Plan: job.Plan, ToolchainSHA256: job.ToolchainSHA256, Bundle: *bundle, NotAfter: s.now().Add(time.Minute).Unix()}
	if err = tx.QueryRowContext(ctx, "SELECT archive FROM uploads WHERE id=? AND project_id=?", job.UploadID, job.ProjectID).Scan(&request.Archive); err != nil {
		return err
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE node_builds SET dispatch_intent=?,lease_until=? WHERE id=?", raw, request.NotAfter, id); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	call, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return executor.SubmitNodeExecution(call, request)
}
