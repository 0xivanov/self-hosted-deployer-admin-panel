package nodelaunch

import (
	"strings"
	"testing"
)

func assignment() Assignment {
	return Assignment{UID: 60000, OperationID: strings.Repeat("a", 64), ProjectID: strings.Repeat("b", 64), RuntimeID: strings.Repeat("c", 64), ArtifactSHA256: strings.Repeat("d", 64), ToolchainSHA256: strings.Repeat("e", 64), Architecture: "arm64", ReleaseDirectory: "release-" + strings.Repeat("f", 64), Port: 3000}
}
func TestRenderRestrictedNodeService(t *testing.T) {
	t.Parallel()
	a := assignment()
	service, err := Render(a)
	if err != nil {
		t.Fatal(err)
	}
	if service.Name != "deployer-node-"+a.OperationID+".service" {
		t.Fatal(service.Name)
	}
	for _, required := range []string{"User=60000", "Group=60000", "PrivateTmp=no", "ExecStart=:/usr/bin/env -i ", " start --ignore-scripts", "NODE_ENV=production", "HOST=127.0.0.1 PORT=3000", "ProtectSystem=strict", "ProtectHome=yes", "NoNewPrivileges=yes", "KillMode=control-group", "MemoryMax=256M", "TasksMax=64", "CPUQuota=100%", "IPAddressDeny=any", "IPAddressAllow=localhost", "Restart=always", "StartLimitBurst=3"} {
		if !strings.Contains(service.Unit, required) {
			t.Fatalf("missing restriction %q", required)
		}
	}
	if strings.Contains(service.Unit, "PrivateTmp=yes") || strings.Contains(service.Unit, "MemoryDenyWriteExecute=") {
		t.Fatal("unqualified tmp override or Node JIT restriction")
	}
}
func TestRejectServiceInjectionAndUnsupportedAssignments(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		change func(*Assignment)
	}{
		{"root_uid", func(a *Assignment) { a.UID = 0 }},
		{"outside_uid_pool", func(a *Assignment) { a.UID = 61000 }},
		{"operation", func(a *Assignment) { a.OperationID = "x\nExecStart=/bin/sh" }},
		{"project", func(a *Assignment) { a.ProjectID = "%n" }},
		{"runtime", func(a *Assignment) { a.RuntimeID = "" }},
		{"artifact", func(a *Assignment) { a.ArtifactSHA256 = "other" }},
		{"toolchain", func(a *Assignment) { a.ToolchainSHA256 = "../../bin" }},
		{"release", func(a *Assignment) { a.ReleaseDirectory = "release-../outside" }},
		{"architecture", func(a *Assignment) { a.Architecture = "x86" }},
		{"privileged_port", func(a *Assignment) { a.Port = 22 }},
		{"invalid_port", func(a *Assignment) { a.Port = 65536 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := assignment()
			tc.change(&a)
			if _, err := Render(a); err == nil {
				t.Fatal("unsafe service accepted")
			}
		})
	}
}
