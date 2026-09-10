//go:build integration

package nodelaunch

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodeartifact"
)

var reservationData, reservationDigest = makeReservationArchive()

func makeReservationArchive() ([]byte, string) {
	var out bytes.Buffer
	archive := zip.NewWriter(&out)
	file, err := archive.Create("package.json")
	if err != nil {
		panic(err)
	}
	if _, err = file.Write([]byte(`{"name":"pool-fixture","version":"1.0.0","scripts":{"start":"node server.js"}}`)); err != nil {
		panic(err)
	}
	if err = archive.Close(); err != nil {
		panic(err)
	}
	sum := sha256.Sum256(out.Bytes())
	return out.Bytes(), hex.EncodeToString(sum[:])
}

type fixtureInstaller func(context.Context, Assignment, []byte) (nodeartifact.Release, error)

func (f fixtureInstaller) InstallNodeRelease(ctx context.Context, a Assignment, data []byte) (nodeartifact.Release, error) {
	return f(ctx, a, data)
}
func fixtureInstall(ctx context.Context, a Assignment, data []byte) (nodeartifact.Release, error) {
	m, err := nodeartifact.Validate(ctx, data, a.ArtifactSHA256)
	return nodeartifact.Release{Directory: a.ReleaseDirectory, Manifest: m}, err
}
func prepareFixture(t *testing.T, p *Pool, operation string) {
	t.Helper()
	if _, err := p.PrepareRelease(context.Background(), operation, reservationData, fixtureInstaller(fixtureInstall)); err != nil {
		t.Fatal(err)
	}
}
func TestInstallationReceiptRequiredBeforeStart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "private", "pool.db")
	p := openPool(t, path, poolConfig())
	a := poolAssignment(1)
	if _, err := p.Reserve(ctx, a); err != nil {
		t.Fatal(err)
	}
	if _, err := p.ClaimStart(ctx, a.OperationID); !errors.Is(err, ErrConflict) {
		t.Fatal("uninstalled release started", err)
	}
	var calls atomic.Int32
	installer := fixtureInstaller(func(ctx context.Context, a Assignment, data []byte) (nodeartifact.Release, error) {
		calls.Add(1)
		return fixtureInstall(ctx, a, data)
	})
	prepared, err := p.PrepareRelease(ctx, a.OperationID, reservationData, installer)
	if err != nil || prepared.Installed == nil || !prepared.InstallationAttempted || prepared.Installed.Assignment != prepared.Assignment {
		t.Fatal(prepared, err)
	}
	if err = p.Close(); err != nil {
		t.Fatal(err)
	}
	p = openPool(t, path, poolConfig())
	retried, err := p.PrepareRelease(ctx, a.OperationID, reservationData, installer)
	if err != nil || retried.Installed == nil || calls.Load() != 1 || !retried.Installed.InstalledAt.Equal(prepared.Installed.InstalledAt) {
		t.Fatal(retried, calls.Load(), err)
	}
	if _, err = p.ClaimStart(ctx, a.OperationID); err != nil {
		t.Fatal(err)
	}
	if _, err = p.PrepareRelease(ctx, a.OperationID, reservationData, installer); !errors.Is(err, ErrConflict) {
		t.Fatal("running release reinstalled", err)
	}
}
func TestInstallationUnknownResultsNeverRetry(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		change func(*nodeartifact.Release) error
	}{
		{"timeout", func(*nodeartifact.Release) error { return context.DeadlineExceeded }},
		{"wrong_directory", func(r *nodeartifact.Release) error {
			r.Directory = "release-" + poolAssignment(9).OperationID
			return nil
		}},
		{"wrong_digest", func(r *nodeartifact.Release) error { r.Manifest.SHA256 = poolAssignment(9).OperationID; return nil }},
		{"wrong_size", func(r *nodeartifact.Release) error { r.Manifest.ExpandedBytes++; return nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "private", "pool.db")
			p := openPool(t, path, poolConfig())
			a := poolAssignment(1)
			if _, err := p.Reserve(ctx, a); err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			installer := fixtureInstaller(func(ctx context.Context, a Assignment, data []byte) (nodeartifact.Release, error) {
				calls.Add(1)
				r, err := fixtureInstall(ctx, a, data)
				if err != nil {
					return r, err
				}
				err = tc.change(&r)
				return r, err
			})
			if _, err := p.PrepareRelease(ctx, a.OperationID, reservationData, installer); err == nil {
				t.Fatal("invalid receipt accepted")
			}
			if err := p.Close(); err != nil {
				t.Fatal(err)
			}
			p = openPool(t, path, poolConfig())
			if _, err := p.PrepareRelease(ctx, a.OperationID, reservationData, installer); !errors.Is(err, ErrConflict) {
				t.Fatal("unknown install repeated", err)
			}
			if calls.Load() != 1 {
				t.Fatal(calls.Load())
			}
			r, err := p.Lookup(ctx, a.OperationID)
			if err != nil || !r.InstallationAttempted || r.Installed != nil {
				t.Fatal(r, err)
			}
			if _, err = p.ClaimStart(ctx, a.OperationID); !errors.Is(err, ErrConflict) {
				t.Fatal(err)
			}
		})
	}
}
func TestInstallationConcurrencyAndRetirement(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "private", "pool.db")
	p := openPool(t, path, poolConfig())
	other := openPool(t, path, poolConfig())
	a := poolAssignment(1)
	if _, err := p.Reserve(ctx, a); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	installer := fixtureInstaller(func(ctx context.Context, a Assignment, data []byte) (nodeartifact.Release, error) {
		calls.Add(1)
		close(entered)
		<-release
		return fixtureInstall(ctx, a, data)
	})
	result := make(chan error, 1)
	go func() { _, err := p.PrepareRelease(ctx, a.OperationID, reservationData, installer); result <- err }()
	<-entered
	if _, err := other.PrepareRelease(ctx, a.OperationID, reservationData, installer); !errors.Is(err, ErrConflict) {
		close(release)
		t.Fatal("second install dispatched", err)
	}
	if _, err := other.BeginRetirement(ctx, a.OperationID); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	if err := <-result; !errors.Is(err, ErrConflict) {
		t.Fatal("late install authorized retired operation", err)
	}
	r, err := p.Lookup(ctx, a.OperationID)
	if err != nil || r.State != "retiring" || r.Installed != nil || calls.Load() != 1 {
		t.Fatal(r, err)
	}
	if _, err = p.ClaimStart(ctx, a.OperationID); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
}
func TestInstallationInvalidArchiveDoesNotConsumeAttempt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p := openPool(t, filepath.Join(t.TempDir(), "private", "pool.db"), poolConfig())
	a := poolAssignment(1)
	if _, err := p.Reserve(ctx, a); err != nil {
		t.Fatal(err)
	}
	if _, err := p.PrepareRelease(ctx, a.OperationID, []byte("invalid"), fixtureInstaller(fixtureInstall)); !errors.Is(err, nodeartifact.ErrArtifact) {
		t.Fatal(err)
	}
	r, err := p.Lookup(ctx, a.OperationID)
	if err != nil || r.InstallationAttempted {
		t.Fatal(r, err)
	}
	alias := poolAssignment(2)
	alias.ReleaseDirectory = a.ReleaseDirectory
	if _, err = p.Reserve(ctx, alias); !errors.Is(err, ErrConflict) {
		t.Fatal("aliased release directory", err)
	}
	prepareFixture(t, p, a.OperationID)
}
func TestInstallationMigratesOutstandingVersionOne(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "private", "pool.db")
	p := openPool(t, path, poolConfig())
	a := poolAssignment(1)
	if _, err := p.Reserve(ctx, a); err != nil {
		t.Fatal(err)
	}
	prepareFixture(t, p, a.OperationID)
	if _, err := p.ClaimStart(ctx, a.OperationID); err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec("ALTER TABLE reservations DROP COLUMN installation_attempted; ALTER TABLE reservations DROP COLUMN installed; PRAGMA user_version=1;"); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	p = openPool(t, path, poolConfig())
	r, err := p.Lookup(ctx, a.OperationID)
	if err != nil || r.State != "starting" || r.Installed != nil {
		t.Fatal(r, err)
	}
	if _, err = p.ClaimStart(ctx, a.OperationID); !errors.Is(err, ErrConflict) {
		t.Fatal("legacy start repeated", err)
	}
	if _, err = p.PrepareRelease(ctx, a.OperationID, reservationData, fixtureInstaller(fixtureInstall)); !errors.Is(err, ErrConflict) {
		t.Fatal("legacy running service reinstalled", err)
	}
	if _, err = p.BeginRetirement(ctx, a.OperationID); err != nil {
		t.Fatal(err)
	}
}

func TestInstallationIntentSurvivesInstallerProcessExit(t *testing.T) {
	if path := os.Getenv("NODE_INSTALL_EXIT_FIXTURE"); path != "" {
		p, err := OpenPool(path, poolConfig())
		if err != nil {
			t.Fatal(err)
		}
		a := poolAssignment(1)
		if _, err = p.Reserve(context.Background(), a); err != nil {
			t.Fatal(err)
		}
		installer := fixtureInstaller(func(context.Context, Assignment, []byte) (nodeartifact.Release, error) {
			os.Exit(0)
			return nodeartifact.Release{}, nil
		})
		if _, err = p.PrepareRelease(context.Background(), a.OperationID, reservationData, installer); err != nil {
			t.Fatal(err)
		}
		t.Fatal("installer did not exit")
	}
	t.Parallel()
	path := filepath.Join(t.TempDir(), "private", "pool.db")
	child := exec.Command(os.Args[0], "-test.run=^TestInstallationIntentSurvivesInstallerProcessExit$")
	child.Env = append(os.Environ(), "NODE_INSTALL_EXIT_FIXTURE="+path)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("child: %v %s", err, output)
	}
	p := openPool(t, path, poolConfig())
	ctx := context.Background()
	a := poolAssignment(1)
	r, err := p.Lookup(ctx, a.OperationID)
	if err != nil || !r.InstallationAttempted || r.Installed != nil || r.State != "reserved" {
		t.Fatal(r, err)
	}
	if _, err = p.PrepareRelease(ctx, a.OperationID, reservationData, fixtureInstaller(fixtureInstall)); !errors.Is(err, ErrConflict) {
		t.Fatal("crashed installation repeated", err)
	}
	if _, err = p.ClaimStart(ctx, a.OperationID); !errors.Is(err, ErrConflict) {
		t.Fatal("unknown installation started", err)
	}
}
