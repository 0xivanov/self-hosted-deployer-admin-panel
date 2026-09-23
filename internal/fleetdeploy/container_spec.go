package fleetdeploy

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/client"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/portal"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/registryimage"
	"gopkg.in/yaml.v3"
)

// These types intentionally describe only the deployer document we own. Using
// yaml.Marshal here keeps release-controlled strings from becoming YAML syntax.
type containerSpec struct {
	EnvironmentRevision string              `yaml:"environmentRevision,omitempty"`
	ImagePullCredential string              `yaml:"imagePullCredential,omitempty"`
	Name                string              `yaml:"name"`
	Image               string              `yaml:"image"`
	Service             containerService    `yaml:"service"`
	Routing             containerRouting    `yaml:"routing"`
	Deploy              containerDeploy     `yaml:"deploy"`
	Placement           containerPlacement  `yaml:"placement"`
	State               containerState      `yaml:"state"`
	Resilience          containerResilience `yaml:"resilience"`
	Hosting             containerHosting    `yaml:"hosting"`
}

type containerService struct {
	Port   int             `yaml:"port"`
	Health containerHealth `yaml:"health"`
}
type containerHealth struct {
	Path string `yaml:"path"`
}
type containerRouting struct {
	Domain string `yaml:"domain"`
}
type containerDeploy struct {
	Replicas int    `yaml:"replicas"`
	Strategy string `yaml:"strategy"`
}
type containerPlacement struct {
	Arch   string `yaml:"arch"`
	Spread bool   `yaml:"spread"`
}
type containerState struct {
	Mode string `yaml:"mode"`
}
type containerResilience struct {
	Mode string `yaml:"mode"`
}
type containerHosting struct {
	Version     string             `yaml:"version"`
	MaxReplicas int                `yaml:"maxReplicas"`
	Resources   containerResources `yaml:"resources"`
}
type containerResources struct {
	Requests containerResourceSet `yaml:"requests"`
	Limits   containerResourceSet `yaml:"limits"`
}
type containerResourceSet struct {
	CPU              string `yaml:"cpu"`
	Memory           string `yaml:"memory"`
	EphemeralStorage string `yaml:"ephemeralStorage"`
}

func validContainerHealthPath(path string) bool {
	if path == "" || len(path) > 512 || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.ContainsAny(path, "?#\r\n\x00") {
		return false
	}
	u, err := url.ParseRequestURI(path)
	if err != nil || u.Host != "" || u.Scheme != "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	for _, c := range u.Path {
		if c < 32 || c == 127 {
			return false
		}
	}
	return true
}

func validContainerRelease(a assignment, release portal.ContainerRelease) bool {
	if !hexID(a.id) || a.p.Kind != "container" || release.ProjectID != a.id || !validDomain(a.p.Domain) {
		return false
	}
	if (release.Input.EnvironmentID != "" && !hexID(release.Input.EnvironmentID)) || (release.Input.CredentialID != "" && !hexID(release.Input.CredentialID)) || release.Input.Port < 1024 || release.Input.Port > 65535 || !validContainerHealthPath(release.Input.HealthPath) {
		return false
	}
	source, err := registryimage.Parse(release.Input.Reference)
	if err != nil || source.String() != release.Input.Reference || (source.Registry != "docker.io" && source.Registry != "ghcr.io") {
		return false
	}
	if release.Image.Source != release.Input.Reference || release.Image.OS != "linux" || release.Image.Architecture != "arm64" {
		return false
	}
	image, err := registryimage.Parse(release.Image.Image)
	if err != nil || image.String() != release.Image.Image || image.Registry != source.Registry || image.Repository != source.Repository || !strings.HasPrefix(image.Version, "sha256:") || release.Image.ManifestDigest != image.Version {
		return false
	}
	return true
}

func renderContainerYAML(a assignment, release portal.ContainerRelease) (string, error) {
	if !validContainerRelease(a, release) {
		return "", errors.New("invalid container release")
	}
	resources := containerResources{
		Requests: containerResourceSet{CPU: "50m", Memory: "64Mi", EphemeralStorage: "64Mi"},
		Limits:   containerResourceSet{CPU: "500m", Memory: "256Mi", EphemeralStorage: "256Mi"},
	}
	spec := containerSpec{
		EnvironmentRevision: release.Input.EnvironmentID,
		ImagePullCredential: release.Input.CredentialID,
		Name:                appName(a.id), Image: release.Image.Image,
		Service:   containerService{Port: release.Input.Port, Health: containerHealth{Path: release.Input.HealthPath}},
		Routing:   containerRouting{Domain: a.p.Domain},
		Deploy:    containerDeploy{Replicas: 2, Strategy: "rolling"},
		Placement: containerPlacement{Arch: "linux/arm64", Spread: true},
		State:     containerState{Mode: "stateless"}, Resilience: containerResilience{Mode: "resilient"},
		Hosting: containerHosting{Version: "v1", MaxReplicas: 2, Resources: resources},
	}
	b, err := yaml.Marshal(spec)
	if err != nil {
		return "", fmt.Errorf("marshal container deployer spec: %w", err)
	}
	return string(b), nil
}

func desiredMap(c map[string]any, key string) map[string]any {
	m, _ := c[key].(map[string]any)
	return m
}

func desiredString(c map[string]any, section, key string) string {
	s, _ := desiredMap(c, section)[key].(string)
	return s
}

func desiredInt(c map[string]any, section, key string) int {
	v := desiredMap(c, section)[key]
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}

func desiredBool(c map[string]any, section, key string) bool {
	b, _ := desiredMap(c, section)[key].(bool)
	return b
}

func desiredResource(c map[string]any, tier, key string) string {
	resources := desiredMap(c, "resources")
	values := desiredMap(resources, tier)
	s, _ := values[key].(string)
	return s
}

func containerDesiredMatches(desired map[string]any, a assignment, release portal.ContainerRelease) bool {
	credentialID, _ := desired["image_pull_credential"].(string)
	environmentID, _ := desired["environment_revision"].(string)
	if !validContainerRelease(a, release) || credentialID != release.Input.CredentialID || environmentID != release.Input.EnvironmentID ||
		desired["name"] != appName(a.id) ||
		desired["image"] != release.Image.Image ||
		desiredString(desired, "routing", "domain") != a.p.Domain ||
		desiredString(desired, "state", "mode") != "stateless" ||
		desiredInt(desired, "deploy", "replicas") != 2 ||
		desiredString(desired, "resilience", "mode") != "resilient" ||
		desiredString(desired, "placement", "arch") != "linux/arm64" ||
		!desiredBool(desired, "placement", "spread") ||
		desiredString(desired, "hosting", "version") != "v1" ||
		desiredInt(desired, "hosting", "maxReplicas") != 2 ||
		desiredInt(desired, "service", "port") != release.Input.Port ||
		desiredString(desiredMap(desired, "service"), "health", "path") != release.Input.HealthPath ||
		desiredResource(desiredMap(desired, "hosting"), "requests", "cpu") != "50m" ||
		desiredResource(desiredMap(desired, "hosting"), "requests", "memory") != "64Mi" ||
		desiredResource(desiredMap(desired, "hosting"), "requests", "ephemeralStorage") != "64Mi" ||
		desiredResource(desiredMap(desired, "hosting"), "limits", "cpu") != "500m" ||
		desiredResource(desiredMap(desired, "hosting"), "limits", "memory") != "256Mi" ||
		desiredResource(desiredMap(desired, "hosting"), "limits", "ephemeralStorage") != "256Mi" {
		return false
	}
	return true
}

func healthyContainer(s client.AppStatusResult, a assignment, release portal.ContainerRelease) bool {
	return s.LatestDeployment.Status == "healthy" && healthyContainerRuntime(s, a, release)
}

func healthyContainerRuntime(s client.AppStatusResult, a assignment, release portal.ContainerRelease) bool {
	if !containerDesiredMatches(map[string]any(s.App.DesiredState), a, release) || s.App.Name != appName(a.id) || s.App.Image != release.Image.Image || (s.App.Domain != "" && s.App.Domain != a.p.Domain) ||
		s.RuntimeStatus != "healthy" ||
		s.DesiredReplicas != 2 ||
		s.AvailableReplicas < 2 {
		return false
	}
	for _, route := range s.Routes {
		if route.Status == "healthy" && route.Domain == a.p.Domain && route.TargetPort == release.Input.Port && route.TLSEnabled {
			return true
		}
	}
	return false
}
