//go:build integration

package nodeartifact

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractPrivateNodeReleaseWithCommandLink(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	data, digest := fixture(t, fixtureEntry{"node_modules/pkg/bin.js", "console.log('ready')", os.ModeSetuid | 0777}, fixtureEntry{"node_modules/.bin/pkg", "../pkg/bin.js", os.ModeSymlink | 0777})
	release, err := Extract(t.Context(), data, digest, root)
	if err != nil {
		t.Fatal(err)
	}
	staged, err := root.OpenRoot(release.Directory)
	if err != nil {
		t.Fatal(err)
	}
	defer staged.Close()
	body, err := staged.ReadFile("node_modules/.bin/pkg")
	if err != nil || string(body) != "console.log('ready')" {
		t.Fatal(string(body), err)
	}
	for _, tc := range []struct {
		name string
		mode os.FileMode
	}{{".", 0700}, {"node_modules", 0700}, {"package.json", 0600}, {"node_modules/pkg/bin.js", 0700}} {
		info, e := staged.Stat(tc.name)
		if e != nil || info.Mode().Perm() != tc.mode || info.Mode()&os.ModeSetuid != 0 {
			t.Fatal(tc.name, info, e)
		}
	}
	link, err := staged.Readlink("node_modules/.bin/pkg")
	if err != nil || link != "../pkg/bin.js" {
		t.Fatal(link, err)
	}
	// A new staging operation must leave the old immutable release in place.
	second, err := Extract(t.Context(), data, digest, root)
	if err != nil || second.Directory == release.Directory {
		t.Fatal(second, err)
	}
	if _, err = os.Stat(filepath.Join(directory, release.Directory, "package.json")); err != nil {
		t.Fatal(err)
	}
}
func TestInvalidArtifactLeavesNoRelease(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	data, digest := fixture(t, fixtureEntry{"link", "../outside", os.ModeSymlink})
	if _, err = Extract(t.Context(), data, digest, root); err == nil {
		t.Fatal("unsafe artifact extracted")
	}
	files, err := os.ReadDir(directory)
	if err != nil || len(files) != 0 {
		t.Fatal(files, err)
	}
	if err = os.Chmod(directory, 0755); err != nil {
		t.Fatal(err)
	}
	data, digest = fixture(t)
	if _, err = Extract(t.Context(), data, digest, root); err == nil {
		t.Fatal("public release root accepted")
	}
}

// Cancel only after a file has actually been created in the staging directory.
// This covers cleanup after writes begin, rather than pre-validation rejection.
type cancelAfterFile struct {
	context.Context
	directory string
}

func (c cancelAfterFile) Err() error {
	releases, _ := os.ReadDir(c.directory)
	for _, release := range releases {
		if _, err := os.Stat(filepath.Join(c.directory, release.Name(), "package.json")); err == nil {
			return context.Canceled
		}
	}
	return c.Context.Err()
}
func TestExtractionCancellationRemovesPartialRelease(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	data, digest := fixture(t)
	result, err := Extract(cancelAfterFile{t.Context(), directory}, data, digest, root)
	if !errors.Is(err, context.Canceled) || result.Directory != "" {
		t.Fatal(result, err)
	}
	files, err := os.ReadDir(directory)
	if err != nil || len(files) != 0 {
		t.Fatal("partial release remains", files, err)
	}
}
