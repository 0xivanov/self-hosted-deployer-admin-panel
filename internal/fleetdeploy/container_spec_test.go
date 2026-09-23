package fleetdeploy

import (
	"strings"
	"testing"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/appconfig"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/client"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/portal"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/registryimage"
	"gopkg.in/yaml.v3"
)

func testContainerRelease(id string) portal.ContainerRelease {
	digest := "sha256:" + strings.Repeat("a", 64)
	return portal.ContainerRelease{
		ProjectID: id,
		Input:     portal.ContainerReleaseInput{Reference: "ghcr.io/acme/demo:stable", Port: 8088, HealthPath: "/healthz"},
		Image:     registryimage.Candidate{Source: "ghcr.io/acme/demo:stable", Image: "ghcr.io/acme/demo@" + digest, ManifestDigest: digest, OS: "linux", Architecture: "arm64"},
	}
}

func TestRenderContainerYAMLIsTypedAndSettingsAware(t *testing.T) {
	id := strings.Repeat("b", 64)
	a := assignment{id: id, p: Project{Kind: "container", Domain: "app.example"}}
	raw, err := renderContainerYAML(a, testContainerRelease(id))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := yaml.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	if got["image"] != "ghcr.io/acme/demo@sha256:"+strings.Repeat("a", 64) {
		t.Fatalf("unexpected image: %#v", got["image"])
	}
	service := got["service"].(map[string]any)
	if service["port"] != 8088 || service["health"].(map[string]any)["path"] != "/healthz" {
		t.Fatalf("settings not rendered: %#v", service)
	}
	for _, want := range []string{"replicas: 2", "mode: stateless", "mode: resilient", "arch: linux/arm64", "ephemeralStorage: 256Mi"} {
		if !strings.Contains(raw, want) {
			t.Errorf("render missing %q", want)
		}
	}
}

func TestRenderContainerYAMLRejectsIdentityAndUnsafeSettings(t *testing.T) {
	id := strings.Repeat("c", 64)
	a := assignment{id: id, p: Project{Kind: "container", Domain: "app.example"}}
	bad := []func(*portal.ContainerRelease){
		func(r *portal.ContainerRelease) { r.ProjectID = strings.Repeat("d", 64) },
		func(r *portal.ContainerRelease) { r.Input.Port = 80 },
		func(r *portal.ContainerRelease) { r.Input.HealthPath = "/health?x=1" },
		func(r *portal.ContainerRelease) { r.Image.Image = "ghcr.io/acme/demo:latest" },
	}
	for i, mutate := range bad {
		r := testContainerRelease(id)
		mutate(&r)
		if _, err := renderContainerYAML(a, r); err == nil {
			t.Errorf("case %d accepted invalid release", i)
		}
	}
	a.p.Kind = "node"
	if _, err := renderContainerYAML(a, testContainerRelease(id)); err == nil {
		t.Fatal("accepted non-container assignment")
	}
}

func TestHealthyContainerRequiresRoutePort(t *testing.T) {
	id := strings.Repeat("e", 64)
	a := assignment{id: id, p: Project{Kind: "container", Domain: "app.example"}}
	r := testContainerRelease(id)
	raw, err := renderContainerYAML(a, r)
	if err != nil {
		t.Fatal(err)
	}
	var desired map[string]any
	if err := yaml.Unmarshal([]byte(raw), &desired); err != nil {
		t.Fatal(err)
	}
	s := client.AppStatusResult{App: client.AppInfo{Name: appName(id), Image: r.Image.Image, DesiredState: appconfig.Config(desired)}, LatestDeployment: client.DeploymentInfo{Status: "healthy"}, RuntimeStatus: "healthy", DesiredReplicas: 2, AvailableReplicas: 2, Routes: []client.RouteInfo{{Domain: a.p.Domain, TargetPort: r.Input.Port, Status: "healthy", TLSEnabled: true}}}
	if !healthyContainer(s, a, r) {
		t.Fatal("expected healthy container")
	}
	health := s.App.DesiredState["service"].(map[string]any)["health"].(map[string]any)
	health["path"] = "/other"
	if healthyContainer(s, a, r) {
		t.Fatal("accepted different desired health path")
	}
	health["path"] = r.Input.HealthPath
	s.Routes[0].TargetPort++
	if healthyContainer(s, a, r) {
		t.Fatal("accepted wrong target port")
	}
}
