package fleetdeploy

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/client"
	"gopkg.in/yaml.v3"
)

type trackedDeployer struct {
	*withdrawalDeployer
	request     client.DeployRequestResult
	submissions int
	lookups     int
	lookupErr   error
}

func (d *trackedDeployer) DeployAppTracked(_ context.Context, _ string, id string) (client.DeployResult, error) {
	d.submissions++
	if id != d.request.RequestID {
		return client.DeployResult{}, errors.New("wrong request ID")
	}
	return client.DeployResult{}, errors.New("synthetic lost reply")
}
func (d *trackedDeployer) GetDeployRequest(_ context.Context, app, id string) (client.DeployRequestResult, error) {
	d.lookups++
	if app != d.request.AppName || id != d.request.RequestID {
		return client.DeployRequestResult{}, errors.New("wrong lookup")
	}
	return d.request, d.lookupErr
}

func TestContainerTrackedLostReplyUsesRecordedResultWithoutReplay(t *testing.T) {
	for _, outcome := range []string{"pending", "applied", "withdrawn", "not-found"} {
		t.Run(outcome, func(t *testing.T) {
			mock := &trackedDeployer{withdrawalDeployer: &withdrawalDeployer{containerDeployerMock: &containerDeployerMock{}}}
			w, q, cleanup := containerRuntimeFixture(t, mock.containerDeployerMock)
			defer cleanup()
			w.factory = func(string) (Deployer, error) { return mock, nil }
			r := containerRuntimeFor(w, q)
			raw, _ := renderContainerYAML(r.a, q.Release)
			var desired map[string]any
			_ = yaml.Unmarshal([]byte(raw), &desired)
			state, _ := json.Marshal(desired)
			mock.preflightState = string(state)
			result := client.DeployResult{RequestedState: state, App: client.AppInfo{ID: "core-app", Name: appName(r.a.id)}, Deployment: client.DeploymentInfo{ID: "core-deployment", AppID: "core-app", Status: "healthy"}}
			mock.request = client.DeployRequestResult{AppName: appName(r.a.id), RequestID: q.Deployment.ID, State: outcome, RequestedState: state, Result: &result}
			if outcome == "withdrawn" {
				result.WithdrawalConfirmed = true
				result.Deployment.Status = "failed"
			}
			if outcome == "not-found" {
				mock.lookupErr = errors.New("request not found")
			}
			mock.status = healthyContainerStatus(t, r.a, q.Release)
			mock.status.App.ID = "core-app"
			mock.status.LatestDeployment.ID = "core-deployment"
			if err := r.SubmitContainerRuntime(t.Context(), q); err == nil {
				t.Fatal("expected lost reply")
			}
			op, err := readContainerOperation(r.statePath(q.Deployment.ID))
			if err != nil || op.RequestID != q.Deployment.ID || op.PreflightState == "" {
				t.Fatal("missing durable request identity")
			}
			// Restarted runtime does not replay the original submission.
			restarted := containerRuntimeFor(w, q)
			if err := restarted.SubmitContainerRuntime(t.Context(), q); err != nil || mock.submissions != 1 {
				t.Fatal("lost reply caused second submission")
			}
			observation, err := restarted.InspectContainerRuntime(t.Context(), q)
			if err != nil {
				t.Fatal(err)
			}
			want := "pending"
			if outcome == "applied" {
				want = "succeeded"
			}
			if outcome == "withdrawn" {
				want = "failed"
			}
			if observation.State != want || mock.lookups != 1 {
				t.Fatalf("outcome=%s observed=%s", outcome, observation.State)
			}
			if outcome == "applied" {
				mock.status.LatestDeployment.ID = "different-attempt"
				observation, err = restarted.InspectContainerRuntime(t.Context(), q)
				if err != nil || observation.State != "pending" {
					t.Fatal("unrelated healthy attempt satisfied receipt")
				}
			}
			if outcome == "withdrawn" {
				mock.lookupErr = errors.New("server now unavailable")
				observation, err = restarted.InspectContainerRuntime(t.Context(), q)
				if err != nil || observation.State != "failed" || mock.lookups != 1 {
					t.Fatal("persisted withdrawal lost after lookup failure")
				}
			}
		})
	}
}

func TestContainerTrackedReceiptRejectsChangedPreflightIdentity(t *testing.T) {
	mock := &trackedDeployer{withdrawalDeployer: &withdrawalDeployer{containerDeployerMock: &containerDeployerMock{}}}
	w, q, cleanup := containerRuntimeFixture(t, mock.containerDeployerMock)
	defer cleanup()
	w.factory = func(string) (Deployer, error) { return mock, nil }
	r := containerRuntimeFor(w, q)
	raw, _ := renderContainerYAML(r.a, q.Release)
	var desired map[string]any
	_ = yaml.Unmarshal([]byte(raw), &desired)
	state, _ := json.Marshal(desired)
	mock.preflightState = string(state)
	mock.request = client.DeployRequestResult{AppName: appName(r.a.id), RequestID: q.Deployment.ID, State: "pending", RequestedState: []byte(`{"name":"other"}`)}
	_ = r.SubmitContainerRuntime(t.Context(), q)
	observation, err := r.InspectContainerRuntime(t.Context(), q)
	if err == nil || observation.State != "pending" {
		t.Fatal("mismatched receipt accepted")
	}
}
