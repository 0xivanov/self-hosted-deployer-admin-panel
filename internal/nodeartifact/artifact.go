// Package nodeartifact validates and stages built Node release archives. It never
// executes their contents. Builders must be stopped before export and runtimes
// must isolate customer code before starting a staged release.
package nodeartifact

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaxCompressed = 100 << 20
	MaxExpanded   = 256 << 20
	MaxFile       = 32 << 20
	MaxEntries    = 20000
)

var ErrArtifact = errors.New("invalid or unsupported Node release artifact")

type Manifest struct {
	SHA256        string
	Files         int
	ExpandedBytes int64
}
type entry struct {
	file   *zip.File
	kind   os.FileMode
	target string
}

// Validate requires an expected digest from the trusted build record. A matching
// archive digest proves byte identity, not build provenance or runtime readiness.
func Validate(ctx context.Context, data []byte, expectedSHA256 string) (Manifest, error) {
	m, _, err := inspect(ctx, data, expectedSHA256)
	return m, err
}
func inspect(ctx context.Context, data []byte, expected string) (Manifest, map[string]entry, error) {
	var m Manifest
	if len(data) > MaxCompressed || len(expected) != 64 {
		return m, nil, ErrArtifact
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != expected {
		return m, nil, ErrArtifact
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(archive.File) == 0 || len(archive.File) > MaxEntries {
		return m, nil, ErrArtifact
	}
	entries := map[string]entry{}
	for _, f := range archive.File {
		if err = ctx.Err(); err != nil {
			return m, nil, err
		}
		name := strings.TrimSuffix(f.Name, "/")
		if !safeName(name) {
			return m, nil, ErrArtifact
		}
		if _, exists := entries[name]; exists {
			return m, nil, ErrArtifact
		}
		kind := f.Mode().Type()
		if kind != 0 && kind != os.ModeDir && kind != os.ModeSymlink {
			return m, nil, ErrArtifact
		}
		if strings.HasSuffix(f.Name, "/") != (kind == os.ModeDir) {
			return m, nil, ErrArtifact
		}
		if f.UncompressedSize64 > MaxFile || (kind == os.ModeDir && f.UncompressedSize64 != 0) {
			return m, nil, ErrArtifact
		}
		if m.ExpandedBytes+int64(f.UncompressedSize64) > MaxExpanded {
			return m, nil, ErrArtifact
		}
		in, e := f.Open()
		if e != nil {
			return m, nil, ErrArtifact
		}
		limit := int64(MaxFile)
		if kind == os.ModeSymlink {
			limit = 4096
		}
		if name == "package.json" {
			limit = 1 << 20
		}
		var body bytes.Buffer
		var sink io.Writer = io.Discard
		if kind == os.ModeSymlink || name == "package.json" {
			sink = &body
		}
		count, e := io.Copy(sink, io.LimitReader(contextReader{ctx, in}, limit+1))
		closed := in.Close()
		if e != nil || closed != nil || count > limit || count != int64(f.UncompressedSize64) {
			return m, nil, errors.Join(ErrArtifact, e, closed)
		}
		item := entry{file: f, kind: kind}
		if kind == os.ModeSymlink {
			item.target = body.String()
			if !safeTarget(item.target) {
				return m, nil, ErrArtifact
			}
		}
		if name == "package.json" {
			var pkg struct {
				Scripts map[string]string `json:"scripts"`
			}
			if kind != 0 || json.Unmarshal(body.Bytes(), &pkg) != nil || strings.TrimSpace(pkg.Scripts["start"]) == "" {
				return m, nil, ErrArtifact
			}
		}
		entries[name] = item
		m.Files++
		m.ExpandedBytes += count
	}
	if pkg, ok := entries["package.json"]; !ok || pkg.kind != 0 {
		return m, nil, ErrArtifact
	}
	// Materialize implicit directories and reject files/links used as parents,
	// independent of ZIP entry order. Extraction never writes through a link.
	for name := range entries {
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if item, ok := entries[parent]; ok {
				if item.kind != os.ModeDir {
					return m, nil, ErrArtifact
				}
			} else {
				entries[parent] = entry{kind: os.ModeDir}
			}
		}
	}
	if len(entries) > MaxEntries {
		return m, nil, ErrArtifact
	}
	for name, item := range entries {
		if item.kind == os.ModeSymlink {
			if !regularTarget(entries, name, map[string]bool{}) {
				return m, nil, ErrArtifact
			}
		}
	}
	if err = ctx.Err(); err != nil {
		return m, nil, err
	}
	m.SHA256 = expected
	return m, entries, nil
}

func safeName(name string) bool {
	if name == "" || len(name) > 4096 || !utf8.ValidString(name) || strings.ContainsAny(name, "\\:") || strings.HasPrefix(name, "/") || path.Clean(name) != name || strings.ContainsFunc(name, unicode.IsControl) {
		return false
	}
	parts := strings.Split(name, "/")
	if len(parts) > 64 {
		return false
	}
	for _, part := range parts {
		lower := strings.ToLower(part)
		if part == "." || part == ".." || lower == ".git" || lower == ".npmrc" || lower == ".env" || strings.HasPrefix(lower, ".env.") {
			return false
		}
	}
	return true
}
func safeTarget(target string) bool {
	if target == "" || len(target) > 4096 || !utf8.ValidString(target) || strings.HasPrefix(target, "/") || strings.ContainsAny(target, "\\:") || strings.ContainsFunc(target, unicode.IsControl) {
		return false
	}
	// Only leading parent steps are accepted. Cleaning a/../b across a symbolic
	// link could otherwise change the meaning of the actual filesystem target.
	descending := false
	for _, part := range strings.Split(target, "/") {
		if part == "" || part == "." {
			return false
		}
		if part == ".." {
			if descending {
				return false
			}
		} else {
			descending = true
		}
	}
	return descending
}
func regularTarget(entries map[string]entry, name string, seen map[string]bool) bool {
	if seen[name] || len(seen) >= 32 {
		return false
	}
	seen[name] = true
	item := entries[name]
	target := path.Join(path.Dir(name), item.target)
	if !safeName(target) {
		return false
	}
	for parent := path.Dir(target); parent != "."; parent = path.Dir(parent) {
		dir, ok := entries[parent]
		if !ok || dir.kind != os.ModeDir {
			return false
		}
	}
	next, ok := entries[target]
	if !ok {
		return false
	}
	if next.kind == 0 {
		return true
	}
	if next.kind != os.ModeSymlink {
		return false
	}
	return regularTarget(entries, target, seen)
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
