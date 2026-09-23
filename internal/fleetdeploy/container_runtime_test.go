package fleetdeploy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/appconfig"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/client"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/portal"
	"gopkg.in/yaml.v3"
)

type containerDeployerMock struct {
	mu           sync.Mutex
	preflight    int
	deploy       int
	status       client.AppStatusResult
	deployErr    error
	preflightErr error
}

func (m *containerDeployerMock) PreflightApp(context.Context, string) (client.PreflightResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.preflight++
	return client.PreflightResult{}, m.preflightErr
}
func (m *containerDeployerMock) DeployApp(context.Context, string) (client.DeployResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deploy++
	return client.DeployResult{}, m.deployErr
}
func (m *containerDeployerMock) GetAppStatus(context.Context, string) (client.AppStatusResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status, nil
}
func (m *containerDeployerMock) Close() error { return nil }

func containerRuntimeFixture(t *testing.T, mock *containerDeployerMock) (*Worker, portal.ContainerRuntimeRequest, func()) {
	t.Helper()
	state := t.TempDir()
	dbDir := t.TempDir()
	if err := os.Chmod(dbDir, 0700); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(dbDir, "portal.sqlite")
	store, err := portal.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("1", 64)
	runtimeID := strings.Repeat("2", 64)
	release := testContainerRelease(id)
	release.ID = strings.Repeat("3", 64)
	release.Revision = 1
	q := portal.ContainerRuntimeRequest{
		Deployment: portal.ContainerDeployment{ID: strings.Repeat("4", 64), ProjectID: id, ReleaseID: release.ID, RuntimeID: runtimeID, Revision: 1, State: "running"},
		Release:    release, ActivateBefore: time.Now().Add(30 * time.Second).Unix(),
	}
	w, err := New(store, Config{StateDirectory: state})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	w.factory = func(string) (Deployer, error) { return mock, nil }
	w.cfg.Projects = map[string]Project{id: {Kind: "container", Domain: "app.example", RuntimeID: runtimeID, Architecture: "arm64"}}
	cleanup := func() { w.Close(); store.Close() }
	return w, q, cleanup
}

func containerRuntimeFor(w *Worker, q portal.ContainerRuntimeRequest) *containerRuntime {
	return &containerRuntime{w: w, a: assignment{id: q.Deployment.ProjectID, p: w.cfg.Projects[q.Deployment.ProjectID]}}
}

func healthyContainerStatus(t *testing.T, a assignment, release portal.ContainerRelease) client.AppStatusResult {
	t.Helper()
	raw, err := renderContainerYAML(a, release)
	if err != nil {
		t.Fatal(err)
	}
	var desired map[string]any
	if err := yaml.Unmarshal([]byte(raw), &desired); err != nil {
		t.Fatal(err)
	}
	return client.AppStatusResult{App: client.AppInfo{Name: appName(a.id), Image: release.Image.Image, DesiredState: appconfig.Config(desired)}, LatestDeployment: client.DeploymentInfo{Status: "healthy"}, RuntimeStatus: "healthy", DesiredReplicas: 2, AvailableReplicas: 2, Routes: []client.RouteInfo{{Domain: a.p.Domain, TargetPort: release.Input.Port, Status: "healthy", TLSEnabled: true}}}
}

func TestContainerRuntimeLostReplyReplaysWithoutSecondDeploy(t *testing.T) {
	mock := &containerDeployerMock{deployErr: errors.New("lost reply")}
	w, q, cleanup := containerRuntimeFixture(t, mock)
	defer cleanup()
	r := containerRuntimeFor(w, q)
	if err := r.SubmitContainerRuntime(t.Context(), q); err == nil {
		t.Fatal("expected lost reply")
	}
	if got := mock.deploy; got != 1 {
		t.Fatalf("deploy calls=%d", got)
	}
	w.Close()
	w2, err := New(r.w.store, Config{StateDirectory: w.cfg.StateDirectory})
	if err != nil {
		t.Fatal(err)
	}
	w2.factory = func(string) (Deployer, error) { return mock, nil }
	w2.cfg.Projects = w.cfg.Projects
	defer w2.Close()
	if err := containerRuntimeFor(w2, q).SubmitContainerRuntime(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	if got := mock.deploy; got != 1 {
		t.Fatalf("replayed deploy calls=%d", got)
	}
}

func TestContainerRuntimeRejectsChangedRequestAndCorruptState(t *testing.T) {
	mock := &containerDeployerMock{deployErr: errors.New("lost reply")}
	w, q, cleanup := containerRuntimeFixture(t, mock)
	defer cleanup()
	r := containerRuntimeFor(w, q)
	if err := r.SubmitContainerRuntime(t.Context(), q); err == nil {
		t.Fatal("expected initial error")
	}
	changed := q
	changed.Release.Input.Port++
	if err := r.SubmitContainerRuntime(t.Context(), changed); err == nil {
		t.Fatal("accepted changed settings")
	}
	if err := os.WriteFile(r.statePath(q.Deployment.ID), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.InspectContainerRuntime(t.Context(), q); err == nil {
		t.Fatal("accepted corrupt state")
	}
}

func TestContainerRuntimeInspectionHealthyAndWrongSettingsPending(t *testing.T) {
	mock := &containerDeployerMock{}
	w, q, cleanup := containerRuntimeFixture(t, mock)
	defer cleanup()
	r := containerRuntimeFor(w, q)
	if err := r.SubmitContainerRuntime(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	a := r.a
	mock.status = healthyContainerStatus(t, a, q.Release)
	o, err := r.InspectContainerRuntime(t.Context(), q)
	if err != nil || o.State != "succeeded" {
		t.Fatalf("healthy observation=%+v err=%v", o, err)
	}
	mock.status.App.DesiredState["service"].(map[string]any)["port"] = 9090
	o, err = r.InspectContainerRuntime(t.Context(), q)
	if err != nil || o.State != "pending" {
		t.Fatalf("wrong settings observation=%+v err=%v", o, err)
	}
	op, err := readContainerOperation(r.statePath(q.Deployment.ID))
	if err != nil {
		t.Fatal(err)
	}
	q.ActivateBefore = time.Now().Add(-time.Hour).Unix()
	op.Request = q
	if err = r.save(op); err != nil {
		t.Fatal(err)
	}
	o, err = r.InspectContainerRuntime(t.Context(), q)
	if err != nil || o.State != "pending" {
		t.Fatalf("expired dispatched operation settled without proof: %+v %v", o, err)
	}
}

func TestContainerRuntimeExpiredPreparingNeedsLockAndHealthyPrior(t *testing.T) {
	mock := &containerDeployerMock{preflightErr: errors.New("preflight")}
	w, q, cleanup := containerRuntimeFixture(t, mock)
	defer cleanup()
	prior := q.Release
	prior.ID = strings.Repeat("5", 64)
	prior.Revision = 1
	q.Previous = &prior
	r := containerRuntimeFor(w, q)
	if err := r.SubmitContainerRuntime(t.Context(), q); err == nil {
		t.Fatal("expected preflight failure")
	}
	op, err := readContainerOperation(r.statePath(q.Deployment.ID))
	if err != nil {
		t.Fatal(err)
	}
	// Simulate an interrupted worker before preflight completed.
	op.Stage = "preparing"
	op.Request.ActivateBefore = time.Now().Add(-time.Second).Unix()
	q.ActivateBefore = op.Request.ActivateBefore
	if err := r.save(op); err != nil {
		t.Fatal(err)
	}
	w.Close()
	if _, err := (&containerRuntime{w: &Worker{cfg: Config{StateDirectory: w.cfg.StateDirectory}}, a: r.a}).InspectContainerRuntime(t.Context(), q); err == nil {
		t.Fatal("unlocked worker inspected deployment")
	}
	locked, err := New(r.w.store, Config{StateDirectory: w.cfg.StateDirectory})
	if err != nil {
		t.Fatal(err)
	}
	locked.factory = func(string) (Deployer, error) { return mock, nil }
	locked.cfg.Projects = w.cfg.Projects
	defer locked.Close()
	pending, err := containerRuntimeFor(locked, q).InspectContainerRuntime(t.Context(), q)
	if err != nil || pending.State != "pending" {
		t.Fatalf("unhealthy prior did not retain pending: %+v %v", pending, err)
	}
	mock.status = healthyContainerStatus(t, r.a, prior)
	o, err := containerRuntimeFor(locked, q).InspectContainerRuntime(t.Context(), q)
	if err != nil || o.State != "failed" {
		t.Fatalf("locked observation=%+v err=%v", o, err)
	}
}

func TestContainerRuntimeKnownUnsubmittedFailureSettlesBeforeDeadline(t *testing.T) {
	mock := &containerDeployerMock{preflightErr: errors.New("private upstream details")}
	w, q, cleanup := containerRuntimeFixture(t, mock)
	defer cleanup()
	r := containerRuntimeFor(w, q)
	if err := r.SubmitContainerRuntime(t.Context(), q); err == nil {
		t.Fatal("expected preflight failure")
	}
	op, err := readContainerOperation(r.statePath(q.Deployment.ID))
	if err != nil || op.Stage != "not_submitted" {
		t.Fatalf("missing proof of no submission: %+v %v", op, err)
	}
	raw, err := os.ReadFile(r.statePath(q.Deployment.ID))
	if err != nil || strings.Contains(string(raw), "private upstream details") {
		t.Fatalf("upstream details persisted or read failed: %v", err)
	}
	observation, err := r.InspectContainerRuntime(t.Context(), q)
	if err != nil || observation.State != "failed" {
		t.Fatalf("known rejection still pending: %+v %v", observation, err)
	}
	// A later worker must not revive a rejected intent after a restart.
	w.Close()
	restarted, err := New(w.store, Config{StateDirectory: w.cfg.StateDirectory})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	restarted.cfg.Projects = w.cfg.Projects
	restarted.factory = w.factory
	mock.preflightErr = nil
	if err := containerRuntimeFor(restarted, q).SubmitContainerRuntime(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	if mock.deploy != 0 || mock.preflight != 1 {
		t.Fatalf("rejected intent replayed: deploy=%d preflight=%d", mock.deploy, mock.preflight)
	}
}

func TestContainerRuntimeUnsubmittedUpdateStillRequiresHealthyPrevious(t *testing.T) {
	mock := &containerDeployerMock{preflightErr: errors.New("preflight rejected")}
	w, q, cleanup := containerRuntimeFixture(t, mock)
	defer cleanup()
	prior := q.Release
	prior.ID = strings.Repeat("5", 64)
	q.Previous = &prior
	r := containerRuntimeFor(w, q)
	if err := r.SubmitContainerRuntime(t.Context(), q); err == nil {
		t.Fatal("expected rejection")
	}
	observation, err := r.InspectContainerRuntime(t.Context(), q)
	if err != nil || observation.State != "pending" {
		t.Fatalf("settled without previous release health: %+v %v", observation, err)
	}
	mock.status = healthyContainerStatus(t, r.a, prior)
	observation, err = r.InspectContainerRuntime(t.Context(), q)
	if err != nil || observation.State != "failed" {
		t.Fatalf("healthy previous release did not settle rejection: %+v %v", observation, err)
	}
}
