package registryimage

import (
	"strings"
	"testing"
)

func TestParseRejectsUnsafeOrUnsupportedReferences(t *testing.T) {
	tests := []string{
		"ghcr.io/org/app:latest/../../x",
		"ghcr.io/org/app:tag?x=1",
		"ghcr.io/org/app:tag#fragment",
		"ghcr.io/org/app:tag\\x",
		"ghcr.io/org/app:tag with-space",
		"ghcr.io/org/app:tag@sha256:" + strings.Repeat("a", 64),
		"evil.example/org/app:tag",
		"localhost/org/app:tag",
		"ghcr.io/org//app:tag",
		"ghcr.io/org/App:tag",
	}
	for _, raw := range tests {
		if _, err := Parse(raw); err == nil {
			t.Errorf("Parse(%q) accepted unsafe or unsupported reference", raw)
		}
	}
	for _, raw := range []string{"ubuntu:latest", "ghcr.io/org/app:release_1", "ghcr.io/org/app@sha256:" + strings.Repeat("a", 64)} {
		if got, err := Parse(raw); err != nil || got.String() == "" {
			t.Errorf("Parse(%q) rejected valid reference: %#v, %v", raw, got, err)
		}
	}
}
