//go:build integration

package npmfetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type downloadFunc func(context.Context, Tarball) ([]byte, error)

func (f downloadFunc) Fetch(ctx context.Context, t Tarball) ([]byte, error) { return f(ctx, t) }
func bundleSource(t *testing.T) ([]byte, string) {
	raw, _ := json.Marshal(map[string]any{"lockfileVersion": 3, "packages": map[string]any{"": map[string]any{}, "node_modules/a": map[string]any{"resolved": "https://registry.npmjs.org/a/-/a.tgz", "integrity": integrity("one")}, "node_modules/b": map[string]any{"resolved": "https://registry.npmjs.org/b/-/b.tgz", "integrity": integrity("two")}}})
	return sourceFixture(t, string(raw), false)
}
func TestBundlePersistenceAndPrivateManifest(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
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
	if err = root.Close(); err != nil {
		t.Fatal(err)
	}
	root, err = os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	raw, err := root.ReadFile(bundle.Directory + "/bundle.json")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != bundle.ManifestSHA256 {
		t.Fatal("manifest digest mismatch")
	}
	var manifest Manifest
	if err = json.Unmarshal(raw, &manifest); err != nil || manifest.SourceSHA256 != digest || len(manifest.Tarballs) != 2 {
		t.Fatal(manifest, err)
	}
	for _, item := range manifest.Tarballs {
		content, err := root.ReadFile(bundle.Directory + "/" + item.File)
		if err != nil || integrity(string(content)) != item.Integrity || len(content) != int(item.Bytes) {
			t.Fatal(item, err)
		}
	}
	if err = filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err == nil && info.Mode().Perm()&0077 != 0 {
			t.Errorf("non-private path %s", path)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = root.Stat(bundle.Directory + "/bundle.pending"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("temporary manifest remains", err)
	}
}
func TestBundleFailureLeavesNoPartialOutput(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"provider", "integrity", "quota", "cancel"} {
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
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			client := downloadFunc(func(context.Context, Tarball) ([]byte, error) {
				calls++
				if calls == 1 {
					return []byte("one"), nil
				}
				switch mode {
				case "provider":
					return nil, errors.New("failed")
				case "integrity":
					return []byte("bad"), nil
				case "cancel":
					cancel()
				}
				return []byte("two"), nil
			})
			budget := int64(MaxBundleBytes)
			if mode == "quota" {
				budget = 5
			}
			bundle, err := downloadBundle(ctx, source, digest, root, client, budget)
			if err == nil || bundle.Directory != "" {
				t.Fatal(bundle, err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 0 {
				t.Fatal(entries, err)
			}
		})
	}
}
