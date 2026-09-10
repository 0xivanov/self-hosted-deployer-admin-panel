// Package projectarchive validates untrusted ZIP uploads without extracting or
// executing them. Publication/build isolation is a separate boundary.
package projectarchive

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaxCompressed = 10 << 20
const MaxExpanded = 32 << 20
const MaxFile = 8 << 20
const MaxEntries = 1000

type Manifest struct {
	SHA256 string `json:"sha256"`
	Files  int    `json:"files"`
	Bytes  int64  `json:"expanded_bytes"`
	Kind   string `json:"kind"`
}

func Validate(ctx context.Context, data []byte, kind string) (Manifest, error) {
	var result Manifest
	if kind != "static" && kind != "node" {
		return result, errors.New("unsupported project type")
	}
	if len(data) == 0 || len(data) > MaxCompressed {
		return result, errors.New("ZIP must be no larger than 10 MiB")
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return result, errors.New("invalid ZIP archive")
	}
	if len(archive.File) == 0 || len(archive.File) > MaxEntries {
		return result, errors.New("ZIP must contain 1 to 1000 entries")
	}
	seen := map[string]bool{}
	files := map[string]bool{}
	directories := map[string]bool{}
	var pkg, lock []byte
	for _, file := range archive.File {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		name := strings.TrimSuffix(file.Name, "/")
		if err := validPath(name); err != nil {
			return result, err
		}
		normalized := strings.ToLower(name)
		if seen[normalized] {
			return result, errors.New("duplicate or case-colliding paths in ZIP")
		}
		seen[normalized] = true
		if file.Flags&1 != 0 {
			return result, errors.New("encrypted ZIP entries are unsupported")
		}
		if file.Mode().IsDir() {
			directories[normalized] = true
			continue
		}
		if !file.Mode().IsRegular() {
			return result, errors.New("links and special files are unsupported")
		}
		files[normalized] = true
		if file.UncompressedSize64 > MaxFile {
			return result, errors.New("each expanded file must be at most 8 MiB")
		}
		reader, err := file.Open()
		if err != nil {
			return result, errors.New("cannot read ZIP entry")
		}
		// Read the stream as well as the declared size, checking checksums and actual
		// expansion limits. Header sizes alone are not trusted.
		limited := io.LimitReader(reader, MaxFile+1)
		var count int64
		if name == "package.json" || name == "package-lock.json" {
			payload, e := io.ReadAll(limited)
			err = e
			count = int64(len(payload))
			if name == "package.json" {
				pkg = payload
			} else {
				lock = payload
			}
		} else {
			count, err = io.Copy(io.Discard, limited)
		}
		closeErr := reader.Close()
		if err != nil || closeErr != nil {
			return result, errors.New("corrupt or unsupported ZIP entry")
		}
		if count > MaxFile {
			return result, errors.New("expanded file exceeds 8 MiB")
		}
		result.Files++
		result.Bytes += count
		if result.Bytes > MaxExpanded {
			return result, errors.New("expanded project exceeds 32 MiB")
		}
	}
	for name := range files {
		if directories[name] {
			return result, errors.New("file/directory path collision")
		}
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if files[parent] {
				return result, errors.New("file used as a parent directory")
			}
		}
	}
	// An explicit directory cannot be a descendant of a file either.
	for name := range directories {
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if files[parent] {
				return result, errors.New("directory nested under a file")
			}
		}
	}
	if result.Files == 0 {
		return result, errors.New("ZIP contains no files")
	}
	if kind == "static" {
		if !files["index.html"] || !exactFile(archive, "index.html") {
			return result, errors.New("static project requires index.html at ZIP root")
		}
	}
	if kind == "node" {
		var manifest struct {
			Scripts map[string]string `json:"scripts"`
		}
		if len(pkg) == 0 || json.Unmarshal(pkg, &manifest) != nil || strings.TrimSpace(manifest.Scripts["start"]) == "" {
			return result, errors.New("Node project requires root package.json with a start script")
		}
		var manifestLock struct {
			LockfileVersion int `json:"lockfileVersion"`
		}
		if len(lock) == 0 || json.Unmarshal(lock, &manifestLock) != nil || manifestLock.LockfileVersion < 1 || manifestLock.LockfileVersion > 3 {
			return result, errors.New("Node project requires a supported root package-lock.json")
		}
	}
	digest := sha256.Sum256(data)
	result.SHA256 = hex.EncodeToString(digest[:])
	result.Kind = kind
	return result, nil
}
func exactFile(archive *zip.Reader, name string) bool {
	for _, file := range archive.File {
		if file.Name == name && !file.Mode().IsDir() {
			return true
		}
	}
	return false
}
func validPath(name string) error {
	if name == "" || len(name) > 1024 || !utf8.ValidString(name) || path.Clean(name) != name || strings.HasPrefix(name, "/") || strings.ContainsAny(name, "\\:") {
		return errors.New("ZIP contains an unsafe path")
	}
	if strings.ContainsFunc(name, func(r rune) bool { return unicode.IsControl(r) }) {
		return errors.New("ZIP paths cannot contain control characters")
	}
	parts := strings.Split(name, "/")
	if len(parts) > 16 {
		return errors.New("ZIP path nesting exceeds 16 levels")
	}
	for _, part := range parts {
		if part == "." || part == ".." || len(part) > 255 || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return errors.New("ZIP contains an unsafe path component")
		}
		lower := strings.ToLower(part)
		if lower == ".git" || lower == "node_modules" || lower == ".npmrc" || strings.HasPrefix(lower, ".env") || lower == "id_rsa" || lower == "id_ed25519" || strings.HasSuffix(lower, ".pem") || strings.HasSuffix(lower, ".key") || strings.HasSuffix(lower, ".p12") || strings.HasSuffix(lower, ".pfx") {
			return errors.New("remove repository metadata, dependencies and credential files before uploading")
		}
	}
	return nil
}
