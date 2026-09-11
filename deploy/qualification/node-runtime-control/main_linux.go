//go:build linux

// Trusted fixture-only systemd adapter. Production provisioning is separate.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodelaunch"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	if os.Geteuid() != 0 || len(os.Args) != 5 {
		return fmt.Errorf("guest root usage: node-runtime-control start|retire|retire-routed|status|usage OPERATION SHA256 RELEASE_DIRECTORY")
	}
	a := nodelaunch.Assignment{UID: 60000, Port: 31877, OperationID: os.Args[2], ProjectID: strings.Repeat("8", 64), RuntimeID: strings.Repeat("7", 64), ArtifactSHA256: os.Args[3], ToolchainSHA256: "5f4ddab610c1ab2016b3c227cebdbf6d9495161487e4739c7b90090595f465f7", Architecture: "arm64", ReleaseDirectory: os.Args[4]}
	unit, err := nodelaunch.Render(a)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot("/var/lib/deployer-node-lab/control")
	if err != nil {
		return err
	}
	defer root.Close()
	gate, err := nodelaunch.OpenControlGate(root)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := func(verb string) error {
		cmd := exec.CommandContext(ctx, "/usr/bin/systemctl", verb, unit.Name)
		cmd.Env = []string{"PATH=/usr/bin:/bin"}
		return cmd.Run()
	}
	switch os.Args[1] {
	case "usage":
		usage, err := nodelaunch.InspectRuntimeUsage(ctx, a)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(usage)
	case "status":
		state, err := nodelaunch.InspectSystemd(ctx, a)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(struct {
			State       nodelaunch.SystemdState
			UnitStopped bool
		}{state, state.UnitStopped()})
	case "retire-routed":
		return retireRoutedFixture(ctx, gate, a)
	case "start":
		pool, err := openFixturePool(a)
		if err != nil {
			return err
		}
		defer pool.Close()
		claimed, err := pool.ClaimStart(ctx, a.OperationID)
		if err != nil {
			return err
		}
		if claimed.Assignment != a {
			return fmt.Errorf("fixture start claim mismatch")
		}
		return gate.Start(ctx, a, func(context.Context, nodelaunch.Assignment) error {
			actual, err := os.ReadFile("/run/systemd/system/" + unit.Name)
			if err != nil {
				return err
			}
			if !bytes.Equal(actual, []byte(unit.Unit)) {
				return fmt.Errorf("fixture unit does not match assignment")
			}
			return command("start")
		})
	case "retire":
		_, err := gate.RetireSystemd(ctx, a)
		return err
	default:
		return fmt.Errorf("unsupported fixture action")
	}
}
