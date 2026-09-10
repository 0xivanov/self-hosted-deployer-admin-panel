//go:build integration

package npmfetch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyBundleRejectsChangedOrIncompleteStorage(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"valid", "source", "manifest", "content", "missing", "extra", "symlink", "public"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Chmod(dir, 0700); err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			source, digest := bundleSource(t)
			client := downloadFunc(func(_ context.Context, tarball Tarball) ([]byte, error) {
				if tarball.Integrity == integrity("one") {
					return []byte("one"), nil
				}
				return []byte("two"), nil
			})
			bundle, err := DownloadBundle(t.Context(), source, digest, root, client)
			if err != nil {
				t.Fatal(err)
			}
			manifest, err := VerifyBundle(t.Context(), root, bundle, digest)
			if err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(dir, bundle.Directory, manifest.Tarballs[0].File)
			switch mode {
			case "source":
				digest = strings.Repeat("f", 64)
			case "manifest":
				bundle.ManifestSHA256 = strings.Repeat("f", 64)
			case "content":
				err = os.WriteFile(file, []byte("bad"), 0600)
			case "missing":
				err = os.Remove(filepath.Join(dir, bundle.Directory, "bundle.json"))
			case "extra":
				err = os.WriteFile(filepath.Join(dir, bundle.Directory, "bundle.pending"), []byte("partial"), 0600)
			case "symlink":
				if err = os.Remove(file); err == nil {
					err = os.Symlink("../outside", file)
				}
			case "public":
				err = os.Chmod(file, 0644)
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = VerifyBundle(t.Context(), root, bundle, digest)
			if mode == "valid" && err != nil {
				t.Fatal(err)
			}
			if mode != "valid" && err == nil {
				t.Fatal("altered bundle accepted", mode)
			}
		})
	}
}
