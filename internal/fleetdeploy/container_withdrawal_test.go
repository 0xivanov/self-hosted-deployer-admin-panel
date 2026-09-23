package fleetdeploy

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/client"
	"gopkg.in/yaml.v3"
)

type withdrawalDeployer struct {
	*containerDeployerMock
	proof          client.DeployResult
	preflightState string
	reportErr      error
	reports        int
}

func (d *withdrawalDeployer) PreflightApp(context.Context, string) (client.PreflightResult, error) {
	return client.PreflightResult{DesiredState: d.preflightState}, nil
}
func (d *withdrawalDeployer) DeployAppReportingWithdrawal(context.Context, string) (client.DeployResult, error) {
	d.reports++
	return d.proof, d.reportErr
}

func TestContainerConfirmedWithdrawalPersistsAndWaitsForPreviousHealth(t *testing.T) {
	mock := &withdrawalDeployer{containerDeployerMock: &containerDeployerMock{}}
	w, q, cleanup := containerRuntimeFixture(t, mock.containerDeployerMock)
	defer cleanup()
	previous := q.Release
	previous.ID = strings.Repeat("5", 64)
	q.Previous = &previous
	q.Deployment.Revision = 2
	w.factory = func(string) (Deployer, error) { return mock, nil }
	r := containerRuntimeFor(w, q)
	raw, _ := renderContainerYAML(r.a, q.Release)
	var desired map[string]any
	_ = yaml.Unmarshal([]byte(raw), &desired)
	state, _ := json.Marshal(desired)
	mock.preflightState = string(state)
	mock.proof = client.DeployResult{WithdrawalConfirmed: true, RequestedState: state, App: client.AppInfo{ID: "core-app", Name: appName(r.a.id)}, Deployment: client.DeploymentInfo{ID: "core-deployment", AppID: "core-app", Status: "failed", FailureReason: "synthetic-private-error"}}
	if err := r.SubmitContainerRuntime(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	op, err := readContainerOperation(r.statePath(q.Deployment.ID))
	if err != nil || op.Stage != "withdrawn" {
		t.Fatal("withdrawal not persisted")
	}
	journal, _ := os.ReadFile(r.statePath(q.Deployment.ID))
	if strings.Contains(string(journal), "synthetic-private-error") {
		t.Fatal("upstream error persisted")
	}
	observation, err := r.InspectContainerRuntime(t.Context(), q)
	if err != nil || observation.State != "pending" {
		t.Fatal("unhealthy predecessor settled")
	}
	mock.status = healthyContainerStatus(t, r.a, previous)
	mock.status.App.ID = "core-app"
	mock.status.LatestDeployment = mock.proof.Deployment
	observation, err = r.InspectContainerRuntime(t.Context(), q)
	if err != nil || observation.State != "failed" {
		t.Fatal("restored predecessor did not settle")
	}
	// Reconstruct runtime using only the durable operation record.
	restarted := containerRuntimeFor(w, q)
	if err := restarted.SubmitContainerRuntime(t.Context(), q); err != nil || mock.reports != 1 {
		t.Fatal("withdrawn operation replayed")
	}
	mock.status.LatestDeployment.ID = "different-attempt"
	observation, err = restarted.InspectContainerRuntime(t.Context(), q)
	if err != nil || observation.State != "pending" {
		t.Fatal("unrelated failure satisfied withdrawal")
	}
}

func TestContainerWithdrawalRejectsWrongIdentityAndLostReply(t *testing.T) {
	for _, kind := range []string{"lost-reply", "wrong-config", "no-proof", "confirmed-first"} {
		t.Run(kind, func(t *testing.T) {
			mock := &withdrawalDeployer{containerDeployerMock: &containerDeployerMock{}}
			w, q, cleanup := containerRuntimeFixture(t, mock.containerDeployerMock)
			defer cleanup()
			w.factory = func(string) (Deployer, error) { return mock, nil }
			r := containerRuntimeFor(w, q)
			raw, _ := renderContainerYAML(r.a, q.Release)
			var desired map[string]any
			_ = yaml.Unmarshal([]byte(raw), &desired)
			state, _ := json.Marshal(desired)
			mock.preflightState = string(state)
			mock.proof = client.DeployResult{WithdrawalConfirmed: true, RequestedState: state, App: client.AppInfo{ID: "core-app", Name: appName(r.a.id)}, Deployment: client.DeploymentInfo{ID: "core-deploy", AppID: "core-app", Status: "failed"}}
			switch kind {
			case "lost-reply":
				mock.reportErr = errors.New("reply lost")
			case "wrong-config":
				mock.proof.RequestedState = []byte(`{"name":"wrong"}`)
			case "no-proof":
				mock.proof.WithdrawalConfirmed = false
			}
			_ = r.SubmitContainerRuntime(t.Context(), q)
			observation, err := r.InspectContainerRuntime(t.Context(), q)
			if err != nil {
				t.Fatal(err)
			}
			want := "pending"
			if kind == "confirmed-first" {
				want = "failed"
			}
			if observation.State != want {
				t.Fatalf("state=%s want=%s", observation.State, want)
			}
			if err := r.SubmitContainerRuntime(t.Context(), q); err != nil || mock.reports != 1 {
				t.Fatal("replayed uncertain or withdrawn request")
			}
		})
	}
}
