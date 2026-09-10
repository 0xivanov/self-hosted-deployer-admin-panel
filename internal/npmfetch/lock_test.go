package npmfetch

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

func sourceFixture(t *testing.T, lock string, shrinkwrap bool) ([]byte, string) {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	entries := map[string]string{"package.json": `{"scripts":{"start":"node index.js"}}`, "package-lock.json": lock}
	if shrinkwrap {
		entries["npm-shrinkwrap.json"] = `{"lockfileVersion":3}`
	}
	for name, data := range entries {
		w, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write([]byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b.Bytes())
	return b.Bytes(), hex.EncodeToString(sum[:])
}
func TestLockPlansPinnedRegistryDependencies(t *testing.T) {
	t.Parallel()
	entry := map[string]any{"resolved": "https://registry.npmjs.org/example/-/example-1.tgz", "integrity": integrity("package")}
	raw, _ := json.Marshal(map[string]any{"lockfileVersion": 3, "packages": map[string]any{"": map[string]any{}, "node_modules/example": entry, "node_modules/parent/node_modules/example": entry}})
	source, digest := sourceFixture(t, string(raw), false)
	set, err := FromSource(t.Context(), source, digest)
	if err != nil || set.SourceSHA256 != digest || len(set.Tarballs) != 1 {
		t.Fatal(set, err)
	}
	if _, err = FromSource(t.Context(), source, "wrong"); err == nil {
		t.Fatal("source mismatch accepted")
	}
	source, digest = sourceFixture(t, string(raw), true)
	if _, err = FromSource(t.Context(), source, digest); err == nil {
		t.Fatal("shrinkwrap precedence ignored")
	}
}
func TestLockRejectsUnsupportedAndConflictingDependencies(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		version int
		entry   map[string]any
	}{
		{"legacy", 1, map[string]any{}},
		{"local", 3, map[string]any{"resolved": "file:../private", "integrity": integrity("x")}},
		{"git", 3, map[string]any{"resolved": "git+https://github.com/a/b", "integrity": integrity("x")}},
		{"link", 3, map[string]any{"link": true}},
		{"no integrity", 3, map[string]any{"resolved": "https://registry.npmjs.org/a/-/a.tgz"}},
		{"weak integrity", 3, map[string]any{"resolved": "https://registry.npmjs.org/a/-/a.tgz", "integrity": "sha1-aaaa"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"lockfileVersion": tc.version, "packages": map[string]any{"": map[string]any{}, "node_modules/a": tc.entry}})
			source, digest := sourceFixture(t, string(raw), false)
			if _, err := FromSource(t.Context(), source, digest); err == nil {
				t.Fatal("unsupported dependency accepted")
			}
		})
	}
	raw, _ := json.Marshal(map[string]any{"lockfileVersion": 3, "packages": map[string]any{"": map[string]any{}, "node_modules/a": map[string]any{"resolved": "https://registry.npmjs.org/a/-/a.tgz", "integrity": integrity("one")}, "node_modules/b": map[string]any{"resolved": "https://registry.npmjs.org/a/-/a.tgz", "integrity": integrity("two")}}})
	source, digest := sourceFixture(t, string(raw), false)
	if _, err := FromSource(t.Context(), source, digest); err == nil {
		t.Fatal("conflicting hashes accepted")
	}
}
