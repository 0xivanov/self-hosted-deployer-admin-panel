//go:build linux

package nodelaunch

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"time"
)

// RetireSystemd persists the operation's control fence, persistently masks its
// exact unit, stops it, and verifies systemd reports a masked, settled unit.
// Call only after removing traffic. It does not prove routing, UID, cgroup, or
// listener clearance and cannot alone authorize reservation reuse.
//
// Failures may follow a dispatched systemd job. Keep the reservation occupied,
// inspect actual state and retry retirement; never infer no action from an error.
// Every install/start controller must share this gate. Masks must be retained
// permanently for retired operation IDs, including across runtime restarts.
func (g *ControlGate) RetireSystemd(ctx context.Context, a Assignment) (SystemdState, error) {
	var state SystemdState
	unit, err := Render(a)
	if err != nil {
		return state, err
	}
	if os.Geteuid() != 0 || a.Architecture != runtime.GOARCH {
		return state, ErrAssignment
	}
	retireCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	err = g.Retire(retireCtx, a, func(ctx context.Context, _ Assignment) error {
		// Do not force replacement of an existing /etc unit. Unexpected operator
		// state must fail for reconciliation instead of overwriting configuration.
		for _, verb := range []string{"mask", "stop"} {
			cmd := exec.CommandContext(ctx, "/usr/bin/systemctl", "--no-ask-password", verb, "--", unit.Name)
			cmd.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
			if err := cmd.Run(); err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if verb == "mask" {
				directory, err := os.Open("/etc/systemd/system")
				if err != nil {
					return err
				}
				if err = errors.Join(directory.Sync(), directory.Close()); err != nil {
					return err
				}
			}
		}
		var err error
		state, err = InspectSystemd(ctx, a)
		if err != nil {
			return err
		}
		target, err := os.Readlink("/etc/systemd/system/" + unit.Name)
		if err != nil {
			return err
		}
		if target != "/dev/null" || state.LoadState != "masked" || state.UnitFileState != "masked" || !state.UnitStopped() {
			return ErrServiceStatus
		}
		return nil
	})
	return state, err
}
