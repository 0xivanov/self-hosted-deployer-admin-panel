package fleetdeploy

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/client"
	"gopkg.in/yaml.v3"
)

type candidateDeployer struct {
	*trackedDeployer
	advances, recoveries int
	recoverErr           error
	advanceErr           error
	deadline             time.Time
}

func (d *candidateDeployer) AdvanceDeployRequest(ctx context.Context, _, _ string) (client.DeployRequestResult, error) {
	d.advances++
	d.deadline, _ = ctx.Deadline()
	return d.request, d.advanceErr
}
func (d *candidateDeployer) RecoverDeployRequest(context.Context, string, string) (client.DeployRequestResult, error) {
	d.recoveries++
	return d.request, d.recoverErr
}

func TestContainerCandidateAdvanceAndDurableRecovery(t *testing.T) {
	mock := &candidateDeployer{trackedDeployer: &trackedDeployer{withdrawalDeployer: &withdrawalDeployer{containerDeployerMock: &containerDeployerMock{}}}}
	w, q, cleanup := containerRuntimeFixture(t, mock.containerDeployerMock)
	defer cleanup()
	w.cfg.EnableCandidateOperations = true
	w.factory = func(string) (Deployer, error) { return mock, nil }
	r := containerRuntimeFor(w, q)
	raw, _ := renderContainerYAML(r.a, q.Release)
	var desired map[string]any
	_ = yaml.Unmarshal([]byte(raw), &desired)
	state, _ := json.Marshal(desired)
	mock.preflightState = string(state)
	mock.request = client.DeployRequestResult{AppName: appName(r.a.id), RequestID: q.Deployment.ID, State: "pending", RequestedState: state}
	_ = r.SubmitContainerRuntime(t.Context(), q) // Lost submission reply is intentional.
	if err := r.AdvanceContainerRuntime(t.Context(), q, true); err != nil {
		t.Fatal(err)
	}
	if mock.advances != 1 || mock.deadline.IsZero() || mock.deadline.After(time.Unix(q.ActivateBefore, 0)) {
		t.Fatal("advance did not respect deadline")
	}
	if _, err := r.InspectContainerRuntime(t.Context(), q); err != nil || mock.advances != 1 {
		t.Fatal("observation advanced request")
	}
	mock.recoverErr = errors.New("lost recovery reply")
	if err := r.AdvanceContainerRuntime(t.Context(), q, false); err == nil {
		t.Fatal("expected lost recovery reply")
	}
	op, err := readContainerOperation(r.statePath(q.Deployment.ID))
	if err != nil || op.Stage != "recovering" {
		t.Fatalf("recovery not persisted: %+v %v", op, err)
	}
	restarted := containerRuntimeFor(w, q)
	mock.recoverErr = nil
	if err = restarted.AdvanceContainerRuntime(t.Context(), q, true); err != nil {
		t.Fatal(err)
	}
	if mock.advances != 1 || mock.recoveries != 2 || mock.submissions != 1 {
		t.Fatal("restart activated after recovery intent or resubmitted")
	}
	observation, err := restarted.InspectContainerRuntime(t.Context(), q)
	if err != nil || observation.State != "pending" {
		t.Fatal("recovery dispatch alone became failure proof")
	}
}

func TestContainerCandidateRejectsChangedReceiptAndPreservesLegacyMode(t *testing.T) {
	mock := &candidateDeployer{trackedDeployer: &trackedDeployer{withdrawalDeployer: &withdrawalDeployer{containerDeployerMock: &containerDeployerMock{}}}}
	w, q, cleanup := containerRuntimeFixture(t, mock.containerDeployerMock)
	defer cleanup()
	w.factory = func(string) (Deployer, error) { return mock, nil }
	r := containerRuntimeFor(w, q)
	raw, _ := renderContainerYAML(r.a, q.Release)
	var desired map[string]any
	_ = yaml.Unmarshal([]byte(raw), &desired)
	state, _ := json.Marshal(desired)
	mock.preflightState = string(state)
	mock.request = client.DeployRequestResult{AppName: appName(r.a.id), RequestID: q.Deployment.ID, State: "pending", RequestedState: state}
	_ = r.SubmitContainerRuntime(t.Context(), q) // Legacy mode saved with candidate operations off.
	w.cfg.EnableCandidateOperations = true
	if err := r.AdvanceContainerRuntime(t.Context(), q, true); err != nil || mock.advances != 0 {
		t.Fatal("legacy request was adopted")
	}
	op, err := readContainerOperation(r.statePath(q.Deployment.ID))
	if err != nil {
		t.Fatal(err)
	}
	op.CandidateOperations = true
	if err = r.save(op); err != nil {
		t.Fatal(err)
	}
	mock.request.RequestedState = []byte(`{"name":"other"}`)
	if err = r.AdvanceContainerRuntime(t.Context(), q, true); err == nil || mock.advances != 0 || mock.recoveries != 0 {
		t.Fatal("changed receipt permitted mutation")
	}
	w.cfg.EnableCandidateOperations = false
	if err = r.AdvanceContainerRuntime(t.Context(), q, true); err != nil || mock.advances != 0 {
		t.Fatal("disabled operations mutated runtime")
	}
}

func TestExpiredCandidateRecoversWithoutAnotherActivation(t *testing.T) {
	mock := &candidateDeployer{trackedDeployer: &trackedDeployer{withdrawalDeployer: &withdrawalDeployer{containerDeployerMock: &containerDeployerMock{}}}}
	w, q, cleanup := containerRuntimeFixture(t, mock.containerDeployerMock)
	defer cleanup()
	w.cfg.EnableCandidateOperations = true
	w.factory = func(string) (Deployer, error) { return mock, nil }
	r := containerRuntimeFor(w, q)
	raw, _ := renderContainerYAML(r.a, q.Release)
	var desired map[string]any
	_ = yaml.Unmarshal([]byte(raw), &desired)
	state, _ := json.Marshal(desired)
	mock.preflightState = string(state)
	mock.request = client.DeployRequestResult{AppName: appName(r.a.id), RequestID: q.Deployment.ID, State: "pending", RequestedState: state}
	_ = r.SubmitContainerRuntime(t.Context(), q)
	// Model an operation saved before its deadline elapsed, without a wall-clock wait.
	op, err := readContainerOperation(r.statePath(q.Deployment.ID))
	if err != nil {
		t.Fatal(err)
	}
	q.ActivateBefore = time.Now().Add(-time.Second).Unix()
	op.Request = q
	if err = r.save(op); err != nil {
		t.Fatal(err)
	}
	if err = r.AdvanceContainerRuntime(t.Context(), q, true); err != nil {
		t.Fatal(err)
	}
	if mock.advances != 0 || mock.recoveries != 1 {
		t.Fatal("expired candidate was activated")
	}
	observation, err := r.InspectContainerRuntime(t.Context(), q)
	if err != nil || observation.State != "pending" {
		t.Fatal("expiry was treated as confirmed failure")
	}
}

func TestAppliedCandidateCleanupRetriesAfterDeadlineWithoutRecovery(t *testing.T) {
	mock := &candidateDeployer{trackedDeployer: &trackedDeployer{withdrawalDeployer: &withdrawalDeployer{containerDeployerMock: &containerDeployerMock{}}}}
	w, q, cleanup := containerRuntimeFixture(t, mock.containerDeployerMock)
	defer cleanup()
	w.cfg.EnableCandidateOperations = true
	w.factory = func(string) (Deployer, error) { return mock, nil }
	r := containerRuntimeFor(w, q)
	raw, _ := renderContainerYAML(r.a, q.Release)
	var desired map[string]any
	_ = yaml.Unmarshal([]byte(raw), &desired)
	state, _ := json.Marshal(desired)
	mock.preflightState = string(state)
	mock.request = client.DeployRequestResult{AppName: appName(r.a.id), RequestID: q.Deployment.ID, State: "pending", RequestedState: state}
	_ = r.SubmitContainerRuntime(t.Context(), q)
	// The release committed before its deadline, but old Pods are still draining.
	mock.request.State = "applied"
	op, err := readContainerOperation(r.statePath(q.Deployment.ID))
	if err != nil {
		t.Fatal(err)
	}
	q.ActivateBefore = time.Now().Add(-time.Minute).Unix()
	op.Request = q
	if err = r.save(op); err != nil {
		t.Fatal(err)
	}
	mock.advanceErr = errors.New("older workloads still draining")
	if err = r.AdvanceContainerRuntime(t.Context(), q, false); err == nil {
		t.Fatal("cleanup failure was hidden")
	}
	op, err = readContainerOperation(r.statePath(q.Deployment.ID))
	if err != nil || op.Stage != "dispatched" || mock.recoveries != 0 || mock.advances != 1 {
		t.Fatalf("committed release entered recovery: %+v %v", op, err)
	}
	mock.advanceErr = nil
	restarted := containerRuntimeFor(w, q)
	if err = restarted.AdvanceContainerRuntime(t.Context(), q, false); err != nil {
		t.Fatal(err)
	}
	if mock.advances != 2 || mock.recoveries != 0 || mock.submissions != 1 {
		t.Fatal("cleanup retry activated or resubmitted release")
	}
	if !mock.deadline.IsZero() {
		t.Fatal("cleanup reused expired activation deadline")
	}
}

type missingRequestDeployer struct {
	*candidateDeployer
	withdrawals    int
	original       string
	withdrawErr    error
	beforeWithdraw func()
}

func (d *missingRequestDeployer) WithdrawDeployRequest(_ context.Context, app, original, id string) (client.DeployRequestResult, error) {
	d.withdrawals++
	if d.beforeWithdraw != nil {
		d.beforeWithdraw()
	}
	if app != d.request.AppName || id != d.request.RequestID || original != d.original {
		return client.DeployRequestResult{}, errors.New("changed original request")
	}
	return d.request, d.withdrawErr
}

func TestMissingContainerRequestWithdrawsOriginalAfterDeadline(t *testing.T) {
	for _, outcome := range []string{"pending", "applied", "withdrawn"} {
		t.Run(outcome, func(t *testing.T) {
			mock := &missingRequestDeployer{candidateDeployer: &candidateDeployer{trackedDeployer: &trackedDeployer{withdrawalDeployer: &withdrawalDeployer{containerDeployerMock: &containerDeployerMock{}}}}}
			w, q, cleanup := containerRuntimeFixture(t, mock.containerDeployerMock)
			defer cleanup()
			w.cfg.EnableCandidateOperations = true
			w.factory = func(string) (Deployer, error) { return mock, nil }
			r := containerRuntimeFor(w, q)
			raw, _ := renderContainerYAML(r.a, q.Release)
			var desired map[string]any
			_ = yaml.Unmarshal([]byte(raw), &desired)
			state, _ := json.Marshal(desired)
			mock.preflightState = string(state)
			mock.original = raw
			mock.request = client.DeployRequestResult{AppName: appName(r.a.id), RequestID: q.Deployment.ID, State: "pending", RequestedState: state}
			_ = r.SubmitContainerRuntime(t.Context(), q)
			mock.lookupErr = errors.New("lookup unavailable")
			if err := r.AdvanceContainerRuntime(t.Context(), q, true); err == nil || mock.withdrawals != 0 {
				t.Fatal("lookup error before deadline started withdrawal")
			}
			op, err := readContainerOperation(r.statePath(q.Deployment.ID))
			if err != nil {
				t.Fatal(err)
			}
			q.ActivateBefore = time.Now().Add(-time.Second).Unix()
			op.Request = q
			if err = r.save(op); err != nil {
				t.Fatal(err)
			}
			mock.beforeWithdraw = func() {
				saved, err := readContainerOperation(r.statePath(q.Deployment.ID))
				if err != nil || saved.Stage != "recovering" {
					t.Fatal("withdrawal dispatched before durable intent")
				}
			}
			mock.withdrawErr = errors.New("lost withdrawal reply")
			if err = r.AdvanceContainerRuntime(t.Context(), q, true); err == nil {
				t.Fatal("lost reply accepted as outcome")
			}
			mock.withdrawErr = nil
			mock.request.State = outcome
			restarted := containerRuntimeFor(w, q)
			if err = restarted.AdvanceContainerRuntime(t.Context(), q, true); err != nil {
				t.Fatal(err)
			}
			if mock.submissions != 1 || mock.withdrawals != 2 {
				t.Fatal("request resubmitted or withdrawal not retried")
			}
			if outcome == "applied" && (mock.advances != 1 || mock.recoveries != 0) {
				t.Fatal("applied race winner not preserved and cleaned")
			}
			if outcome == "pending" && mock.recoveries != 1 {
				t.Fatal("pending withdrawal did not recover")
			}
			if outcome == "withdrawn" && (mock.advances != 0 || mock.recoveries != 0) {
				t.Fatal("terminal withdrawal mutated")
			}
		})
	}
}
