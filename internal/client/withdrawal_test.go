package client

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeployWithdrawalCLIOptsInAndPreservesReceipt(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "deployer")
	capture := filepath.Join(dir, "args")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" > \"$WITHDRAWAL_ARGS\"\nprintf '%s' '{\"app\":{\"id\":\"a\",\"name\":\"my-api\"},\"deployment\":{\"id\":\"d\",\"app_id\":\"a\",\"status\":\"failed\"},\"withdrawal_confirmed\":true,\"requested_state\":{\"name\":\"my-api\"}}'\n"
	if err := os.WriteFile(exe, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WITHDRAWAL_ARGS", capture)
	c := &CLI{executable: exe, directory: dir, config: filepath.Join(dir, "config.json")}
	result, err := c.DeployAppReportingWithdrawal(t.Context(), "name: my-api\n")
	if err != nil || !result.WithdrawalConfirmed || result.Deployment.Status != "failed" || string(result.RequestedState) != `{"name":"my-api"}` {
		t.Fatal("withdrawal receipt lost")
	}
	args, err := os.ReadFile(capture)
	if err != nil || !strings.Contains(string(args), "deploy --report-withdrawal --file ") {
		t.Fatal("withdrawal flag missing")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "app-*.yaml"))
	if len(files) != 0 {
		t.Fatal("temporary config retained")
	}
}

func TestTrackedDeploymentCLIUsesStableIDAndReadOnlyLookup(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "deployer")
	capture := filepath.Join(dir, "args")
	id := strings.Repeat("a", 64)
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$TRACKED_ARGS\"\nprintf '%s' '{\"app_name\":\"my-api\",\"request_id\":\"" + id + "\",\"state\":\"pending\",\"requested_state\":{\"name\":\"my-api\"}}'\n"
	if err := os.WriteFile(exe, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TRACKED_ARGS", capture)
	c := &CLI{executable: exe, directory: dir, config: filepath.Join(dir, "config.json")}
	if _, err := c.DeployAppTracked(t.Context(), "name: my-api\n", id); err != nil {
		t.Fatal(err)
	}
	record, err := c.GetDeployRequest(t.Context(), "my-api", id)
	if err != nil || record.RequestID != id || record.State != "pending" {
		t.Fatal("request metadata lost")
	}
	raw, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	args := string(raw)
	if !strings.Contains(args, "deploy --request-id "+id+" --report-withdrawal --file ") || !strings.Contains(args, "apps request my-api "+id) {
		t.Fatal("wrong tracked CLI commands")
	}
}

func TestAdvanceAndRecoverDeployRequestCommands(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "deployer")
	capture := filepath.Join(dir, "args")
	id := strings.Repeat("b", 64)
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$RECOVERY_ARGS\"\nprintf '%s' '{\"app_name\":\"my-api\",\"request_id\":\"" + id + "\",\"state\":\"pending\",\"requested_state\":{\"name\":\"my-api\"}}'\n"
	if err := os.WriteFile(exe, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RECOVERY_ARGS", capture)
	c := &CLI{executable: exe, directory: dir, config: filepath.Join(dir, "config.json")}
	advanced, err := c.AdvanceDeployRequest(t.Context(), "my-api", id)
	if err != nil || advanced.AppName != "my-api" || advanced.RequestID != id || advanced.State != "pending" {
		t.Fatalf("advance response: %#v err=%v", advanced, err)
	}
	recovered, err := c.RecoverDeployRequest(t.Context(), "my-api", id)
	if err != nil || recovered.AppName != "my-api" || recovered.RequestID != id || recovered.State != "pending" {
		t.Fatalf("recover response: %#v err=%v", recovered, err)
	}
	if _, err := c.WithdrawDeployRequest(t.Context(), "my-api", "name: my-api\n", id); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	args := string(raw)
	if !strings.Contains(args, "apps withdraw my-api "+id+" --file ") {
		t.Fatal("missing withdrawal command")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "app-*.yaml"))
	if len(files) != 0 {
		t.Fatal("original configuration temporary file retained")
	}
	if !strings.Contains(args, "apps advance my-api "+id) || !strings.Contains(args, "apps recover my-api "+id) {
		t.Fatalf("wrong recovery commands: %q", args)
	}
}
