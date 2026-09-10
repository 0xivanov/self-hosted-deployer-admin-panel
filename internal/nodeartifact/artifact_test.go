package nodeartifact

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"os"
	"testing"
)

type fixtureEntry struct {
	name, body string
	mode       os.FileMode
}

func fixture(t *testing.T, extra ...fixtureEntry) ([]byte, string) {
	t.Helper()
	entries := append([]fixtureEntry{{"package.json", `{"scripts":{"start":"node server.js"}}`, 0600}}, extra...)
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for _, entry := range entries {
		h := &zip.FileHeader{Name: entry.name, Method: zip.Store}
		h.SetMode(entry.mode)
		w, err := z.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write([]byte(entry.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	data := b.Bytes()
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:])
}
func TestValidateNodeDependencyLinks(t *testing.T) {
	t.Parallel()
	data, digest := fixture(t, fixtureEntry{"node_modules/pkg/bin.js", "console.log('ready')", 0755}, fixtureEntry{"node_modules/.bin/pkg", "../pkg/bin.js", os.ModeSymlink | 0777}, fixtureEntry{"launcher", "node_modules/.bin/pkg", os.ModeSymlink | 0777})
	m, err := Validate(t.Context(), data, digest)
	if err != nil || m.SHA256 != digest || m.Files != 4 {
		t.Fatal(m, err)
	}
}
func TestRejectUnsafeNodeArtifacts(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		entries []fixtureEntry
	}{
		{"traversal", []fixtureEntry{{"../escape", "x", 0600}}},
		{"absolute", []fixtureEntry{{"/escape", "x", 0600}}},
		{"backslash", []fixtureEntry{{`a\b`, "x", 0600}}},
		{"duplicate", []fixtureEntry{{"package.json", "{}", 0600}}},
		{"secret", []fixtureEntry{{".env.production", "secret", 0600}}},
		{"nested_config", []fixtureEntry{{"node_modules/pkg/.npmrc", "secret", 0600}}},
		{"socket", []fixtureEntry{{"socket", "", os.ModeSocket}}},
		{"dangling_link", []fixtureEntry{{"link", "missing", os.ModeSymlink}}},
		{"external_link", []fixtureEntry{{"link", "../outside", os.ModeSymlink}}},
		{"absolute_link", []fixtureEntry{{"link", "/etc/passwd", os.ModeSymlink}}},
		{"link_cycle", []fixtureEntry{{"a", "b", os.ModeSymlink}, {"b", "a", os.ModeSymlink}}},
		{"link_parent", []fixtureEntry{{"a/file", "x", 0600}, {"a", "package.json", os.ModeSymlink}}},
		{"file_parent", []fixtureEntry{{"a/file", "x", 0600}, {"a", "x", 0600}}},
		{"directory_link", []fixtureEntry{{"a/file", "x", 0600}, {"link", "a", os.ModeSymlink}}},
		{"ambiguous_link", []fixtureEntry{{"link", "a/../package.json", os.ModeSymlink}}},
		{"intermediate_link", []fixtureEntry{{"a", "package.json", os.ModeSymlink}, {"link", "a/child", os.ModeSymlink}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, digest := fixture(t, tc.entries...)
			if _, err := Validate(t.Context(), data, digest); err == nil {
				t.Fatal("unsafe artifact accepted")
			}
		})
	}
}
func TestArtifactIntegrityAndCancellation(t *testing.T) {
	t.Parallel()
	data, digest := fixture(t, fixtureEntry{"server.js", "console.log('unique corruption target')", 0600})
	if _, err := Validate(t.Context(), data, "wrong"); err == nil {
		t.Fatal("wrong digest accepted")
	}
	changed := bytes.Clone(data)
	at := bytes.Index(changed, []byte("unique corruption target"))
	if at < 0 {
		t.Fatal("fixture missing")
	}
	changed[at] = 'X'
	if _, err := Validate(t.Context(), changed, digest); err == nil {
		t.Fatal("changed bytes accepted")
	}
	sum := sha256.Sum256(changed)
	if _, err := Validate(t.Context(), changed, hex.EncodeToString(sum[:])); err == nil {
		t.Fatal("bad ZIP CRC accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Validate(ctx, data, digest); err == nil {
		t.Fatal("cancelled validation succeeded")
	}
}

func TestArtifactDeclaredFileLimit(t *testing.T) {
	t.Parallel()
	data, _ := fixture(t)
	at := bytes.Index(data, []byte{0x50, 0x4b, 0x01, 0x02})
	if at < 0 {
		t.Fatal("missing ZIP directory")
	}
	binary.LittleEndian.PutUint32(data[at+24:at+28], MaxFile+1)
	sum := sha256.Sum256(data)
	if _, err := Validate(t.Context(), data, hex.EncodeToString(sum[:])); err == nil {
		t.Fatal("oversized file accepted")
	}
}
