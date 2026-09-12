//go:build integration

package portal

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/noderouter"
)

type deploymentWorkerRuntime struct {
	request NodeRuntimeRequest
	calls   int
}

func (r *deploymentWorkerRuntime) SubmitNodeRuntime(_ context.Context, request NodeRuntimeRequest) error {
	r.request = request
	r.calls++
	return errors.New("accepted but reply lost")
}
func (r *deploymentWorkerRuntime) InspectNodeRuntime(_ context.Context, runtime, project, operation string) (NodeRuntimeObservation, error) {
	c := noderouter.Candidate{ProjectID: project, RuntimeID: runtime, OperationID: operation, DeploymentID: r.request.DeploymentID, ArtifactSHA256: r.request.ArtifactSHA256, Revision: r.request.Revision, Backend: "blue"}
	return NodeRuntimeObservation{Routing: noderouter.State{Fence: c, Status: "active", Active: &c}, ToolchainSHA256: r.request.ToolchainSHA256, Architecture: r.request.Architecture, Healthy: true, Settled: true, ObservedAt: time.Now()}, nil
}
func TestDeploymentWorkerReconcilesAcceptedRequestWithoutResubmit(t *testing.T) {
	t.Parallel()
	s, _, _, session, release := deploymentFixture(t)
	runtime := strings.Repeat("b", 64)
	job, err := s.RequestNodeDeployment(t.Context(), session.Token, release.ProjectID, release.BuildID, "worker-deploy-request", runtime)
	if err != nil {
		t.Fatal(err)
	}
	provider := &deploymentWorkerRuntime{}
	changed, err := s.WorkNodeDeployment(t.Context(), release.ProjectID, runtime, release.ToolchainSHA256, release.Architecture, provider)
	if !changed || err == nil || provider.calls != 1 {
		t.Fatal(changed, err, provider.calls)
	}
	changed, err = s.WorkNodeDeployment(t.Context(), release.ProjectID, runtime, release.ToolchainSHA256, release.Architecture, provider)
	if !changed || err != nil || provider.calls != 1 {
		t.Fatal(changed, err, provider.calls)
	}
	active, err := s.ActiveNodeDeployment(t.Context(), session.Token, release.ProjectID)
	if err != nil || active == nil || active.ID != job.ID {
		t.Fatal(active, err)
	}
	if changed, err = s.WorkNodeDeployment(t.Context(), release.ProjectID, runtime, release.ToolchainSHA256, release.Architecture, provider); changed || err != nil || provider.calls != 1 {
		t.Fatal(changed, err, provider.calls)
	}
}
