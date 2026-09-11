//go:build linux

// Trusted disposable-VM helper. Installs a verified fixture; never starts code.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodeartifact"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodelaunch"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) != 4 || os.Geteuid() != 0 {
		return fmt.Errorf("guest root usage: node-runtime-install ZIP SHA256 OPERATION")
	}
	f, err := os.Open(os.Args[1])
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, nodeartifact.MaxCompressed+1))
	if err != nil {
		return err
	}
	root, err := os.OpenRoot("/opt/deployer-node/releases")
	if err != nil {
		return err
	}
	defer root.Close()
	if err = os.MkdirAll("/var/lib/deployer-node-lab/control", 0700); err != nil {
		return err
	}
	control, err := os.OpenRoot("/var/lib/deployer-node-lab/control")
	if err != nil {
		return err
	}
	defer control.Close()
	gate, err := nodelaunch.OpenControlGate(control)
	if err != nil {
		return err
	}
	a := nodelaunch.Assignment{UID: 60000, Port: 31877, OperationID: os.Args[3], ProjectID: strings.Repeat("8", 64), RuntimeID: strings.Repeat("7", 64), ArtifactSHA256: os.Args[2], ToolchainSHA256: "5f4ddab610c1ab2016b3c227cebdbf6d9495161487e4739c7b90090595f465f7", Architecture: "arm64", ReleaseDirectory: "release-" + os.Args[3]}
	installer := nodelaunch.GatedInstaller{Gate: gate, Installer: nodelaunch.LinuxInstaller{Releases: root}}
	pool, err := nodelaunch.OpenPool("/var/lib/deployer-node-lab/pool/state.db", nodelaunch.PoolConfig{ProjectID: a.ProjectID, RuntimeID: a.RuntimeID, ToolchainSHA256: a.ToolchainSHA256, Architecture: a.Architecture, Slots: []nodelaunch.Slot{{UID: 60000, Port: 31877}, {UID: 60001, Port: 31878}}})
	if err != nil {
		return err
	}
	defer pool.Close()
	requested := a
	requested.UID = 0
	requested.Port = 0
	reserved, err := pool.Reserve(context.Background(), requested)
	if err != nil {
		return err
	}
	if reserved.Assignment != a {
		return fmt.Errorf("fixture slot remains occupied; reconcile before another rehearsal")
	}
	prepared, err := pool.PrepareRelease(context.Background(), a.OperationID, data, installer)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(nodeartifact.Release{Directory: prepared.Assignment.ReleaseDirectory, Manifest: prepared.Installed.Manifest})
}
