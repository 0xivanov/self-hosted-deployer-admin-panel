//go:build linux

package nodelaunch

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"syscall"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodeartifact"
	"golang.org/x/sys/unix"
)

// ControlGate serializes host mutations across processes. Its root must be
// private, root-owned, durable and shared by every controller for these services.
// Never remove lock files or restore older records while old requests can exist.
// It is not a service manager, authorization endpoint or proof of stopped OS jobs.
type ControlGate struct{ root *os.Root }
type ControlState struct {
	Version                                        int
	Assignment                                     Assignment
	InstallationAttempted, StartAttempted, Retired bool
}
type ControlAction func(context.Context, Assignment) error

func OpenControlGate(root *os.Root) (*ControlGate, error) {
	if os.Geteuid() != 0 || root == nil {
		return nil, ErrAssignment
	}
	info, err := root.Stat(".")
	if err != nil || !info.IsDir() || !rootOwned(info) || info.Mode().Perm() != 0700 {
		return nil, ErrAssignment
	}
	return &ControlGate{root: root}, nil
}
func (g *ControlGate) lock(ctx context.Context, a Assignment) (*os.File, error) {
	if _, err := Render(a); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := g.openFile(a.OperationID+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !privateControlFile(info) {
		f.Close()
		return nil, ErrAssignment
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}
func unlockControl(f *os.File) { unix.Flock(int(f.Fd()), unix.LOCK_UN); f.Close() }
func privateControlFile(info os.FileInfo) bool {
	return info != nil && info.Mode().IsRegular() && info.Mode().Perm() == 0600 && rootOwned(info) && info.Sys().(*syscall.Stat_t).Nlink == 1
}

// Names are validated operation IDs or generated temporary basenames, with no
// path components. Use the kernel's no-follow check directly: os.Root.OpenFile
// may resolve links itself before applying flags to the final open.
func (g *ControlGate) openFile(name string, flags int, mode os.FileMode) (*os.File, error) {
	directory, err := g.root.Open(".")
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	fd, err := unix.Openat(int(directory.Fd()), name, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(mode))
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}
func (g *ControlGate) read(a Assignment) (ControlState, error) {
	var state ControlState
	f, err := g.openFile(a.OperationID+".json", os.O_RDONLY, 0)
	if errors.Is(err, os.ErrNotExist) {
		return state, ErrNotFound
	}
	if err != nil {
		return state, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !privateControlFile(info) || info.Size() > 4096 {
		return state, ErrAssignment
	}
	decoder := json.NewDecoder(io.LimitReader(f, 4097))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&state); err != nil {
		return state, err
	}
	var extra any
	if err = decoder.Decode(&extra); err != io.EOF {
		return state, ErrAssignment
	}
	if state.Version != 1 || state.Assignment != a || (state.StartAttempted && !state.InstallationAttempted) {
		return state, ErrConflict
	}
	return state, nil
}
func (g *ControlGate) write(state ControlState) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	var entropy [32]byte
	if _, err = rand.Read(entropy[:]); err != nil {
		return err
	}
	temporary := ".pending-" + hex.EncodeToString(entropy[:])
	f, err := g.openFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer g.root.Remove(temporary)
	_, writeErr := io.Copy(f, bytes.NewReader(raw))
	err = errors.Join(writeErr, f.Sync(), f.Close())
	if err != nil {
		return err
	}
	if err = g.root.Rename(temporary, state.Assignment.OperationID+".json"); err != nil {
		return err
	}
	directory, err := g.root.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}

// Inspect reports durable control intent, never process state. Calls made from
// inside a ControlAction must not reenter this gate for the same operation.
func (g *ControlGate) Inspect(ctx context.Context, a Assignment) (ControlState, error) {
	lock, err := g.lock(ctx, a)
	if err != nil {
		return ControlState{}, err
	}
	defer unlockControl(lock)
	if err = ctx.Err(); err != nil {
		return ControlState{}, err
	}
	return g.read(a)
}

func (g *ControlGate) Install(ctx context.Context, a Assignment, action ControlAction) error {
	return g.run(ctx, a, "install", action)
}

// Start requires installation dispatch through the same gate. The caller must
// additionally hold Pool start authorization and verify the installation receipt,
// toolchain, account, listener and runtime identity before actually starting.
func (g *ControlGate) Start(ctx context.Context, a Assignment, action ControlAction) error {
	return g.run(ctx, a, "start", action)
}

// Retire commits a permanent fence before calling cleanup, even if no install
// ever arrived. Cleanup may be retried. It must settle OS jobs, stop processes and
// disable/mask restart paths; a gate fence alone does not prove those facts.
func (g *ControlGate) Retire(ctx context.Context, a Assignment, action ControlAction) error {
	return g.run(ctx, a, "retire", action)
}
func (g *ControlGate) run(ctx context.Context, a Assignment, kind string, action ControlAction) error {
	if action == nil {
		return ErrAssignment
	}
	lock, err := g.lock(ctx, a)
	if err != nil {
		return err
	}
	defer unlockControl(lock)
	if err = ctx.Err(); err != nil {
		return err
	}
	state, err := g.read(a)
	if errors.Is(err, ErrNotFound) {
		state = ControlState{Version: 1, Assignment: a}
	} else if err != nil {
		return err
	}
	switch kind {
	case "install":
		if state.Retired || state.InstallationAttempted {
			return ErrConflict
		}
		state.InstallationAttempted = true
	case "start":
		if state.Retired || state.StartAttempted || !state.InstallationAttempted {
			return ErrConflict
		}
		state.StartAttempted = true
	case "retire":
		state.Retired = true
	default:
		return ErrAssignment
	}
	if err = g.write(state); err != nil {
		return err
	}
	// A cancellation after persisting intent still consumes the attempt. The next
	// controller must inspect/reconcile instead of dispatching it a second time.
	if err = ctx.Err(); err != nil {
		return err
	}
	err = action(ctx, a)
	return errors.Join(err, ctx.Err())
}

// GatedInstaller connects pool preparation/recovery to the same durable host
// control gate later used for starts and retirement. All controllers must use
// this wrapper; bypassing it bypasses retirement fencing.
type GatedInstaller struct {
	Gate      *ControlGate
	Installer LinuxInstaller
}

func (i GatedInstaller) InstallNodeRelease(ctx context.Context, a Assignment, data []byte) (nodeartifact.Release, error) {
	var result nodeartifact.Release
	if i.Gate == nil {
		return result, ErrAssignment
	}
	err := i.Gate.Install(ctx, a, func(ctx context.Context, a Assignment) error {
		var err error
		result, err = i.Installer.InstallNodeRelease(ctx, a, data)
		return err
	})
	return result, err
}
func (i GatedInstaller) ObserveNodeInstallation(ctx context.Context, a Assignment, data []byte) (InstallationObservation, error) {
	if i.Gate == nil {
		return InstallationObservation{}, ErrAssignment
	}
	lock, err := i.Gate.lock(ctx, a)
	if err != nil {
		return InstallationObservation{}, err
	}
	defer unlockControl(lock)
	state, err := i.Gate.read(a)
	if err != nil {
		return InstallationObservation{}, err
	}
	if state.Retired || !state.InstallationAttempted {
		return InstallationObservation{}, ErrConflict
	}
	return i.Installer.ObserveNodeInstallation(ctx, a, data)
}
