//go:build integration

package noderuntimeapi

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/portal"
)

type runtimeSubmitFunc func(context.Context, portal.NodeRuntimeRequest) error

func (f runtimeSubmitFunc) SubmitNodeRuntime(ctx context.Context, r portal.NodeRuntimeRequest) error {
	return f(ctx, r)
}
func TestDeploymentHTTPSSubmissionAndScope(t *testing.T) {
	t.Parallel()
	var archive bytes.Buffer
	z := zip.NewWriter(&archive)
	f, _ := z.Create("package.json")
	f.Write([]byte(`{"scripts":{"start":"node app.js"}}`))
	f, _ = z.Create("app.js")
	f.Write([]byte("console.log('fixture')"))
	z.Close()
	digest := sha256.Sum256(archive.Bytes())
	request := portal.NodeRuntimeRequest{OperationID: operationID, DeploymentID: strings.Repeat("e", 64), ProjectID: projectID, RuntimeID: runtimeID, ReleaseID: strings.Repeat("f", 64), ArtifactSHA256: hex.EncodeToString(digest[:]), ToolchainSHA256: strings.Repeat("1", 64), Architecture: "arm64", Revision: 1, ActivateBefore: time.Now().Add(time.Minute).Unix(), Archive: archive.Bytes()}
	server := httptest.NewUnstartedServer(nil)
	defer server.Close()
	calls := 0
	handler, err := DeploymentHandler(server.Listener.Addr().String(), projectID, runtimeID, token, providerFunc(func(context.Context, string, string, string) (portal.NodeRuntimeObservation, error) {
		return observation(), nil
	}), runtimeSubmitFunc(func(ctx context.Context, r portal.NodeRuntimeRequest) error {
		calls++
		if !bytes.Equal(r.Archive, request.Archive) || r.OperationID != operationID || r.ArtifactSHA256 != request.ArtifactSHA256 {
			t.Error("submission changed")
		}
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = handler
	server.StartTLS()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	client, err := NewClient(server.URL, projectID, runtimeID, token, roots)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err = client.SubmitNodeRuntime(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	wrong := request
	wrong.RuntimeID = strings.Repeat("0", 64)
	if err = client.SubmitNodeRuntime(t.Context(), wrong); !errors.Is(err, ErrAssignment) {
		t.Fatal(err)
	}
	wrong = request
	wrong.ActivateBefore = time.Now().Add(-time.Minute).Unix()
	if err = client.SubmitNodeRuntime(t.Context(), wrong); !errors.Is(err, ErrAssignment) {
		t.Fatal(err)
	}
	badAuth, err := NewClient(server.URL, projectID, runtimeID, strings.Repeat("2", 64), roots)
	if err != nil {
		t.Fatal(err)
	}
	defer badAuth.Close()
	if err = badAuth.SubmitNodeRuntime(t.Context(), request); !errors.Is(err, ErrSubmission) {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal(calls)
	}
}
