//go:build integration

package nodebuild

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func privateJobs(t *testing.T) (*os.Root, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return root, dir
}
func TestExtractNodeSourcePrivateAndIndependent(t *testing.T) {
	t.Parallel()
	root, dir := privateJobs(t)
	source, digest := archiveFixture(t, `{"scripts":{"start":"node server.js"}}`)
	first, err := ExtractSource(t.Context(), source, digest, root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ExtractSource(t.Context(), source, digest, root)
	if err != nil {
		t.Fatal(err)
	}
	if first.Directory == second.Directory || first.SHA256 != digest {
		t.Fatal(first, second)
	}
	data, err := root.ReadFile(first.Directory + "/server.js")
	if err != nil || !strings.Contains(string(data), "must not execute") {
		t.Fatal(string(data), err)
	}
	err = filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode().Perm()&0077 != 0 {
			t.Errorf("non-private extracted path %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
func TestExtractNodeSourceRejectsBeforeWriting(t *testing.T) {
	t.Parallel()
	root, dir := privateJobs(t)
	source, digest := archiveFixture(t, `{"scripts":{"start":"node server.js"}}`)
	if _, err := ExtractSource(t.Context(), source, "wrong", root); err == nil {
		t.Fatal("digest mismatch accepted")
	}
	if _, err := ExtractSource(t.Context(), source, digest, nil); err == nil {
		t.Fatal("nil root accepted")
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := ExtractSource(t.Context(), source, digest, root); err == nil {
		t.Fatal("public root accepted")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	// Traversal and symlinks must be rejected even when the supplied digest matches.
	for _, tc := range []struct {
		name string
		mode os.FileMode
	}{{"../escape", 0600}, {"link", os.ModeSymlink | 0777}} {
		var b bytes.Buffer
		z := zip.NewWriter(&b)
		for _, f := range []struct {
			name, body string
			mode       os.FileMode
		}{{"package.json", `{"scripts":{"start":"node server.js"}}`, 0600}, {"package-lock.json", `{"lockfileVersion":3}`, 0600}, {tc.name, "../../outside", tc.mode}} {
			h := &zip.FileHeader{Name: f.name}
			h.SetMode(f.mode)
			w, err := z.CreateHeader(h)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = w.Write([]byte(f.body)); err != nil {
				t.Fatal(err)
			}
		}
		if err := z.Close(); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(b.Bytes())
		if _, err := ExtractSource(t.Context(), b.Bytes(), hex.EncodeToString(sum[:]), root); err == nil {
			t.Fatal("unsafe source accepted", tc.name)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatal(entries, err)
	}
}

// Cancel after the first file is created to exercise partial-directory cleanup
// deterministically, rather than relying on scheduler timing.
type extractionCancel struct {
	context.Context
	root string
}

func (c extractionCancel) Err() error {
	matches, _ := filepath.Glob(filepath.Join(c.root, "source-*", "package.json"))
	if len(matches) > 0 {
		return context.Canceled
	}
	return nil
}
func TestExtractCancellationRemovesPartialSource(t *testing.T) {
	t.Parallel()
	root, dir := privateJobs(t)
	source, digest := archiveFixture(t, `{"scripts":{"start":"node server.js"}}`)
	result, err := ExtractSource(extractionCancel{t.Context(), dir}, source, digest, root)
	if !errors.Is(err, context.Canceled) || result.Directory != "" {
		t.Fatal(result, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatal(entries, err)
	}
}

func TestExtractPreservesOnlyOwnerExecution(t *testing.T) {
	t.Parallel()
	root, _ := privateJobs(t)
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for _, f := range []struct {
		name, body string
		mode       os.FileMode
	}{
		{"package.json", `{"scripts":{"start":"./bin/start"}}`, 0644},
		{"package-lock.json", `{"lockfileVersion":3}`, 0644},
		{"bin/start", "#!/bin/sh\nexit 99\n", 0755},
	} {
		h := &zip.FileHeader{Name: f.name}
		h.SetMode(f.mode)
		w, err := z.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write([]byte(f.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b.Bytes())
	source, err := ExtractSource(t.Context(), b.Bytes(), hex.EncodeToString(sum[:]), root)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path string
		mode os.FileMode
	}{{"bin", 0700}, {"bin/start", 0700}, {"package.json", 0600}} {
		info, err := root.Stat(source.Directory + "/" + tc.path)
		if err != nil || info.Mode().Perm() != tc.mode {
			t.Fatal(tc.path, info, err)
		}
	}
}
