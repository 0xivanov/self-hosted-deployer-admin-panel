//go:build linux

package noderuntime

import (
	"context"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodelaunch"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/noderouter"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/portal"
)

func (r *Runner) recoveryCandidate(request portal.NodeRuntimeRequest, a nodelaunch.Assignment) (noderouter.Candidate, error) {
	if a.ProjectID != request.ProjectID || a.RuntimeID != request.RuntimeID || a.OperationID != request.OperationID || a.ArtifactSHA256 != request.ArtifactSHA256 || a.ToolchainSHA256 != request.ToolchainSHA256 || a.Architecture != request.Architecture {
		return noderouter.Candidate{}, ErrAssignment
	}
	backend, err := r.Router.BackendForPort(a.Port)
	if err != nil {
		return noderouter.Candidate{}, err
	}
	return noderouter.Candidate{ProjectID: request.ProjectID, RuntimeID: request.RuntimeID, OperationID: request.OperationID, DeploymentID: request.DeploymentID, ArtifactSHA256: request.ArtifactSHA256, Revision: request.Revision, Backend: backend}, nil
}

func (r *Runner) recoverProcessing(ctx context.Context, request portal.NodeRuntimeRequest) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// Reserve is idempotent, including retired records. A crash before reserve
	// may allocate a slot here solely to fence and retire this operation.
	reserved, err := r.Pool.Reserve(ctx, nodelaunch.Assignment{ProjectID: request.ProjectID, RuntimeID: request.RuntimeID, OperationID: request.OperationID, ArtifactSHA256: request.ArtifactSHA256, ToolchainSHA256: request.ToolchainSHA256, Architecture: request.Architecture, ReleaseDirectory: "release-" + request.OperationID})
	if err != nil {
		return err
	}
	candidate, err := r.recoveryCandidate(request, reserved.Assignment)
	if err != nil {
		return err
	}
	state, err := r.Router.Snapshot(ctx)
	if err != nil {
		return err
	}
	if state.Status == "active" && state.Fence == candidate && state.Active != nil && *state.Active == candidate {
		// Activation already committed. Preserve the replacement, finish retiring
		// earlier reservations and settle the operation without another start.
		outstanding, err := r.Pool.Outstanding(ctx)
		if err != nil {
			return err
		}
		for _, old := range outstanding {
			if old.Assignment.OperationID == request.OperationID {
				continue
			}
			work, err := r.Inbox.Lookup(ctx, old.Assignment.OperationID)
			if err != nil {
				return err
			}
			if work.Request.Revision >= request.Revision {
				return ErrReconciliation
			}
			previous, err := r.recoveryCandidate(work.Request, old.Assignment)
			if err != nil {
				return err
			}
			if _, err = nodelaunch.RetireRoutedNode(ctx, r.Pool, r.Gate, r.Router, previous); err != nil {
				return err
			}
		}
	} else {
		if err = r.Router.RejectActivation(ctx, candidate); err != nil {
			return err
		}
		if _, err = nodelaunch.RetireRoutedNode(ctx, r.Pool, r.Gate, r.Router, candidate); err != nil {
			return err
		}
	}
	return r.Inbox.Settle(ctx, request.OperationID)
}
