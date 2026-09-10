//go:build linux && integration

package nodelaunch

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

func controlGate(t *testing.T, directory string) *ControlGate {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("requires root inside disposable Linux VM")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	gate, err := OpenControlGate(root)
	if err != nil {
		t.Fatal(err)
	}
	return gate
}
func controlAssignment() Assignment { a := assignment(); a.Architecture = runtime.GOARCH; return a }
func TestControlGateRetirementPreventsLateMutation(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "gate")
	gate := controlGate(t, directory)
	a := controlAssignment()
	ctx := t.Context()
	var installs, starts, stops atomic.Int32
	install := func(context.Context, Assignment) error { installs.Add(1); return nil }
	start := func(context.Context, Assignment) error { starts.Add(1); return nil }
	stop := func(context.Context, Assignment) error { stops.Add(1); return nil }
	if err := gate.Start(ctx, a, start); !errors.Is(err, ErrConflict) {
		t.Fatal("start without install", err)
	}
	if err := gate.Install(ctx, a, install); err != nil {
		t.Fatal(err)
	}
	if err := gate.Install(ctx, a, install); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if err := gate.Start(ctx, a, start); err != nil {
		t.Fatal(err)
	}
	if err := gate.Start(ctx, a, start); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	failedStop := func(context.Context, Assignment) error { stops.Add(1); return context.DeadlineExceeded }
	if err := gate.Retire(ctx, a, failedStop); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	reopened := controlGate(t, directory)
	if err := reopened.Start(ctx, a, start); !errors.Is(err, ErrConflict) {
		t.Fatal("retirement lost", err)
	}
	if err := reopened.Install(ctx, a, install); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if err := reopened.Retire(ctx, a, stop); err != nil {
		t.Fatal(err)
	}
	state, err := reopened.Inspect(ctx, a)
	if err != nil || !state.Retired || !state.StartAttempted || !state.InstallationAttempted {
		t.Fatal(state, err)
	}
	if installs.Load() != 1 || starts.Load() != 1 || stops.Load() != 2 {
		t.Fatal(installs.Load(), starts.Load(), stops.Load())
	}
	other := a
	other.Port++
	if err := reopened.Retire(ctx, other, stop); !errors.Is(err, ErrConflict) {
		t.Fatal("identity changed", err)
	}
}
func TestControlGateRetireBeforeInstall(t *testing.T) {
	t.Parallel()
	gate := controlGate(t, filepath.Join(t.TempDir(), "gate"))
	a := controlAssignment()
	if err := gate.Retire(t.Context(), a, func(context.Context, Assignment) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := gate.Install(t.Context(), a, func(context.Context, Assignment) error { t.Fatal("late install dispatched"); return nil }); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
}
func TestControlGateSerializesControllersAndHonorsCancellation(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "gate")
	first := controlGate(t, directory)
	second := controlGate(t, directory)
	a := controlAssignment()
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- first.Install(t.Context(), a, func(context.Context, Assignment) error { close(entered); <-release; return nil })
	}()
	<-entered
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if err := second.Retire(ctx, a, func(context.Context, Assignment) error { t.Error("cleanup raced install"); return nil }); !errors.Is(err, context.DeadlineExceeded) {
		close(release)
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := second.Retire(t.Context(), a, func(context.Context, Assignment) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := first.Start(t.Context(), a, func(context.Context, Assignment) error { t.Fatal("retired operation started"); return nil }); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
}
func TestControlGateProcessExitKeepsStartFence(t *testing.T) {
	if directory := os.Getenv("NODE_CONTROL_EXIT_FIXTURE"); directory != "" {
		gate := controlGate(t, directory)
		a := controlAssignment()
		if err := gate.Install(context.Background(), a, func(context.Context, Assignment) error { return nil }); err != nil {
			t.Fatal(err)
		}
		if err := gate.Start(context.Background(), a, func(context.Context, Assignment) error { os.Exit(0); return nil }); err != nil {
			t.Fatal(err)
		}
		t.Fatal("start callback did not exit")
	}
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "gate")
	gate := controlGate(t, directory)
	child := exec.Command(os.Args[0], "-test.run=^TestControlGateProcessExitKeepsStartFence$")
	child.Env = append(os.Environ(), "NODE_CONTROL_EXIT_FIXTURE="+directory)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("child: %v %s", err, output)
	}
	a := controlAssignment()
	state, err := gate.Inspect(t.Context(), a)
	if err != nil || !state.StartAttempted {
		t.Fatal(state, err)
	}
	if err = gate.Start(t.Context(), a, func(context.Context, Assignment) error { t.Fatal("crashed start repeated"); return nil }); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if err = gate.Retire(t.Context(), a, func(context.Context, Assignment) error { return nil }); err != nil {
		t.Fatal("crashed process retained lock", err)
	}
}
func TestControlGateRejectsUnsafeFiles(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"lock_symlink", "record_symlink", "record_mode", "record_identity", "record_json"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			directory := filepath.Join(t.TempDir(), "gate")
			gate := controlGate(t, directory)
			a := controlAssignment()
			if kind == "lock_symlink" {
				if err := os.Symlink("target", filepath.Join(directory, a.OperationID+".lock")); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := gate.Install(t.Context(), a, func(context.Context, Assignment) error { return nil }); err != nil {
					t.Fatal(err)
				}
				record := filepath.Join(directory, a.OperationID+".json")
				switch kind {
				case "record_symlink":
					if err := os.Remove(record); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink("target", record); err != nil {
						t.Fatal(err)
					}
				case "record_mode":
					if err := os.Chmod(record, 0644); err != nil {
						t.Fatal(err)
					}
				case "record_identity":
					a.Port++
				case "record_json":
					if err := os.WriteFile(record, []byte("{} {}"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := gate.Retire(t.Context(), a, func(context.Context, Assignment) error { t.Fatal("unsafe state allowed control"); return nil }); err == nil {
				t.Fatal("unsafe record accepted")
			}
		})
	}
}
func TestControlGateConnectsPoolInstallationAndRetirement(t *testing.T) {
	t.Parallel()
	_, root := installRoot(t)
	gate := controlGate(t, filepath.Join(t.TempDir(), "gate"))
	config := poolConfig()
	config.Architecture = runtime.GOARCH
	p := openPool(t, filepath.Join(t.TempDir(), "private", "pool.db"), config)
	a := poolAssignment(1)
	a.Architecture = runtime.GOARCH
	r, err := p.Reserve(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	installer := GatedInstaller{Gate: gate, Installer: LinuxInstaller{Releases: root}}
	prepared, err := p.PrepareRelease(t.Context(), a.OperationID, reservationData, installer)
	if err != nil || prepared.Installed == nil {
		t.Fatal(prepared, err)
	}
	if _, err = installer.ObserveNodeInstallation(t.Context(), r.Assignment, reservationData); err != nil {
		t.Fatal(err)
	}
	claimed, err := p.ClaimStart(t.Context(), a.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if err = gate.Start(t.Context(), claimed.Assignment, func(context.Context, Assignment) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err = p.BeginRetirement(t.Context(), a.OperationID); err != nil {
		t.Fatal(err)
	}
	if err = gate.Retire(t.Context(), r.Assignment, func(context.Context, Assignment) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err = installer.ObserveNodeInstallation(t.Context(), r.Assignment, reservationData); !errors.Is(err, ErrConflict) {
		t.Fatal("retired installation recovered", err)
	}
	if _, err = installer.InstallNodeRelease(t.Context(), r.Assignment, reservationData); !errors.Is(err, ErrConflict) {
		t.Fatal("retired installation repeated", err)
	}
}
