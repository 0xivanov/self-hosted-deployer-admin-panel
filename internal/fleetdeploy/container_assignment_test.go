package fleetdeploy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestContainerAssignmentRequiresExplicitEnablement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	id := strings.Repeat("a", 64)
	c := Config{Database: "db", DeployerBinary: "deployer", DeployerConfig: "config", StateDirectory: "state", ImageBuilder: "builder", Projects: map[string]Project{id: {Kind: "container", Domain: "site.example", RuntimeID: strings.Repeat("b", 64), Architecture: "arm64"}}}
	write := func() {
		t.Helper()
		raw, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("container enabled by default")
	}
	c.EnableContainerDeployments = true
	write()
	if _, err := LoadConfig(path); err != nil {
		t.Fatal(err)
	}
	p := c.Projects[id]
	p.Architecture = "amd64"
	c.Projects[id] = p
	write()
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("unsupported architecture accepted")
	}
}
