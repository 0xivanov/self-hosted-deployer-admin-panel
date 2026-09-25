package fleetdeploy

import (
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/client"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/portal"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadConfigRejectsUnsafeAssignments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fleet.json")
	raw := `{"database":"db","deployer_binary":"deployer","deployer_config":"cfg","state_directory":"state","image_builder":"builder","projects":{"not-hex":{"kind":"static","domain":"example.test"}}}`
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected invalid project id")
	}
	raw = `{"database":"db","deployer_binary":"deployer","deployer_config":"cfg","state_directory":"state","image_builder":"builder","projects":{"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef":{"kind":"static","domain":"bad domain"}}}`
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected invalid domain")
	}
}

func TestLoadConfigBillingModeDefaultsToTestAndAcceptsLive(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fleet.json")
	base := `{"database":"db","deployer_binary":"deployer","deployer_config":"cfg","state_directory":"state","image_builder":"builder","projects":{}}`
	if err := os.WriteFile(path, []byte(base), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil || cfg.BillingMode != "test" {
		t.Fatalf("default mode=%q err=%v", cfg.BillingMode, err)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(base, `"projects":{}`, `"billing_mode":"live","projects":{}`, 1)), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err = LoadConfig(path)
	if err != nil || cfg.BillingMode != "live" {
		t.Fatalf("live mode=%q err=%v", cfg.BillingMode, err)
	}
}

func TestLoadConfigRejectsUnknownBillingMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fleet.json")
	raw := `{"database":"db","deployer_binary":"deployer","deployer_config":"cfg","state_directory":"state","image_builder":"builder","billing_mode":"production","projects":{}}`
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("accepted unknown billing mode")
	}
}
func TestImmutableImageAndRenderContract(t *testing.T) {
	id := strings.Repeat("a", 64)
	if !validImage("registry.example/app@sha256:" + id) {
		t.Fatal("expected valid image")
	}
	if validImage("registry.example/app:latest") || validImage("registry.example/app@sha256:"+strings.Repeat("z", 64)) {
		t.Fatal("accepted invalid image")
	}
	y := renderYAML(assignment{id: id, p: Project{Kind: "static", Domain: "site.example"}}, "registry.example/app@sha256:"+id)
	for _, want := range []string{"port: 8080", "path: /", "replicas: 2", "mode: resilient", "arch: linux/arm64", "spread: true"} {
		if !strings.Contains(y, want) {
			t.Errorf("render missing %q", want)
		}
	}
}
func TestHealthyRequiresExactCoreRouteAndTwoReplicas(t *testing.T) {
	s := client.AppStatusResult{App: client.AppInfo{Image: "repo/app@sha256:" + strings.Repeat("a", 64)}, LatestDeployment: client.DeploymentInfo{Status: "healthy"}, RuntimeStatus: "healthy", DesiredReplicas: 2, AvailableReplicas: 2, Routes: []client.RouteInfo{{Domain: "site.example", Status: "healthy"}}}
	if !healthy(s, s.App.Image, "site.example") {
		t.Fatal("expected healthy")
	}
	s.Routes[0].Domain = "other.example"
	if healthy(s, s.App.Image, "site.example") {
		t.Fatal("accepted wrong route")
	}
}

func TestPreparationFailureSurvivesRestartWithoutActivation(t *testing.T) {
	state := t.TempDir()
	dbdir := t.TempDir()
	if err := os.Chmod(dbdir, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := portal.Open(filepath.Join(dbdir, "portal.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project := strings.Repeat("a", 64)
	runtimeID := strings.Repeat("b", 64)
	pin := strings.Repeat("c", 64)
	w := &Worker{store: store, cfg: Config{StateDirectory: state, ImageBuilder: "/does-not-exist"}}
	w.factory = func(string) (Deployer, error) { t.Fatal("preparation failure attempted core access"); return nil, nil }
	a := assignment{id: project, p: Project{Kind: "node", Domain: "site.example", RuntimeID: runtimeID, ToolchainSHA256: pin, Architecture: "arm64"}}
	q := portal.NodeRuntimeRequest{ProjectID: project, RuntimeID: runtimeID, ToolchainSHA256: pin, Architecture: "arm64", OperationID: strings.Repeat("d", 64), DeploymentID: strings.Repeat("e", 64), ArtifactSHA256: strings.Repeat("f", 64), Revision: 1, ActivateBefore: time.Now().Add(time.Minute).Unix(), Archive: []byte("source")}
	first := &runtime{w: w, a: a}
	if first.SubmitNodeRuntime(t.Context(), q) == nil {
		t.Fatal("expected image preparation error")
	}
	restored := &runtime{w: w, a: a}
	o, err := restored.InspectNodeRuntime(t.Context(), runtimeID, project, q.OperationID)
	if err != nil || !o.Settled || !o.CandidateStopped || o.Routing.Status != "failed" || o.Routing.Active != nil {
		t.Fatalf("failure did not reconcile: %+v %v", o, err)
	}
	if err = restored.SubmitNodeRuntime(t.Context(), q); err != nil {
		t.Fatalf("exact replay: %v", err)
	}
	q.ArtifactSHA256 = strings.Repeat("9", 64)
	if restored.SubmitNodeRuntime(t.Context(), q) == nil {
		t.Fatal("conflicting replay accepted")
	}
}
