package fleetdeploy

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/portal"
)

type environmentDeployer struct {
	*credentialDeployer
	environmentErr error
	environmentID  string
}

func (d *environmentDeployer) CreateEnvironment(_ context.Context, app, id string, values map[string]string) error {
	d.order = append(d.order, "environment")
	if app != d.appName || values["TOKEN"] != "synthetic-env-secret" {
		return errors.New("incorrect environment target or values")
	}
	d.environmentID = id
	return d.environmentErr
}
func TestContainerRuntimeEnvironmentRegistrationAndHealth(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{true: "rejected", false: "accepted"}[failure], func(t *testing.T) {
			mock := &environmentDeployer{credentialDeployer: &credentialDeployer{containerDeployerMock: &containerDeployerMock{}}}
			if failure {
				mock.environmentErr = errors.New("synthetic-env-secret")
			}
			w, q, _, _, cleanup := credentialRuntimeFixture(t, mock.credentialDeployer)
			defer cleanup()
			vault, err := portal.NewContainerEnvironments(w.store, []byte(strings.Repeat("e", 32)))
			if err != nil {
				t.Fatal(err)
			}
			session, err := w.store.Login(t.Context(), "fleet-credentials@example.test", "a long synthetic password")
			if err != nil {
				t.Fatal(err)
			}
			environment, err := vault.Create(t.Context(), session.Token, q.Deployment.ProjectID, "fleet-environment-key", "Production", map[string]string{"TOKEN": "synthetic-env-secret"})
			if err != nil {
				t.Fatal(err)
			}
			q.Release.Input.EnvironmentID = environment.ID
			w.containerEnvironments = vault
			w.factory = func(string) (Deployer, error) { return mock, nil }
			r := containerRuntimeFor(w, q)
			err = r.SubmitContainerRuntime(t.Context(), q)
			if (err != nil) != failure {
				t.Fatal("unexpected submission result")
			}
			if err != nil && strings.Contains(err.Error(), "synthetic-env-secret") {
				t.Fatal("secret exposed in submission error")
			}
			raw, err := os.ReadFile(r.statePath(q.Deployment.ID))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "synthetic-env-secret") {
				t.Fatal("secret persisted in operation")
			}
			if failure {
				if mock.deploy != 0 || mock.preflight != 0 || !strings.Contains(string(raw), "not_submitted") {
					t.Fatal("registration failure reached deployment")
				}
				return
			}
			if strings.Join(mock.order, ",") != "register,environment,preflight,deploy" || mock.environmentID != environment.ID {
				t.Fatal("incorrect registration ordering")
			}
			status := healthyContainerStatus(t, r.a, q.Release)
			status.App.DesiredState["image_pull_credential"] = q.Release.Input.CredentialID
			status.App.DesiredState["environment_revision"] = "wrong"
			if healthyContainer(status, r.a, q.Release) {
				t.Fatal("wrong environment marked healthy")
			}
			status.App.DesiredState["environment_revision"] = environment.ID
			if !healthyContainer(status, r.a, q.Release) {
				t.Fatal("matching environment not healthy")
			}
			if err := r.SubmitContainerRuntime(t.Context(), q); err != nil || mock.deploy != 1 || len(mock.order) != 4 {
				t.Fatal("replay performed mutations")
			}
		})
	}
}
