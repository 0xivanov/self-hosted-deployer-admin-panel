package fleetdeploy

import (
	"context"
	"errors"
	"os"
	"reflect"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/client"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/portal"
)

type missingCandidateWithdrawer interface {
	WithdrawDeployRequest(context.Context, string, string, string) (client.DeployRequestResult, error)
}

type candidateContainerDeployer interface {
	AdvanceDeployRequest(context.Context, string, string) (client.DeployRequestResult, error)
	RecoverDeployRequest(context.Context, string, string) (client.DeployRequestResult, error)
}

// AdvanceContainerRuntime advances the already submitted request, never submits
// another deployment. Recovery intent is persisted before dispatch and survives
// lost replies/restarts. A deadline starts recovery; it is not failure proof.
func (r *containerRuntime) AdvanceContainerRuntime(ctx context.Context, q portal.ContainerRuntimeRequest, allowActivation bool) error {
	if r.w == nil || r.w.lock == nil {
		return errors.New("exclusive fleet worker lock required")
	}
	if !r.w.cfg.EnableCandidateOperations {
		return nil
	}
	if !validContainerRequest(r.a, q) {
		return errors.New("invalid candidate request")
	}
	op, err := readContainerOperation(r.statePath(q.Deployment.ID))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(op.Request, q) || op.Domain != r.a.p.Domain {
		return errors.New("candidate operation identity mismatch")
	}
	if !op.CandidateOperations || (op.Stage != "dispatched" && op.Stage != "recovering") {
		return nil
	}
	revision, id, err := r.readFence()
	if err != nil {
		return err
	}
	if revision != q.Deployment.Revision || id != q.Deployment.ID {
		return errors.New("candidate revision fence mismatch")
	}
	c, err := r.w.factory(r.a.id)
	if err != nil {
		return err
	}
	defer c.Close()
	tracker, ok := c.(trackedContainerDeployer)
	if !ok {
		return errors.New("candidate request lookup unavailable")
	}
	candidate, ok := c.(candidateContainerDeployer)
	if !ok {
		return errors.New("candidate operations unavailable")
	}
	record, err := tracker.GetDeployRequest(ctx, appName(r.a.id), op.RequestID)
	if err != nil {
		// A failed lookup is ambiguous. Once activation is no longer allowed,
		// withdraw the original identity; the server atomically records the intent
		// if absent and preserves an already-applied winner. Never infer failure
		// from a transport error or submit the deployment again.
		if op.Stage != "recovering" && allowActivation && q.ActivateBefore > time.Now().Unix() {
			return err
		}
		if op.Stage != "recovering" {
			op.Stage = "recovering"
			if saveErr := r.save(op); saveErr != nil {
				return saveErr
			}
		}
		withdrawer, ok := c.(missingCandidateWithdrawer)
		if !ok {
			return errors.New("missing-request withdrawal unavailable")
		}
		original, renderErr := renderContainerYAML(r.a, q.Release)
		if renderErr != nil || op.OriginalYAML == "" || original != op.OriginalYAML {
			return errors.New("original deployment configuration unavailable or changed")
		}

		record, err = withdrawer.WithdrawDeployRequest(ctx, appName(r.a.id), op.OriginalYAML, op.RequestID)
		if err != nil {
			return err
		}
	}
	if record.AppName != appName(r.a.id) || record.RequestID != op.RequestID || !matchingContainerStates(record.RequestedState, op.PreflightState) {
		return errors.New("candidate receipt identity mismatch")
	}
	if record.State == "applied" {
		// Activation is already committed. Advance now only reclaims older
		// workloads, even if the original activation deadline has passed.
		// Keep the portal job running until this retryable cleanup succeeds.
		_, err = candidate.AdvanceDeployRequest(ctx, appName(r.a.id), op.RequestID)
		return err
	}
	if record.State != "pending" {
		return nil
	} // Observation validates and settles terminal receipts.
	if record.Result != nil {
		return errors.New("pending candidate contains terminal result")
	}
	if op.Stage == "recovering" || !allowActivation || q.ActivateBefore <= time.Now().Unix() {
		if op.Stage != "recovering" {
			op.Stage = "recovering"
			if err = r.save(op); err != nil {
				return err
			}
		}
		_, err = candidate.RecoverDeployRequest(ctx, appName(r.a.id), op.RequestID)
		return err
	}
	call, cancel := context.WithDeadline(ctx, time.Unix(q.ActivateBefore, 0))
	defer cancel()
	_, err = candidate.AdvanceDeployRequest(call, appName(r.a.id), op.RequestID)
	return err
}

var _ portal.ContainerRuntimeAdvancer = (*containerRuntime)(nil)
