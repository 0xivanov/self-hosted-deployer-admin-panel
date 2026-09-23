package fleetdeploy

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/client"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/portal"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/registryimage"
)

type credentialDeployer struct {
	*containerDeployerMock
	mu           sync.Mutex
	order        []string
	credErr      error
	appName      string
	revision     string
	registryHost string
	credential   registryimage.Credentials
}

func (d *credentialDeployer) CreateRegistryCredential(_ context.Context, appName, revision, registry string, credentials registryimage.Credentials) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.order = append(d.order, "register")
	d.appName, d.revision, d.registryHost = appName, revision, registry
	d.credential = credentials
	if appName == "" || revision == "" || registry == "" {
		return errors.New("missing registration identity")
	}
	return d.credErr
}

func (d *credentialDeployer) PreflightApp(ctx context.Context, spec string) (client.PreflightResult, error) {
	d.mu.Lock()
	d.order = append(d.order, "preflight")
	d.mu.Unlock()
	return d.containerDeployerMock.PreflightApp(ctx, spec)
}

func (d *credentialDeployer) DeployApp(ctx context.Context, spec string) (client.DeployResult, error) {
	d.mu.Lock()
	d.order = append(d.order, "deploy")
	d.mu.Unlock()
	return d.containerDeployerMock.DeployApp(ctx, spec)
}

func credentialRuntimeFixture(t *testing.T, mock *credentialDeployer) (*Worker, portal.ContainerRuntimeRequest, *portal.ContainerCredentials, string, func()) {
	t.Helper()
	w, q, cleanup := containerRuntimeFixture(t, mock.containerDeployerMock)
	w.store.ConfigureContainerProjects(true)
	ctx := t.Context()
	account, verifyToken, err := w.store.Register(ctx, "fleet-credentials@example.test", "a long synthetic password", "fleet credentials")
	if err != nil {
		t.Fatal(err)
	}
	if err = w.store.Verify(ctx, verifyToken); err != nil {
		t.Fatal(err)
	}
	session, err := w.store.Login(ctx, account.Email, "a long synthetic password")
	if err != nil {
		t.Fatal(err)
	}
	project, err := w.store.CreateProject(ctx, session.Token, account.WorkspaceID, "fleet-container", "container")
	if err != nil {
		t.Fatal(err)
	}
	vault, err := portal.NewContainerCredentials(w.store, []byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	credential, err := vault.Create(ctx, session.Token, project.ID, "fleet-credential-request-key", "primary", "ghcr.io", registryimage.Credentials{Username: "robot", Password: "fleet-secret"})
	if err != nil {
		t.Fatal(err)
	}
	q.Deployment.ProjectID = project.ID
	q.Release.ProjectID = project.ID
	q.Release.Input.CredentialID = credential.ID
	w.cfg.Projects = map[string]Project{project.ID: {Kind: "container", Domain: "app.example", RuntimeID: q.Deployment.RuntimeID, Architecture: "arm64"}}
	w.containerCredentials = vault
	w.factory = func(string) (Deployer, error) { return mock, nil }
	return w, q, vault, credential.ID, cleanup
}

func TestContainerRuntimeRegistersCredentialBeforePreflightAndDeploy(t *testing.T) {
	mock := &credentialDeployer{containerDeployerMock: &containerDeployerMock{}}
	w, q, _, credentialID, cleanup := credentialRuntimeFixture(t, mock)
	defer cleanup()
	if err := containerRuntimeFor(w, q).SubmitContainerRuntime(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(mock.order, ","); got != "register,preflight,deploy" {
		t.Fatalf("call order=%q", got)
	}
	if mock.appName != appName(q.Deployment.ProjectID) || mock.revision != credentialID || mock.registryHost != "ghcr.io" || mock.credential.Username != "robot" || mock.credential.Password != "fleet-secret" || q.Release.Input.CredentialID != credentialID {
		t.Fatalf("credential registration app=%q revision=%q registry=%q credentials=%#v", mock.appName, mock.revision, mock.registryHost, mock.credential)
	}
	if mock.deploy != 1 {
		t.Fatalf("deploy calls=%d", mock.deploy)
	}
	if err := containerRuntimeFor(w, q).SubmitContainerRuntime(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	if len(mock.order) != 3 || mock.deploy != 1 {
		t.Fatalf("replay repeated calls order=%v deploy=%d", mock.order, mock.deploy)
	}
}

func TestContainerRuntimeCredentialFailurePreventsDeployAndPersistsRejection(t *testing.T) {
	mock := &credentialDeployer{containerDeployerMock: &containerDeployerMock{}, credErr: errors.New("registration failed")}
	w, q, _, _, cleanup := credentialRuntimeFixture(t, mock)
	defer cleanup()
	r := containerRuntimeFor(w, q)
	if err := r.SubmitContainerRuntime(t.Context(), q); err == nil {
		t.Fatal("credential registration failure accepted")
	}
	if mock.preflight != 0 || mock.deploy != 0 {
		t.Fatalf("deployer called after registration failure: preflight=%d deploy=%d", mock.preflight, mock.deploy)
	}
	data, err := os.ReadFile(r.statePath(q.Deployment.ID))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "fleet-secret") || !strings.Contains(string(data), "not_submitted") {
		t.Fatalf("durable rejection contains secret or wrong stage: %s", data)
	}
	if err = r.SubmitContainerRuntime(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	if len(mock.order) != 1 || mock.deploy != 0 {
		t.Fatalf("replay retried registration/deploy order=%v deploy=%d", mock.order, mock.deploy)
	}
}

func TestContainerRuntimeHealthRequiresMatchingCredentialID(t *testing.T) {
	mock := &credentialDeployer{containerDeployerMock: &containerDeployerMock{}}
	w, q, _, _, cleanup := credentialRuntimeFixture(t, mock)
	defer cleanup()
	r := containerRuntimeFor(w, q)
	if err := r.SubmitContainerRuntime(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	status := healthyContainerStatus(t, r.a, q.Release)
	status.App.DesiredState["image_pull_credential"] = "wrong-credential"
	mock.status = status
	observation, err := r.InspectContainerRuntime(t.Context(), q)
	if err != nil || observation.State != "pending" {
		t.Fatalf("wrong credential status: %+v %v", observation, err)
	}
	status.App.DesiredState["image_pull_credential"] = q.Release.Input.CredentialID
	mock.status = status
	observation, err = r.InspectContainerRuntime(t.Context(), q)
	if err != nil || observation.State != "succeeded" {
		t.Fatalf("matching credential status: %+v %v", observation, err)
	}
}
