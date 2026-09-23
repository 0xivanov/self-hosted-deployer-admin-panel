package client

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/registryimage"
)

func TestCreateRegistryCredentialUsesPrivateTemporaryFileAndRemovesIt(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "deployer")
	capture := filepath.Join(dir, "capture")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" > \"$CAPTURE.args\"\n" +
		"while [ \"$1\" != \"--credentials\" ]; do shift; done\n" +
		"shift\n" +
		"cp \"$1\" \"$CAPTURE.file\"\n" +
		"stat -f '%Lp' \"$CAPTURE.file\" > \"$CAPTURE.mode\" 2>/dev/null || stat -c '%a' \"$CAPTURE.file\" > \"$CAPTURE.mode\"\n" +
		"printf '%s' '{\"app_name\":\"my-api\",\"revision\":\"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\",\"registry\":\"ghcr.io\"}'\n"
	if err := os.WriteFile(exe, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAPTURE", capture)
	c := &CLI{executable: exe, directory: dir, config: filepath.Join(dir, "config.json")}
	password := "private-token"
	if err := c.CreateRegistryCredential(context.Background(), "my-api", strings.Repeat("a", 64), "ghcr.io", registryimage.Credentials{Username: "owner", Password: password}); err != nil {
		t.Fatalf("create credential: %v", err)
	}
	data, err := os.ReadFile(capture + ".file")
	if err != nil || string(data) != `{"username":"owner","password":"private-token"}` {
		t.Fatalf("unexpected captured credential file: %q, %v", data, err)
	}
	mode, err := os.ReadFile(capture + ".mode")
	if err != nil || strings.TrimSpace(string(mode)) != "600" {
		t.Fatalf("temporary credential file mode: %q, %v", mode, err)
	}
	args, _ := os.ReadFile(capture + ".args")
	if strings.Contains(string(args), password) {
		t.Fatal("password appeared in process arguments")
	}
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".registry-credentials-") {
			t.Fatalf("temporary credential file was not removed: %s", entry.Name())
		}
	}
}

func TestCreateRegistryCredentialRejectsInvalidInputBeforeRunningCLI(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "called")
	exe := filepath.Join(dir, "deployer")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\ntouch \"$MARKER\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MARKER", marker)
	c := &CLI{executable: exe, directory: dir}
	tests := []registryimage.Credentials{
		{Username: "bad:user", Password: "p"},
		{Username: "u", Password: "line\nfeed"},
		{Username: "u", Password: ""},
	}
	for _, creds := range tests {
		if err := c.CreateRegistryCredential(context.Background(), "my-api", "bad", "docker.io", creds); err == nil {
			t.Fatal("accepted invalid credential input")
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("invalid input invoked CLI")
	}
}

func TestCreateRegistryCredentialRejectsWrongMetadataWithoutSecretError(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "deployer")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\nprintf '%s' '{\"app_name\":\"other\",\"revision\":\"bad\",\"registry\":\"ghcr.io\"}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	c := &CLI{executable: exe, directory: dir}
	err := c.CreateRegistryCredential(context.Background(), "my-api", strings.Repeat("a", 64), "ghcr.io", registryimage.Credentials{Username: "u", Password: "sensitive"})
	if err == nil || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("wrong metadata error leaked credential or was absent: %v", err)
	}
}
