//go:build linux

package noderuntime

import (
	"context"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodelaunch"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/noderouter"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/portal"
)

// InspectNodeRuntime reports fresh facts for the current routing operation.
// Inbox acceptance alone never provides a healthy or settled observation.
func (r *Runner) InspectNodeRuntime(ctx context.Context, runtimeID, project, operation string) (portal.NodeRuntimeObservation, error) {
	var out portal.NodeRuntimeObservation
	if r == nil || r.Inbox == nil || r.Pool == nil || r.Router == nil || r.Gate == nil || r.Releases == nil {
		return out, ErrAssignment
	}
	if runtimeID != r.Inbox.assignment.RuntimeID || project != r.Inbox.assignment.ProjectID {
		return out, ErrAssignment
	}
	work, err := r.Inbox.Lookup(ctx, operation)
	if err != nil {
		return out, err
	}
	reservation, err := r.Pool.Lookup(ctx, operation)
	if err != nil {
		return out, err
	}
	a := reservation.Assignment
	request := work.Request
	if a.ProjectID != project || a.RuntimeID != runtimeID || a.ArtifactSHA256 != request.ArtifactSHA256 || a.ToolchainSHA256 != request.ToolchainSHA256 || a.Architecture != request.Architecture {
		return out, ErrAssignment
	}
	backend, err := r.Router.BackendForPort(a.Port)
	if err != nil {
		return out, err
	}
	expected := noderouter.Candidate{ProjectID: project, RuntimeID: runtimeID, OperationID: operation, DeploymentID: request.DeploymentID, ArtifactSHA256: request.ArtifactSHA256, Revision: request.Revision, Backend: backend}
	state, err := r.Router.Snapshot(ctx)
	if err != nil {
		return out, err
	}
	if state.Fence != expected {
		return out, ErrReconciliation
	}
	control, err := r.Gate.Inspect(ctx, a)
	if err != nil {
		return out, err
	}
	unit, err := nodelaunch.InspectSystemd(ctx, a)
	if err != nil {
		return out, err
	}
	out = portal.NodeRuntimeObservation{Routing: state, ToolchainSHA256: a.ToolchainSHA256, Architecture: a.Architecture}
	if state.Status == "active" && state.Active != nil && *state.Active == expected {
		if reservation.State != "starting" || reservation.Installed == nil || !control.StartAttempted || control.Retired || unit.ActiveState != "active" || unit.MainPID == 0 || unit.NeedDaemonReload || (unit.Job != "" && unit.Job != "0") {
			return portal.NodeRuntimeObservation{}, ErrReconciliation
		}
		installed, err := (nodelaunch.LinuxInstaller{Releases: r.Releases}).ObserveNodeInstallation(ctx, a, request.Archive)
		if err != nil {
			return portal.NodeRuntimeObservation{}, err
		}
		if installed.Manifest != reservation.Installed.Manifest {
			return portal.NodeRuntimeObservation{}, ErrAssignment
		}
		out.Healthy = r.Router.WaitHealthy(ctx, expected) == nil
		out.Settled = work.State == "settled"
	} else if state.Status == "failed" && reservation.State == "retired" && control.Retired && unit.LoadState == "masked" && unit.UnitStopped() {
		usage, err := nodelaunch.InspectRuntimeUsage(ctx, a)
		if err != nil {
			return portal.NodeRuntimeObservation{}, err
		}
		out.CandidateStopped = !usage.UIDProcessesPresent && !usage.CgroupPopulated && !usage.TCPListenerPresent
		out.Settled = out.CandidateStopped && work.State == "settled"
	}
	latest, err := r.Router.Snapshot(ctx)
	if err != nil {
		return portal.NodeRuntimeObservation{}, err
	}
	if latest.Fence != state.Fence || latest.Status != state.Status || (latest.Active == nil) != (state.Active == nil) || (latest.Active != nil && *latest.Active != *state.Active) {
		return portal.NodeRuntimeObservation{}, ErrReconciliation
	}
	if err = ctx.Err(); err != nil {
		return portal.NodeRuntimeObservation{}, err
	}
	out.ObservedAt = time.Now()
	return out, nil
}
