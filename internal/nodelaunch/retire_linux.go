//go:build linux

package nodelaunch

import (
	"context"
	"os"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/noderouter"
)

// RetireRoutedNode joins routing drain, service retirement and reservation
// reconciliation on the dedicated Linux runtime host. The router must be the
// only ingress to the reserved listener; all launchers must share pool and gate.
// Runtime provisioning must give this controller full host process/network
// visibility and exclusively allocate these UIDs/ports to the pool.
//
// The routing guard remains held through the reservation commit. Any incomplete
// drain, stop or observation leaves the slot occupied for recovery. Terminal
// retries return the recorded receipt without touching a possibly reused slot.
func RetireRoutedNode(ctx context.Context, pool *Pool, gate *ControlGate, router *noderouter.Router, c noderouter.Candidate) (Reservation, error) {
	var result Reservation
	if pool == nil || gate == nil || router == nil {
		return result, ErrAssignment
	}
	current, err := pool.Lookup(ctx, c.OperationID)
	if err != nil {
		return result, err
	}
	a := current.Assignment
	port, err := router.BackendPort(c)
	if err != nil {
		return result, err
	}
	if c.ProjectID != a.ProjectID || c.RuntimeID != a.RuntimeID || c.ArtifactSHA256 != a.ArtifactSHA256 || port != a.Port {
		return result, ErrAssignment
	}
	if current.State == "retired" {
		return current, nil
	}
	// Refuse a controller isolated from the visible system manager. Provisioning
	// is still responsible for making PID 1 the dedicated runtime's host manager.
	if err = hostNamespaces(); err != nil {
		return result, err
	}
	result, err = pool.BeginRetirement(ctx, c.OperationID)
	if err != nil {
		return result, err
	}
	err = router.WithDrainedBackend(ctx, c, func(ctx context.Context) error {
		if _, err := gate.RetireSystemd(ctx, a); err != nil {
			return err
		}
		var err error
		result, err = pool.ReconcileRetirement(ctx, c.OperationID, drainedHostReader{gate: gate})
		return err
	})
	return result, err
}

// This private reader can assert routing detachment only inside the routing
// guard above. It must never be exposed as a standalone retirement provider.
type drainedHostReader struct{ gate *ControlGate }

func (r drainedHostReader) ObserveNodeRetirement(ctx context.Context, a Assignment) (RetirementObservation, error) {
	var out RetirementObservation
	control, err := r.gate.Inspect(ctx, a)
	if err != nil {
		return out, err
	}
	if !control.Retired {
		return out, ErrRetirement
	}
	state, err := InspectSystemd(ctx, a)
	if err != nil {
		return out, err
	}
	if !state.UnitStopped() || state.LoadState != "masked" || state.UnitFileState != "masked" {
		return out, ErrRetirement
	}
	usage, err := InspectRuntimeUsage(ctx, a)
	if err != nil {
		return out, err
	}
	if usage.UIDProcessesPresent || usage.CgroupPopulated || usage.TCPListenerPresent {
		return out, ErrRetirement
	}
	return RetirementObservation{Assignment: a, ObservedAt: time.Now(), StartsFenced: true, ProcessesGone: true, ListenerGone: true, RoutingDetached: true}, nil
}

func hostNamespaces() error {
	if os.Geteuid() != 0 {
		return ErrAssignment
	}
	for _, name := range []string{"pid", "net", "mnt", "user", "cgroup"} {
		own, err := os.Readlink("/proc/self/ns/" + name)
		if err != nil {
			return err
		}
		init, err := os.Readlink("/proc/1/ns/" + name)
		if err != nil {
			return err
		}
		if own != init {
			return ErrRuntimeUsage
		}
	}
	return nil
}
