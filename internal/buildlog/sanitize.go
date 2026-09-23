// Package buildlog bounds untrusted build output before persistence and display.
package buildlog

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaxBytes = 1536

var ansi = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
var sensitive = regexp.MustCompile(`(?i)(authorization|bearer\s|password|passwd|secret|token|api[_-]?key|private.key|://[^\s/]+:[^\s/]+@|\b(?:sk|rk)_(?:live|test)_|\bgh[pousr]_)`)

// Sanitize is defense in depth, not a guarantee that arbitrary secrets printed
// by customer code can be recognized. Logs stay behind project write access.
func Sanitize(raw string) string {
	if len(raw) > 16384 {
		raw = raw[len(raw)-16384:]
	}
	raw = strings.ToValidUTF8(raw, "")
	raw = ansi.ReplaceAllString(raw, "")
	var lines []string
	privateKey := false
	for _, line := range strings.Split(raw, "\n") {
		if strings.Contains(line, "BEGIN ") && strings.Contains(line, "PRIVATE KEY") {
			privateKey = true
		}
		if privateKey {
			if strings.Contains(line, "END ") && strings.Contains(line, "PRIVATE KEY") {
				privateKey = false
			}
			lines = append(lines, "[redacted]")
			continue
		}
		line = strings.Map(func(r rune) rune {
			if r == '\t' || !unicode.IsControl(r) && !unicode.Is(unicode.Cf, r) {
				return r
			}
			return -1
		}, line)
		if sensitive.MatchString(line) {
			line = "[redacted potentially sensitive output]"
		}
		lines = append(lines, line)
	}
	result := strings.TrimSpace(strings.Join(lines, "\n"))
	if len(result) > MaxBytes {
		result = result[len(result)-MaxBytes+len("[earlier output omitted]\n"):]
		for len(result) > 0 && !utf8.RuneStart(result[0]) {
			result = result[1:]
		}
		result = "[earlier output omitted]\n" + result
	}
	return result
}

// Tail bounds memory while accepting all writes from a subprocess pipe.
type Tail struct{ Data []byte }

func (t *Tail) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) >= 16384 {
		t.Data = append(t.Data[:0], p[len(p)-16384:]...)
	} else {
		t.Data = append(t.Data, p...)
		if len(t.Data) > 16384 {
			t.Data = append([]byte(nil), t.Data[len(t.Data)-16384:]...)
		}
	}
	return n, nil
}
