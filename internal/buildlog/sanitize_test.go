package buildlog

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitizeOutput(t *testing.T) {
	raw := "\x1b[31mError: missing module\x1b[0m\nAPI_TOKEN=do-not-expose\nhttps://user:pass@host/path\n-----BEGIN PRIVATE KEY-----\nprivate-bytes\n-----END PRIVATE KEY-----\nnormal build failure"
	result := Sanitize(raw)
	for _, secret := range []string{"do-not-expose", "user:pass", "private-bytes", "\x1b"} {
		if strings.Contains(result, secret) {
			t.Fatalf("leaked %q", secret)
		}
	}
	if !strings.Contains(result, "Error: missing module") || !strings.Contains(result, "normal build failure") {
		t.Fatal(result)
	}
	for _, raw := range []string{strings.Repeat("é", 20000), strings.Repeat("a", 20000)} {
		out := Sanitize(raw)
		if len(out) > MaxBytes || !utf8.ValidString(out) {
			t.Fatal("unbounded or invalid output")
		}
	}
}
func TestTailBounds(t *testing.T) {
	var tail Tail
	tail.Write([]byte(strings.Repeat("a", 20000)))
	tail.Write([]byte("last"))
	if len(tail.Data) != 16384 || !strings.HasSuffix(string(tail.Data), "last") {
		t.Fatal("tail incorrect")
	}
}
