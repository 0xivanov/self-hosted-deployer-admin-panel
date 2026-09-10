// Package nodelaunch prepares restricted Linux services for verified Node
// releases. Rendering a unit does not qualify or enforce the host's isolation.
package nodelaunch

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

var ErrAssignment = errors.New("invalid Node service assignment")

type Assignment struct {
	UID              int
	OperationID      string
	ProjectID        string
	RuntimeID        string
	ArtifactSHA256   string
	ToolchainSHA256  string
	Architecture     string
	ReleaseDirectory string
	Port             int
}
type Service struct {
	Name string
	Unit string
}

func id(s string) bool {
	v, e := hex.DecodeString(s)
	return e == nil && len(v) == 32 && hex.EncodeToString(v) == s
}

// Render uses only validated identifiers and fixed operator-owned paths. It must
// be installed by the trusted runtime agent only after verifying the artifact,
// toolchain and actual Linux architecture and sealing the release root read-only.
// No commands, environment values or service properties come from uploaded input.
func Render(a Assignment) (Service, error) {
	if a.UID < 60000 || a.UID > 60999 || !id(a.OperationID) || !id(a.ProjectID) || !id(a.RuntimeID) || !id(a.ArtifactSHA256) || !id(a.ToolchainSHA256) || (a.Architecture != "arm64" && a.Architecture != "amd64") || a.Port < 1024 || a.Port > 65535 || !strings.HasPrefix(a.ReleaseDirectory, "release-") || !id(strings.TrimPrefix(a.ReleaseDirectory, "release-")) {
		return Service{}, ErrAssignment
	}
	name := "deployer-node-" + a.OperationID + ".service"
	root := "/opt/deployer-node"
	bin := root + "/toolchains/" + a.ToolchainSHA256
	// The trusted launcher must reserve each UID exclusively, verify its account
	// has no supplementary privileges, and keep it reserved until full shutdown.
	unit := fmt.Sprintf(`[Unit]
Description=Node project %s operation %s
StartLimitIntervalSec=60
StartLimitBurst=3

[Service]
Type=exec
User=%d
Group=%d
SupplementaryGroups=
PrivateTmp=no
RemoveIPC=yes
WorkingDirectory=%s/releases/%s
ExecStart=:/usr/bin/env -i PATH=%s/bin:/usr/bin:/bin HOME=/work NODE_ENV=production HOST=127.0.0.1 PORT=%d npm_config_cache=/work/npm-cache npm_config_userconfig=%s/empty-user.npmrc npm_config_globalconfig=%s/empty-global.npmrc npm_config_offline=true %s/bin/node %s/lib/node_modules/npm/bin/npm-cli.js start --ignore-scripts
Restart=always
RestartSec=3
TimeoutStartSec=15
TimeoutStopSec=5
KillMode=control-group
SendSIGKILL=yes
OOMPolicy=stop
UMask=0077
NoNewPrivileges=yes
CapabilityBoundingSet=
AmbientCapabilities=
PrivateDevices=yes
ProtectSystem=strict
ProtectHome=yes
ProtectControlGroups=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectKernelLogs=yes
ProtectProc=invisible
RestrictSUIDSGID=yes
RestrictNamespaces=yes
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
LockPersonality=yes
MemoryMax=256M
MemorySwapMax=0
TasksMax=64
CPUQuota=100%%
LimitFSIZE=16M
TemporaryFileSystem=/work:rw,size=64M,mode=1777 /tmp:rw,size=16M,mode=1777 /var/tmp:rw,size=16M,mode=1777
IPAddressDeny=any
IPAddressAllow=localhost
StandardOutput=journal
StandardError=journal
LogRateLimitIntervalSec=30s
LogRateLimitBurst=200
`, a.ProjectID, a.OperationID, a.UID, a.UID, root, a.ReleaseDirectory, bin, a.Port, root, root, bin, bin)
	return Service{Name: name, Unit: unit}, nil
}
