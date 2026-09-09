package client

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFrozenPrivateContextAndCleanup(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "deployer")
	// A known test executable returns only a synthetic server identity.
	if err := os.WriteFile(exe, []byte("#!/bin/sh\nprintf '%s\\n' '{\"server_identity\":\"installation-a\",\"ready\":true}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "source.json")
	data := `{"contexts":{"a":{"server_url":"https://a.example:7443","credential_ref":"env:PANEL_TEST_TOKEN"}},"current_context":"a"}`
	t.Setenv("PANEL_TEST_TOKEN", "synthetic-private-token")
	if err := os.WriteFile(source, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := New(exe, source, "")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	privatePath := c.config
	raw, err := os.ReadFile(privatePath)
	if err != nil {
		t.Fatal(err)
	}
	var frozen savedConfig
	if err = json.Unmarshal(raw, &frozen); err != nil {
		t.Fatal(err)
	}
	if frozen.Contexts["panel"].AdminToken != "synthetic-private-token" || frozen.Contexts["panel"].Identity != "installation-a" {
		t.Fatal("missing frozen credentials or identity")
	}
	info, _ := os.Stat(privatePath)
	if info.Mode().Perm() != 0600 {
		t.Fatal("config not private")
	}
	if err = os.WriteFile(source, []byte(`{"server_url":"https://other.example"}`), 0600); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(privatePath)
	if string(raw) != string(after) {
		t.Fatal("source change altered running context")
	}
	if _, err = c.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(privatePath); !os.IsNotExist(err) {
		t.Fatal("private snapshot retained after close")
	}
}
func TestRejectPublicCredentialFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := privateRead(p); err == nil {
		t.Fatal("accepted readable credential file")
	}
}
func TestBoundedOutput(t *testing.T) {
	b := limitedBuffer{limit: 16}
	if _, err := b.Write([]byte(strings.Repeat("x", 17))); err == nil {
		t.Fatal("unbounded output")
	}
}

func TestNodeRemovalStopsWhenDrainFails(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "fake-cli")
	log := filepath.Join(dir, "calls")
	t.Setenv("PANEL_NODE_TEST_LOG", log)
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$PANEL_NODE_TEST_LOG\"\ncase \"$*\" in *'nodes drain'*) exit 1;; esac\nprintf '{}'\n"
	if err := os.WriteFile(exe, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	c := &CLI{executable: exe, config: filepath.Join(dir, "config")}
	if err := c.ChangeNode(context.Background(), "worker-id", "remove"); err == nil {
		t.Fatal("drain failure ignored")
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(calls), "nodes remove") {
		t.Fatal("removed after failed drain")
	}
	if err := c.ChangeNode(context.Background(), "worker-id", "purge"); err != nil {
		t.Fatal(err)
	}
	calls, _ = os.ReadFile(log)
	if !strings.Contains(string(calls), "nodes purge --yes worker-id") {
		t.Fatal("purge not dispatched")
	}
}
