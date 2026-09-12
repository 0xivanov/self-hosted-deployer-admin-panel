//go:build integration

package nodebuildapi

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodebuild"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/npmfetch"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/portal"
)

type fixtureExecutor struct {
	mu      sync.Mutex
	request portal.NodeExecutionRequest
	submits int
	stale   bool
}

func (f *fixtureExecutor) SubmitNodeExecution(_ context.Context, r portal.NodeExecutionRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.request = r
	f.submits++
	return nil
}
func (f *fixtureExecutor) observation() portal.NodeExecutionObservation {
	now := time.Now()
	if f.stale {
		now = now.Add(-time.Minute)
	}
	r := f.request
	return portal.NodeExecutionObservation{ProjectID: r.ProjectID, ExecutionID: r.ExecutionID, SourceSHA256: r.Plan.SourceSHA256, ToolchainSHA256: r.ToolchainSHA256, Architecture: r.Plan.Architecture, Outcome: "succeeded", Retired: true, ObservedAt: now}
}
func (f *fixtureExecutor) InspectNodeExecution(context.Context, string) (portal.NodeExecutionObservation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.observation(), nil
}
func (f *fixtureExecutor) ReadNodeArtifact(context.Context, string) (portal.NodeArtifactObservation, []byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return portal.NodeArtifactObservation{NodeExecutionObservation: f.observation(), ArtifactSHA256: f.request.Plan.SourceSHA256, DependencyManifestSHA256: f.request.Bundle.ManifestSHA256}, f.request.Archive, nil
}

func TestBuildTransportRoundTripAndRejection(t *testing.T) {
	t.Parallel()
	project, pin, token := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	provider := &fixtureExecutor{}
	server := httptest.NewUnstartedServer(nil)
	h, err := Handler(server.Listener.Addr().String(), project, pin, "arm64", token, provider)
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = h
	server.StartTLS()
	defer server.Close()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	client, err := NewClient(server.URL, project, pin, "arm64", token, roots)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var archive bytes.Buffer
	z := zip.NewWriter(&archive)
	for name, body := range map[string]string{"package.json": `{"scripts":{"start":"node server.js"}}`, "package-lock.json": `{"lockfileVersion":3,"packages":{}}`, "server.js": `console.log('fixture')`} {
		w, e := z.Create(name)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = w.Write([]byte(body)); e != nil {
			t.Fatal(e)
		}
	}
	if err = z.Close(); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(archive.Bytes())
	sha := hex.EncodeToString(digest[:])
	plan, err := nodebuild.Prepare(t.Context(), archive.Bytes(), sha, nodebuild.Settings{Architecture: "arm64"})
	if err != nil {
		t.Fatal(err)
	}
	request := portal.NodeExecutionRequest{ExecutionID: strings.Repeat("d", 64), BuildID: strings.Repeat("e", 64), ProjectID: project, ToolchainSHA256: pin, Plan: plan, Bundle: npmfetch.Bundle{Directory: "dependencies-" + strings.Repeat("f", 64), ManifestSHA256: strings.Repeat("1", 64)}, NotAfter: time.Now().Add(50 * time.Second).Unix(), Archive: archive.Bytes()}
	if err = client.SubmitNodeExecution(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	observation, err := client.InspectNodeExecution(t.Context(), request.ExecutionID)
	if err != nil || observation.ExecutionID != request.ExecutionID || observation.ObservedAt.Before(started) {
		t.Fatal(observation, err)
	}
	artifact, data, err := client.ReadNodeArtifact(t.Context(), request.ExecutionID)
	if err != nil || !bytes.Equal(data, request.Archive) || artifact.DependencyManifestSHA256 != request.Bundle.ManifestSHA256 {
		t.Fatal(artifact, err)
	}
	wrong, err := NewClient(server.URL, project, pin, "arm64", strings.Repeat("9", 64), roots)
	if err != nil {
		t.Fatal(err)
	}
	defer wrong.Close()
	if err = wrong.SubmitNodeExecution(t.Context(), request); err == nil {
		t.Fatal("wrong credential accepted")
	}
	invalid := request
	invalid.NotAfter = time.Now().Add(2 * time.Minute).Unix()
	if err = client.SubmitNodeExecution(t.Context(), invalid); err == nil {
		t.Fatal("unbounded deadline accepted")
	}
	provider.mu.Lock()
	provider.request.ProjectID = strings.Repeat("8", 64)
	provider.mu.Unlock()
	if _, err = client.InspectNodeExecution(t.Context(), request.ExecutionID); err == nil {
		t.Fatal("foreign project observation accepted")
	}
	provider.mu.Lock()
	provider.request.ProjectID = project
	provider.stale = true
	count := provider.submits
	provider.mu.Unlock()
	if count != 1 {
		t.Fatal("unexpected submission count", count)
	}
	if _, err = client.InspectNodeExecution(t.Context(), request.ExecutionID); err == nil {
		t.Fatal("stale status accepted")
	}
	if _, _, err = client.ReadNodeArtifact(t.Context(), request.ExecutionID); err == nil {
		t.Fatal("stale artifact accepted")
	}
	req, _ := http.NewRequest("GET", server.URL+"/v1/executions/"+request.ExecutionID, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Project-ID", project)
	req.Header.Set("X-Toolchain-SHA256", pin)
	req.Header.Set("X-Architecture", "arm64")
	req.Header.Set("Origin", "https://customer.example.test")
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal("browser request accepted", response.StatusCode)
	}
}
