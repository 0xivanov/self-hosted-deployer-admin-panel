package portal

import (
	"bytes"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadContainerCredentialKey(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, 32)
	for _, tc := range []struct {
		name, data string
		mode       os.FileMode
		valid      bool
	}{
		{"valid", hex.EncodeToString(key) + "\n", 0600, true},
		{"read only", hex.EncodeToString(key), 0400, true},
		{"shared", hex.EncodeToString(key), 0640, false},
		{"short", "abcd", 0600, false},
		{"oversized", strings.Repeat("a", 129), 0600, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "key")
			if err := os.WriteFile(path, []byte(tc.data), tc.mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, tc.mode); err != nil {
				t.Fatal(err)
			}
			got, err := LoadContainerCredentialKey(path)
			if tc.valid {
				if err != nil || !bytes.Equal(got, key) {
					t.Fatal("valid key rejected")
				}
			} else if err == nil {
				t.Fatal("unsafe key accepted")
			}
		})
	}
	t.Run("symlink", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "key")
		link := filepath.Join(dir, "link")
		if err := os.WriteFile(path, []byte(hex.EncodeToString(key)), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(path, link); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadContainerCredentialKey(link); err == nil {
			t.Fatal("symlink accepted")
		}
	})
}
