//go:build linux

package noderuntime

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodelaunch"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/noderouter"
)

var ErrReconciliation = errors.New("runtime operation needs reconciliation before another launch")

// Runner connects accepted work to the existing isolated Linux runtime primitives.
// The operator must provision the verified toolchain, exclusive accounts/ports,
// private control storage and content ingress before using it. A failed run is
// retained for reconciliation; it is never blindly restarted.
type Runner struct {
	Inbox    *Inbox
	Pool     *nodelaunch.Pool
	Gate     *nodelaunch.ControlGate
	Router   *noderouter.Router
	Releases *os.Root
}

// ProcessNext executes one fresh claim through installation, start and activation.
// It is not a recovery loop: interrupted processing is surfaced explicitly, and
// all start/installation attempts remain fenced by their durable local gates.
func (r *Runner) ProcessNext(ctx context.Context) (bool, error) {
	if r == nil || r.Inbox == nil || r.Pool == nil || r.Gate == nil || r.Router == nil || r.Releases == nil {
		return false, ErrAssignment
	}
	processing, err := r.Inbox.Processing(ctx)
	if err != nil {
		return false, err
	}
	if processing != nil {
		return false, ErrReconciliation
	}
	work, err := r.Inbox.Claim(ctx)
	if err != nil || work == nil {
		return false, err
	}
	request := work.Request
	requested := nodelaunch.Assignment{ProjectID: request.ProjectID, RuntimeID: request.RuntimeID, OperationID: request.OperationID, ArtifactSHA256: request.ArtifactSHA256, ToolchainSHA256: request.ToolchainSHA256, Architecture: request.Architecture, ReleaseDirectory: "release-" + request.OperationID}
	reserved, err := r.Pool.Reserve(ctx, requested)
	if err != nil {
		return true, err
	}
	assignment := reserved.Assignment
	backend, err := r.Router.BackendForPort(assignment.Port)
	if err != nil {
		return true, err
	}
	candidate := noderouter.Candidate{ProjectID: request.ProjectID, RuntimeID: request.RuntimeID, DeploymentID: request.DeploymentID, OperationID: request.OperationID, Revision: request.Revision, ArtifactSHA256: request.ArtifactSHA256, Backend: backend}
	if _, err = r.Router.BackendPort(candidate); err != nil {
		return true, err
	}
	if request.ActivateBefore <= time.Now().Unix() {
		if _, err = nodelaunch.RetireRoutedNode(ctx, r.Pool, r.Gate, r.Router, candidate); err != nil {
			return true, err
		}
		return true, r.Inbox.Settle(ctx, request.OperationID)
	}
	stage, cancel := context.WithDeadline(ctx, time.Unix(request.ActivateBefore, 0))
	defer cancel()
	installer := nodelaunch.GatedInstaller{Gate: r.Gate, Installer: nodelaunch.LinuxInstaller{Releases: r.Releases}}
	if _, err = r.Pool.PrepareRelease(stage, request.OperationID, request.Archive, installer); err != nil {
		return true, err
	}
	if _, err = r.Pool.ClaimStart(stage, request.OperationID); err != nil {
		return true, err
	}
	if err = r.Gate.StartInstalledSystemd(stage, r.Pool, assignment, request.Archive, r.Releases); err != nil {
		return true, err
	}
	if err = r.Router.WaitHealthy(stage, candidate); err != nil {
		return true, err
	}
	previous, err := r.Router.Snapshot(stage)
	if err != nil {
		return true, err
	}
	if err = r.Router.Activate(stage, candidate); err != nil {
		return true, err
	}
	// Retirement uses its own bounded context after successful activation. The
	// activation deadline must not tear down the healthy replacement website.
	cleanup, cancelCleanup := context.WithTimeout(ctx, 30*time.Second)
	defer cancelCleanup()
	if previous.Active != nil && previous.Active.OperationID != candidate.OperationID {
		if _, err = nodelaunch.RetireRoutedNode(cleanup, r.Pool, r.Gate, r.Router, *previous.Active); err != nil {
			return true, err
		}
	}
	return true, r.Inbox.Settle(cleanup, request.OperationID)
}
