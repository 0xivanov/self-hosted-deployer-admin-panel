//go:build linux && integration

package nodelaunch

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodeartifact"
)

func installArchive(t *testing.T, extra string) ([]byte, string) {
	t.Helper()
	var out bytes.Buffer
	archive := zip.NewWriter(&out)
	for _, entry := range []struct {
		name, body string
		mode       os.FileMode
	}{
		{"package.json", `{"name":"sealed-fixture","version":"1.0.0","scripts":{"start":"node server.js"}}`, 0600},
		{"server.js", "throw new Error('must never execute during installation');", 0600},
		{"node_modules/tool/cli.js", "synthetic executable fixture", 0700 | os.ModeSetuid},
		{"node_modules/.bin/tool", "../tool/cli.js", os.ModeSymlink | 0777},
	} {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		header.SetMode(entry.mode)
		w, err := archive.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write([]byte(entry.body)); err != nil {
			t.Fatal(err)
		}
	}
	if extra != "" {
		w, err := archive.Create(extra)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write([]byte("forbidden")); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	data := out.Bytes()
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:])
}
func installRoot(t *testing.T) (string, *os.Root) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("requires root inside disposable Linux VM")
	}
	directory := t.TempDir()
	// This synthetic fixture is intentionally readable by the test runtime UID.
	if err := os.Chmod(filepath.Dir(directory), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0755); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return directory, root
}
func TestInstallReleaseSealsAndPublishesWithoutExecuting(t *testing.T) {
	t.Parallel()
	directory, root := installRoot(t)
	data, digest := installArchive(t, "")
	first, err := InstallRelease(context.Background(), data, digest, root)
	if err != nil {
		t.Fatal(err)
	}
	if first.Manifest.SHA256 != digest || first.Directory == "" {
		t.Fatal(first)
	}
	installed, err := root.OpenRoot(first.Directory)
	if err != nil {
		t.Fatal(err)
	}
	defer installed.Close()
	err = fs.WalkDir(installed.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := installed.Lstat(name)
		if err != nil {
			return err
		}
		if !rootOwned(info) {
			t.Errorf("not root owned: %s", name)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		expected := os.FileMode(0444)
		if info.IsDir() || name == "node_modules/tool/cli.js" {
			expected = 0555
		}
		if info.Mode().Perm() != expected || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
			t.Errorf("incorrect mode %s: %v", name, info.Mode())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	link, err := installed.Readlink("node_modules/.bin/tool")
	if err != nil || link != "../tool/cli.js" {
		t.Fatal(link, err)
	}
	second, err := InstallRelease(context.Background(), data, digest, root)
	if err != nil || first.Directory == second.Directory {
		t.Fatal(second, err)
	}
	contents, err := root.ReadFile(first.Directory + "/server.js")
	if err != nil || !bytes.Contains(contents, []byte("must never execute")) {
		t.Fatal(string(contents), err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 2 {
		t.Fatal("staging residue", entries, err)
	}
	for _, target := range []string{"server.js", "node_modules/.bin/tool"} {
		read := exec.Command("/usr/bin/test", "-r", filepath.Join(directory, first.Directory, target))
		read.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 60000, Gid: 60000, Groups: []uint32{}}}
		if output, err := read.CombinedOutput(); err != nil {
			t.Fatalf("runtime cannot read %s: %v %s", target, err, output)
		}
	}
	for _, target := range []string{"server.js", "new-file"} {
		write := exec.Command("/bin/sh", "-c", `printf forbidden > "$1"`, "write-probe", filepath.Join(directory, first.Directory, target))
		write.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 60000, Gid: 60000, Groups: []uint32{}}}
		if output, err := write.CombinedOutput(); err == nil {
			t.Fatalf("runtime modified sealed release: %s", output)
		}
	}
}
func TestInstallReleaseRejectsAndCleansInvalidInputs(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, extra       string
		badDigest, cancel bool
	}{
		{name: "digest", badDigest: true}, {name: "traversal", extra: "../outside"}, {name: "secret", extra: ".env"}, {name: "cancelled", cancel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			directory, root := installRoot(t)
			data, digest := installArchive(t, tc.extra)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			if tc.badDigest {
				digest = poolAssignment(7).OperationID
			}
			result, err := InstallRelease(ctx, data, digest, root)
			if err == nil || result.Directory != "" {
				t.Fatal("invalid input published", result, err)
			}
			if tc.cancel && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if !tc.cancel && !errors.Is(err, nodeartifact.ErrArtifact) {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(directory)
			if err != nil || len(entries) != 0 {
				t.Fatal("failed install residue", entries, err)
			}
		})
	}
}
func TestInstallReleaseRejectsWritableOrUnownedRoot(t *testing.T) {
	t.Parallel()
	directory, root := installRoot(t)
	data, digest := installArchive(t, "")
	if err := os.Chmod(directory, 0777); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallRelease(context.Background(), data, digest, root); !errors.Is(err, ErrAssignment) {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(directory, 60000, 60000); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallRelease(context.Background(), data, digest, root); !errors.Is(err, ErrAssignment) {
		t.Fatal(err)
	}
	if _, err := InstallRelease(context.Background(), data, digest, nil); !errors.Is(err, ErrAssignment) {
		t.Fatal(err)
	}
}

func TestInstallReleaseConnectsPoolReceiptAndStartClaim(t *testing.T) {
	t.Parallel()
	directory, root := installRoot(t)
	ctx := context.Background()
	config := poolConfig()
	config.Architecture = runtime.GOARCH
	p := openPool(t, filepath.Join(t.TempDir(), "private", "pool.db"), config)
	a := poolAssignment(1)
	a.Architecture = runtime.GOARCH
	r, err := p.Reserve(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.ClaimStart(ctx, a.OperationID); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	r, err = p.PrepareRelease(ctx, a.OperationID, reservationData, LinuxInstaller{Releases: root})
	if err != nil || r.Installed == nil {
		t.Fatal(r, err)
	}
	info, err := os.Stat(filepath.Join(directory, a.ReleaseDirectory, "package.json"))
	if err != nil || info.Mode().Perm() != 0444 {
		t.Fatal(info, err)
	}
	claimed, err := p.ClaimStart(ctx, a.OperationID)
	if err != nil || claimed.Installed == nil {
		t.Fatal(claimed, err)
	}
	service, err := Render(claimed.Assignment)
	if err != nil || !strings.Contains(service.Unit, "/releases/"+a.ReleaseDirectory) {
		t.Fatal(service, err)
	}
	if _, err = p.PrepareRelease(ctx, a.OperationID, reservationData, LinuxInstaller{Releases: root}); !errors.Is(err, ErrConflict) {
		t.Fatal("started release reinstalled", err)
	}
	// The primitive itself also refuses replacement, even if called outside Pool.
	if result, err := (LinuxInstaller{Releases: root}).InstallNodeRelease(ctx, claimed.Assignment, reservationData); !errors.Is(err, os.ErrExist) || result.Directory != "" {
		t.Fatal("existing release replaced", result, err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 {
		t.Fatal("unexpected installation residue", entries, err)
	}
}

func TestInstallReleaseRecoversPublishedProcessExit(t *testing.T) {
	if base := os.Getenv("NODE_PUBLISHED_EXIT_FIXTURE"); base != "" {
		config := poolConfig()
		config.Architecture = runtime.GOARCH
		p, err := OpenPool(filepath.Join(base, "private", "pool.db"), config)
		if err != nil {
			t.Fatal(err)
		}
		root, err := os.OpenRoot(filepath.Join(base, "releases"))
		if err != nil {
			t.Fatal(err)
		}
		a := poolAssignment(1)
		a.Architecture = runtime.GOARCH
		if _, err = p.Reserve(context.Background(), a); err != nil {
			t.Fatal(err)
		}
		installer := fixtureInstaller(func(ctx context.Context, a Assignment, data []byte) (nodeartifact.Release, error) {
			r, e := (LinuxInstaller{Releases: root}).InstallNodeRelease(ctx, a, data)
			if e != nil {
				return r, e
			}
			os.Exit(0)
			return r, nil
		})
		if _, err = p.PrepareRelease(context.Background(), a.OperationID, reservationData, installer); err != nil {
			t.Fatal(err)
		}
		t.Fatal("child did not exit after publication")
	}
	if os.Geteuid() != 0 {
		t.Skip("requires root inside disposable Linux VM")
	}
	t.Parallel()
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, "releases"), 0755); err != nil {
		t.Fatal(err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestInstallReleaseRecoversPublishedProcessExit$")
	child.Env = append(os.Environ(), "NODE_PUBLISHED_EXIT_FIXTURE="+base)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("child: %v %s", err, output)
	}
	config := poolConfig()
	config.Architecture = runtime.GOARCH
	p := openPool(t, filepath.Join(base, "private", "pool.db"), config)
	ctx := t.Context()
	a := poolAssignment(1)
	a.Architecture = runtime.GOARCH
	before, err := p.Lookup(ctx, a.OperationID)
	if err != nil || !before.InstallationAttempted || before.Installed != nil {
		t.Fatal(before, err)
	}
	if _, err = p.ClaimStart(ctx, a.OperationID); !errors.Is(err, ErrConflict) {
		t.Fatal("lost receipt allowed start", err)
	}
	root, err := os.OpenRoot(filepath.Join(base, "releases"))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	recovered, err := p.ReconcileInstallation(ctx, a.OperationID, reservationData, LinuxInstaller{Releases: root})
	if err != nil || recovered.Installed == nil || recovered.Installed.Manifest.SHA256 != a.ArtifactSHA256 {
		t.Fatal(recovered, err)
	}
	if _, err = p.ClaimStart(ctx, a.OperationID); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(base, "releases"))
	if err != nil || len(entries) != 1 || entries[0].Name() != a.ReleaseDirectory {
		t.Fatal("recovery reinstalled files", entries, err)
	}
}
func TestInstallReleaseInspectionRejectsOwnershipAndHardLinks(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"owner", "hard_link", "writable", "contents", "directory_link"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			directory, root := installRoot(t)
			ctx := t.Context()
			a := poolAssignment(1)
			a.UID = 60000
			a.Port = 31001
			a.Architecture = runtime.GOARCH
			installer := LinuxInstaller{Releases: root}
			if _, err := installer.InstallNodeRelease(ctx, a, reservationData); err != nil {
				t.Fatal(err)
			}
			if _, err := installer.ObserveNodeInstallation(ctx, a, reservationData); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(directory, a.ReleaseDirectory, "package.json")
			switch kind {
			case "owner":
				if err := os.Chown(target, 60000, 60000); err != nil {
					t.Fatal(err)
				}
			case "hard_link":
				if err := os.Link(target, filepath.Join(t.TempDir(), "alias")); err != nil {
					t.Fatal(err)
				}
			case "writable":
				if err := os.Chmod(target, 0644); err != nil {
					t.Fatal(err)
				}
			case "contents":
				if err := os.WriteFile(target, []byte("changed"), 0444); err != nil {
					t.Fatal(err)
				}
			case "directory_link":
				if err := os.Rename(filepath.Join(directory, a.ReleaseDirectory), filepath.Join(directory, "other")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("other", filepath.Join(directory, a.ReleaseDirectory)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := installer.ObserveNodeInstallation(ctx, a, reservationData); err == nil {
				t.Fatal("unsafe installed tree accepted")
			}
		})
	}
}
