//go:build linux

package nodelaunch

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"time"
)

// StartInstalledSystemd starts only a claimed, verified installation. The gate
// commits start intent before host actions and excludes retirement throughout.
// The operator must provision and verify the pinned toolchain and exclusively
// reserve this account/port on the isolated runtime before accepting deployments.
// An error may follow a systemd job: reconcile actual state, never retry start.
func (g *ControlGate) StartInstalledSystemd(ctx context.Context, pool *Pool, a Assignment, archive []byte, releases *os.Root) error {
	if pool == nil || releases == nil || os.Geteuid() != 0 || a.Architecture != runtime.GOARCH {
		return ErrAssignment
	}
	unit, err := Render(a)
	if err != nil {
		return err
	}
	reservation, err := pool.Lookup(ctx, a.OperationID)
	if err != nil {
		return err
	}
	if reservation.Assignment != a || reservation.State != "starting" || reservation.Installed == nil || reservation.Installed.Assignment != a {
		return ErrConflict
	}
	startCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return g.Start(startCtx, a, func(ctx context.Context, _ Assignment) error {
		// Recheck the exact sealed bytes immediately before dispatch, inside the gate.
		observed, err := (LinuxInstaller{Releases: releases}).ObserveNodeInstallation(ctx, a, archive)
		if err != nil {
			return err
		}
		if observed.Manifest != reservation.Installed.Manifest {
			return ErrConflict
		}
		account, err := user.LookupId(strconv.Itoa(a.UID))
		if err != nil {
			return ErrAssignment
		}
		groups, err := account.GroupIds()
		if err != nil || account.Gid != account.Uid {
			return ErrAssignment
		}
		for _, group := range groups {
			if group != account.Gid {
				return ErrAssignment
			}
		}
		if err = installRuntimeUnit(unit); err != nil {
			return err
		}
		for _, args := range [][]string{{"daemon-reload"}, {"start", "--", unit.Name}} {
			if err = ctx.Err(); err != nil {
				return err
			}
			command := exec.CommandContext(ctx, "/usr/bin/systemctl", args...)
			command.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
			if err = command.Run(); err != nil {
				return err
			}
		}
		return ctx.Err()
	})
}

func installRuntimeUnit(unit Service) error {
	const directory = "/run/systemd/system"
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || !rootOwned(info) || info.Mode().Perm()&0022 != 0 {
		return ErrAssignment
	}
	path := filepath.Join(directory, unit.Name)
	verify := func() error {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || !rootOwned(info) || info.Mode().Perm()&0022 != 0 || info.Size() != int64(len(unit.Unit)) {
			return ErrAssignment
		}
		actual, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.Equal(actual, []byte(unit.Unit)) {
			return ErrConflict
		}
		return nil
	}
	if _, err = os.Lstat(path); err == nil {
		return verify()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, err := os.CreateTemp(directory, ".deployer-unit-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.WriteString(unit.Unit)
	if err = errors.Join(writeErr, file.Chmod(0644), file.Sync(), file.Close()); err != nil {
		return err
	}
	// Link publishes without overwriting a mask or another controller's unit.
	if err = os.Link(file.Name(), path); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if err = verify(); err != nil {
		return err
	}
	parent, err := os.Open(directory)
	if err != nil {
		return err
	}
	return errors.Join(parent.Sync(), parent.Close())
}
