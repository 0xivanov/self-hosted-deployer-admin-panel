package nodelaunch

import (
	"context"
	"encoding/json"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodeartifact"
)

type InstalledRelease struct {
	Assignment  Assignment
	Manifest    nodeartifact.Manifest
	InstalledAt time.Time
}

// ReleaseInstaller is trusted host access. Success promises the exact archive was
// validated, privately extracted, sealed read-only and synced at the reserved
// directory. Errors, including timeouts after publication, are never successes.
// The host service manager must fence calls against retirement, including calls
// already dispatched. An installer primitive alone cannot establish that fence.
// Installers must not execute uploaded code.
type ReleaseInstaller interface {
	InstallNodeRelease(context.Context, Assignment, []byte) (nodeartifact.Release, error)
}

// PrepareRelease records one installation attempt before dispatch. A retry after
// an uncertain outcome never calls the installer again. A completed receipt is
// idempotent. Unknown outcomes retain their slot until trusted installation
// reconciliation or retirement.
func (p *Pool) PrepareRelease(ctx context.Context, operation string, data []byte, installer ReleaseInstaller) (Reservation, error) {
	r, err := p.Lookup(ctx, operation)
	if err != nil {
		return r, err
	}
	manifest, err := nodeartifact.Validate(ctx, data, r.Assignment.ArtifactSHA256)
	if err != nil {
		return r, err
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return r, err
	}
	defer tx.Rollback()
	current, err := readReservation(tx.QueryRowContext(ctx, "SELECT assignment,state,retirement,installation_attempted,installed FROM reservations WHERE operation=?", operation))
	if err != nil {
		return r, err
	}
	if current.Assignment != r.Assignment || current.State != "reserved" {
		return current, ErrConflict
	}
	if current.Installed != nil {
		if current.Installed.Assignment != r.Assignment || current.Installed.Manifest != manifest {
			return current, ErrConflict
		}
		return current, tx.Commit()
	}
	if current.InstallationAttempted || installer == nil {
		return current, ErrConflict
	}
	if _, err = tx.ExecContext(ctx, "UPDATE reservations SET installation_attempted=1 WHERE operation=?", operation); err != nil {
		return r, err
	}
	if err = tx.Commit(); err != nil {
		return r, err
	}
	r.InstallationAttempted = true
	installCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	installed, err := installer.InstallNodeRelease(installCtx, r.Assignment, data)
	if err != nil {
		return r, err
	}
	if err = installCtx.Err(); err != nil {
		return r, err
	}
	if installed.Directory != r.Assignment.ReleaseDirectory || installed.Manifest != manifest {
		return r, ErrConflict
	}
	receipt := InstalledRelease{Assignment: r.Assignment, Manifest: manifest, InstalledAt: time.Now()}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return r, err
	}
	tx, err = p.db.BeginTx(ctx, nil)
	if err != nil {
		return r, err
	}
	defer tx.Rollback()
	current, err = readReservation(tx.QueryRowContext(ctx, "SELECT assignment,state,retirement,installation_attempted,installed FROM reservations WHERE operation=?", operation))
	if err != nil {
		return r, err
	}
	if current.Assignment != r.Assignment || current.State != "reserved" || !current.InstallationAttempted || current.Installed != nil {
		return current, ErrConflict
	}
	if _, err = tx.ExecContext(ctx, "UPDATE reservations SET installed=? WHERE operation=?", raw, operation); err != nil {
		return r, err
	}
	r.Installed = &receipt
	return r, tx.Commit()
}

// InstallationObservation is collected by a trusted local adapter. ObservedAt
// must use the caller's clock after byte, ownership, permission and sync checks.
type InstallationObservation struct {
	Assignment Assignment
	Manifest   nodeartifact.Manifest
	ObservedAt time.Time
}
type InstallationReader interface {
	ObserveNodeInstallation(context.Context, Assignment, []byte) (InstallationObservation, error)
}

// ReconcileInstallation recovers a completed but unacknowledged installation by
// inspecting it. It never dispatches installation or starts a process. Failure
// leaves the attempt and occupied slot unchanged for later recovery/retirement.
func (p *Pool) ReconcileInstallation(ctx context.Context, operation string, data []byte, reader InstallationReader) (Reservation, error) {
	r, err := p.Lookup(ctx, operation)
	if err != nil {
		return r, err
	}
	manifest, err := nodeartifact.Validate(ctx, data, r.Assignment.ArtifactSHA256)
	if err != nil {
		return r, err
	}
	if r.State != "reserved" || !r.InstallationAttempted {
		return r, ErrConflict
	}
	if r.Installed != nil {
		if r.Installed.Assignment != r.Assignment || r.Installed.Manifest != manifest {
			return r, ErrConflict
		}
		return r, nil
	}
	if reader == nil {
		return r, ErrConflict
	}
	started := time.Now()
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	observation, err := reader.ObserveNodeInstallation(probeCtx, r.Assignment, data)
	if err != nil {
		return r, err
	}
	if err = probeCtx.Err(); err != nil {
		return r, err
	}
	if observation.Assignment != r.Assignment || observation.Manifest != manifest || observation.ObservedAt.Before(started) || observation.ObservedAt.After(time.Now()) {
		return r, ErrConflict
	}
	receipt := InstalledRelease{Assignment: r.Assignment, Manifest: manifest, InstalledAt: observation.ObservedAt}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return r, err
	}
	defer tx.Rollback()
	current, err := readReservation(tx.QueryRowContext(ctx, "SELECT assignment,state,retirement,installation_attempted,installed FROM reservations WHERE operation=?", operation))
	if err != nil {
		return r, err
	}
	if current.Assignment != r.Assignment || current.State != "reserved" || !current.InstallationAttempted {
		return current, ErrConflict
	}
	if current.Installed != nil {
		if current.Installed.Assignment != r.Assignment || current.Installed.Manifest != manifest {
			return current, ErrConflict
		}
		return current, tx.Commit()
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return r, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE reservations SET installed=? WHERE operation=?", raw, operation); err != nil {
		return r, err
	}
	r.Installed = &receipt
	return r, tx.Commit()
}
