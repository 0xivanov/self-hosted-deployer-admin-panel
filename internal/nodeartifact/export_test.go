//go:build integration

package nodeartifact

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestExportBuildTree(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for name, body := range map[string]string{"package.json": `{"scripts":{"start":"node server.js"}}`, "server.js": "console.log('built');"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("server.js", filepath.Join(dir, "entry.js")); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	data, manifest, err := Export(t.Context(), root)
	if err != nil || manifest.Files != 3 {
		t.Fatal(manifest, err)
	}
	second, again, err := Export(t.Context(), root)
	if err != nil || !bytes.Equal(data, second) || manifest != again {
		t.Fatal("export is not reproducible", err)
	}
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range z.File {
		if f.Name == "server.js" && f.Mode().Perm() != 0755 {
			t.Fatal("executable permission lost")
		}
		if f.Name == "entry.js" && f.Mode().Type() != os.ModeSymlink {
			t.Fatal("link not preserved")
		}
	}
}

func TestExportRejectsUnsafeOutput(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"escape", "dangling", "secret", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"scripts":{"start":"node server.js"}}`), 0600); err != nil {
				t.Fatal(err)
			}
			var err error
			switch mode {
			case "escape":
				err = os.Symlink("../outside", filepath.Join(dir, "bad"))
			case "dangling":
				err = os.Symlink("missing", filepath.Join(dir, "bad"))
			case "secret":
				err = os.WriteFile(filepath.Join(dir, ".env"), []byte("fixture"), 0600)
			case "oversized":
				var f *os.File
				f, err = os.Create(filepath.Join(dir, "large"))
				if err == nil {
					err = f.Truncate(MaxFile + 1)
					f.Close()
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			data, _, err := Export(t.Context(), root)
			if err == nil || data != nil {
				t.Fatal("unsafe artifact exported", err)
			}
		})
	}
}
