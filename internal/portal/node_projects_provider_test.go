package portal

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodebuild"
)

func TestNodeProjectProviderReloadsAndFailsClosed(t *testing.T) {
	project := strings.Repeat("a", 64)
	runtime := strings.Repeat("b", 64)
	path := t.TempDir() + "/assignments.json"
	assignment := map[string]NodeProjectConfig{project: {RuntimeID: runtime, Build: NodeBuildAssignment{ToolchainSHA256: strings.Repeat("c", 64), Settings: nodebuild.Settings{Architecture: "amd64"}}}}
	raw, _ := json.Marshal(assignment)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	p, err := NewNodeProjectProvider(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Snapshot()) != 1 {
		t.Fatal("initial assignment was not loaded")
	}
	if err := os.WriteFile(path, []byte(`{`), 0600); err != nil {
		t.Fatal(err)
	}
	if len(p.Snapshot()) != 0 {
		t.Fatal("invalid reload retained stale assignment")
	}
}
