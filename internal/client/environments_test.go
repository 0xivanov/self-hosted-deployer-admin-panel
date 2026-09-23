package client

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateEnvironmentPrivateFileAndSanitizedFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			dir := t.TempDir()
			exe := filepath.Join(dir, "deployer")
			capture := filepath.Join(dir, "capture")
			script := "#!/bin/sh\nprintf '%s\\n' \"$*\" > \"$CAPTURE.args\"\nwhile [ \"$1\" != \"--values\" ]; do shift; done\nshift\ncp \"$1\" \"$CAPTURE.file\"\nstat -f '%Lp' \"$1\" > \"$CAPTURE.mode\" 2>/dev/null || stat -c '%a' \"$1\" > \"$CAPTURE.mode\"\n"
			if fail {
				script += "printf 'synthetic-secret' >&2\nexit 1\n"
			} else {
				script += "printf '%s' '{\"app_name\":\"my-api\",\"revision\":\"" + strings.Repeat("a", 64) + "\",\"names\":[\"EMPTY\",\"TOKEN\"]}'\n"
			}
			if err := os.WriteFile(exe, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("CAPTURE", capture)
			c := &CLI{executable: exe, directory: dir, config: filepath.Join(dir, "config.json")}
			err := c.CreateEnvironment(context.Background(), "my-api", strings.Repeat("a", 64), map[string]string{"TOKEN": "synthetic-secret", "EMPTY": ""})
			if (err != nil) != fail {
				t.Fatal("unexpected operation result")
			}
			if err != nil && strings.Contains(err.Error(), "synthetic-secret") {
				t.Fatal("secret in error")
			}
			args, _ := os.ReadFile(capture + ".args")
			if strings.Contains(string(args), "synthetic-secret") {
				t.Fatal("secret in arguments")
			}
			mode, _ := os.ReadFile(capture + ".mode")
			if strings.TrimSpace(string(mode)) != "600" {
				t.Fatal("unsafe temporary file")
			}
			raw, _ := os.ReadFile(capture + ".file")
			var values map[string]string
			if json.Unmarshal(raw, &values) != nil || values["TOKEN"] != "synthetic-secret" || len(values) != 2 {
				t.Fatal("incorrect values payload")
			}
			files, _ := filepath.Glob(filepath.Join(dir, ".environment-*"))
			if len(files) != 0 {
				t.Fatal("temporary file retained")
			}
		})
	}
}

func TestEnvironmentValuesBounds(t *testing.T) {
	for _, values := range []map[string]string{
		{"BAD-NAME": "x"}, {"TOKEN": "x\x00y"}, {"TOKEN": string([]byte{255})}, {"TOKEN": strings.Repeat("a", 8193)},
		{"A": strings.Repeat("a", 8192), "B": strings.Repeat("a", 8192), "C": strings.Repeat("a", 8192), "D": strings.Repeat("a", 8192)},
	} {
		if validEnvironmentValues(values) {
			t.Fatal("invalid environment accepted")
		}
	}
	if !validEnvironmentValues(map[string]string{"TOKEN": "first\nsecond", "EMPTY": "", "__proto__": "valid"}) || !validEnvironmentValues(nil) {
		t.Fatal("valid environment rejected")
	}
}
