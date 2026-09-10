//go:build integration

package noderuntimeapi

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodebuild"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/noderouter"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/npmfetch"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/portal"
)

type noDependencies struct{}

func (noDependencies) Fetch(context.Context, npmfetch.Tarball) ([]byte, error) {
	return nil, errors.New("unexpected dependency")
}

type submitFunc func(context.Context, portal.NodeExecutionRequest) error

func (f submitFunc) SubmitNodeExecution(ctx context.Context, r portal.NodeExecutionRequest) error {
	return f(ctx, r)
}

type artifactFunc func(context.Context, string) (portal.NodeArtifactObservation, []byte, error)

func (f artifactFunc) ReadNodeArtifact(ctx context.Context, id string) (portal.NodeArtifactObservation, []byte, error) {
	return f(ctx, id)
}

// This joins real portal persistence and authenticated TLS transport. Executor
// facts are explicit fixtures; this test does not claim to run a Node process.
func TestTLSObservationCompletesPortalDeployment(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	directory := t.TempDir()
	s, err := portal.Open(filepath.Join(directory, "db", "portal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a, verification, err := s.Register(ctx, "runtime-tls@example.test", "synthetic-runtime-password", "TLS workspace")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Verify(ctx, verification); err != nil {
		t.Fatal(err)
	}
	session, err := s.Login(ctx, a.Email, "synthetic-runtime-password")
	if err != nil {
		t.Fatal(err)
	}
	project, err := s.CreateProject(ctx, session.Token, a.WorkspaceID, "TLS site", "node")
	if err != nil {
		t.Fatal(err)
	}
	var source bytes.Buffer
	z := zip.NewWriter(&source)
	for _, f := range []struct{ name, body string }{{"package.json", `{"scripts":{"start":"node server.js"}}`}, {"package-lock.json", `{"lockfileVersion":3,"packages":{"":{}}}`}} {
		w, e := z.Create(f.name)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = w.Write([]byte(f.body)); e != nil {
			t.Fatal(e)
		}
	}
	if err = z.Close(); err != nil {
		t.Fatal(err)
	}
	upload, err := s.SaveUpload(ctx, session.Token, project.ID, source.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	pin := strings.Repeat("1", 64)
	if _, err = s.RequestNodeBuild(ctx, session.Token, project.ID, upload.ID, "runtime-tls-build-key", portal.NodeBuildAssignment{Settings: nodebuild.Settings{Architecture: "arm64"}, ToolchainSHA256: pin}); err != nil {
		t.Fatal(err)
	}
	bundles := filepath.Join(directory, "bundles")
	if err = os.Mkdir(bundles, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(bundles)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	prepared, err := s.PrepareNodeBuild(ctx, project.ID, pin, "arm64", root, noDependencies{})
	if err != nil || prepared == nil {
		t.Fatal(prepared, err)
	}
	c := prepared.Claim
	if err = s.DispatchNodeBuild(ctx, c.Job.ID, c.ExecutionID, c.Lease, root, submitFunc(func(context.Context, portal.NodeExecutionRequest) error { return nil })); err != nil {
		t.Fatal(err)
	}
	release, err := s.RetainNodeRelease(ctx, artifactFunc(func(context.Context, string) (portal.NodeArtifactObservation, []byte, error) {
		return portal.NodeArtifactObservation{NodeExecutionObservation: portal.NodeExecutionObservation{ExecutionID: c.ExecutionID, SourceSHA256: upload.SHA256, ToolchainSHA256: pin, Architecture: "arm64", Outcome: "succeeded", Retired: true, ObservedAt: time.Now()}, ArtifactSHA256: upload.SHA256, DependencyManifestSHA256: prepared.Bundle.ManifestSHA256}, source.Bytes(), nil
	}), project.ID, c.Job.ID)
	if err != nil || release == nil {
		t.Fatal(release, err)
	}
	j, err := s.RequestNodeDeployment(ctx, session.Token, project.ID, release.BuildID, "runtime-tls-deploy-key", runtimeID)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimNodeDeployment(ctx, project.ID, runtimeID, pin, "arm64")
	if err != nil {
		t.Fatal(err)
	}
	candidate := noderouter.Candidate{ProjectID: project.ID, RuntimeID: runtimeID, DeploymentID: j.ID, OperationID: claim.OperationID, Revision: j.Revision, ArtifactSHA256: j.ArtifactSHA256, Backend: "blue"}
	server := httptest.NewUnstartedServer(nil)
	handler, err := Handler(server.Listener.Addr().String(), project.ID, runtimeID, token, providerFunc(func(context.Context, string, string, string) (portal.NodeRuntimeObservation, error) {
		return portal.NodeRuntimeObservation{Routing: noderouter.State{Fence: candidate, Status: "active", Active: &candidate}, ToolchainSHA256: pin, Architecture: "arm64", Settled: true, Healthy: true}, nil
	}))
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	server.Config.Handler = handler
	server.StartTLS()
	defer server.Close()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	client, err := NewClient(server.URL, project.ID, runtimeID, token, roots)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if changed, err := s.ReconcileNodeDeployment(ctx, client, project.ID, j.ID); err != nil || !changed {
		t.Fatal(changed, err)
	}
	active, err := s.ActiveNodeDeployment(ctx, session.Token, project.ID)
	if err != nil || active == nil || active.ID != j.ID {
		t.Fatal(active, err)
	}
	if err = s.DeleteNodeRelease(ctx, session.Token, project.ID, release.BuildID); !errors.Is(err, portal.ErrRetained) {
		t.Fatal(err)
	}
}
