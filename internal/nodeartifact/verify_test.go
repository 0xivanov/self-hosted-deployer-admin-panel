//go:build integration

package nodeartifact

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"testing"
)

func sealedFixture(t *testing.T) (*os.Root, []byte, string) {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	parent, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { parent.Close() })
	data, digest := fixture(t, fixtureEntry{"node_modules/pkg/bin.js", "console.log('ready')", 0700}, fixtureEntry{"node_modules/.bin/pkg", "../pkg/bin.js", os.ModeSymlink | 0777})
	release, err := Extract(t.Context(), data, digest, parent)
	if err != nil {
		t.Fatal(err)
	}
	root, err := parent.OpenRoot(release.Directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		fs.WalkDir(root.FS(), ".", func(name string, d fs.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if d.IsDir() {
				return root.Chmod(name, 0700)
			}
			return nil
		})
		root.Close()
	})
	err = fs.WalkDir(root.FS(), ".", func(name string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		mode := os.FileMode(0444)
		if d.IsDir() || name == "node_modules/pkg/bin.js" {
			mode = 0555
		}
		return root.Chmod(name, mode)
	})
	if err != nil {
		t.Fatal(err)
	}
	return root, data, digest
}
func TestVerifySealedExactTree(t *testing.T) {
	t.Parallel()
	root, data, digest := sealedFixture(t)
	m, err := VerifySealed(t.Context(), data, digest, root)
	if err != nil || m.SHA256 != digest || m.Files != 3 {
		t.Fatal(m, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = VerifySealed(ctx, data, digest, root); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err = VerifySealed(t.Context(), data, digest, nil); !errors.Is(err, ErrArtifact) {
		t.Fatal(err)
	}
}
func TestVerifySealedRejectsDrift(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		change func(*os.Root) error
	}{
		{"same_length_bytes", func(r *os.Root) error {
			body, e := r.ReadFile("package.json")
			if e != nil {
				return e
			}
			if e = r.Chmod("package.json", 0600); e != nil {
				return e
			}
			if e = r.WriteFile("package.json", bytes.Repeat([]byte("x"), len(body)), 0600); e != nil {
				return e
			}
			return r.Chmod("package.json", 0444)
		}},
		{"writable_file", func(r *os.Root) error { return r.Chmod("package.json", 0644) }},
		{"lost_executable", func(r *os.Root) error { return r.Chmod("node_modules/pkg/bin.js", 0444) }},
		{"writable_root", func(r *os.Root) error { return r.Chmod(".", 0755) }},
		{"extra_file", func(r *os.Root) error {
			if e := r.Chmod(".", 0755); e != nil {
				return e
			}
			if e := r.WriteFile("extra", []byte("x"), 0444); e != nil {
				return e
			}
			return r.Chmod(".", 0555)
		}},
		{"extra_directory", func(r *os.Root) error {
			if e := r.Chmod(".", 0755); e != nil {
				return e
			}
			if e := r.Mkdir("extra", 0555); e != nil {
				return e
			}
			return r.Chmod(".", 0555)
		}},
		{"missing_file", func(r *os.Root) error {
			if e := r.Chmod("node_modules/pkg", 0755); e != nil {
				return e
			}
			if e := r.Remove("node_modules/pkg/bin.js"); e != nil {
				return e
			}
			return r.Chmod("node_modules/pkg", 0555)
		}},
		{"changed_link", func(r *os.Root) error {
			if e := r.Chmod("node_modules/.bin", 0755); e != nil {
				return e
			}
			if e := r.Remove("node_modules/.bin/pkg"); e != nil {
				return e
			}
			if e := r.Symlink("../../package.json", "node_modules/.bin/pkg"); e != nil {
				return e
			}
			return r.Chmod("node_modules/.bin", 0555)
		}},
		{"file_to_link", func(r *os.Root) error {
			if e := r.Chmod(".", 0755); e != nil {
				return e
			}
			if e := r.Remove("package.json"); e != nil {
				return e
			}
			if e := r.Symlink("node_modules/pkg/bin.js", "package.json"); e != nil {
				return e
			}
			return r.Chmod(".", 0555)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root, data, digest := sealedFixture(t)
			if err := tc.change(root); err != nil {
				t.Fatal(err)
			}
			if _, err := VerifySealed(t.Context(), data, digest, root); err == nil {
				t.Fatal("drift accepted")
			}
		})
	}
}
