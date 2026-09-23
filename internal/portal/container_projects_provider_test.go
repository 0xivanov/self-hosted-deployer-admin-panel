package portal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestContainerProjectProviderRevokesInvalidReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "assignments.json")
	project := strings.Repeat("a", 64)
	runtime := strings.Repeat("b", 64)
	raw, _ := json.Marshal(map[string]ContainerProjectConfig{project: {RuntimeID: runtime}})
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	p, err := NewContainerProjectProvider(path)
	if err != nil {
		t.Fatal(err)
	}
	if p.Snapshot()[project].RuntimeID != runtime {
		t.Fatal("assignment missing")
	}
	if err = os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if len(p.Snapshot()) != 0 {
		t.Fatal("invalid reload kept stale assignment")
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if len(p.Snapshot()) != 0 {
		t.Fatal("public mapping accepted")
	}
}
func TestContainerProjectAssignmentRejectsSharedRuntime(t *testing.T) {
	runtime := strings.Repeat("c", 64)
	if _, err := copyContainerProjects(map[string]ContainerProjectConfig{strings.Repeat("a", 64): {RuntimeID: runtime}, strings.Repeat("b", 64): {RuntimeID: runtime}}); err == nil {
		t.Fatal("shared runtime accepted")
	}
}
