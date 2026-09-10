// Package npmfetch resolves bounded registry downloads without executing npm or
// package scripts. Installing/unpacking them remains an isolated-builder operation.
package npmfetch

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"path"
	"sort"
	"strings"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/projectarchive"
)

var ErrDependency = errors.New("dependencies require a modern npm lockfile, public registry tarballs and SHA-512 integrity")

const MaxTarballs = 1000
const MaxTarballBytes = 25 << 20

type Tarball struct {
	URL       string `json:"url"`
	Integrity string `json:"integrity"`
}
type Set struct {
	SourceSHA256 string    `json:"source_sha256"`
	Tarballs     []Tarball `json:"tarballs"`
}

// FromSource plans downloads for every locked platform and dependency type.
// It does not resolve package names or contact a registry. npm ci in the offline
// builder still checks package.json/lockfile consistency and platform selection.
func FromSource(ctx context.Context, source []byte, expectedSHA256 string) (Set, error) {
	m, err := projectarchive.Validate(ctx, source, "node")
	if err != nil {
		return Set{}, err
	}
	if m.SHA256 != expectedSHA256 {
		return Set{}, ErrDependency
	}
	z, err := zip.NewReader(bytes.NewReader(source), int64(len(source)))
	if err != nil {
		return Set{}, err
	}
	var lock []byte
	for _, f := range z.File {
		if strings.EqualFold(f.Name, "npm-shrinkwrap.json") {
			return Set{}, ErrDependency
		}
		if f.Name != "package-lock.json" {
			continue
		}
		r, e := f.Open()
		if e != nil {
			return Set{}, e
		}
		lock, e = io.ReadAll(io.LimitReader(r, projectarchive.MaxFile+1))
		closeErr := r.Close()
		if e != nil || closeErr != nil {
			return Set{}, ErrDependency
		}
	}
	var document struct {
		Version  int                        `json:"lockfileVersion"`
		Packages map[string]json.RawMessage `json:"packages"`
	}
	if json.Unmarshal(lock, &document) != nil || (document.Version != 2 && document.Version != 3) || document.Packages == nil || len(document.Packages) > MaxTarballs+1 {
		return Set{}, ErrDependency
	}
	if root, ok := document.Packages[""]; !ok || len(root) == 0 || root[0] != '{' {
		return Set{}, ErrDependency
	}
	unique := map[string]string{}
	for location, raw := range document.Packages {
		if err = ctx.Err(); err != nil {
			return Set{}, err
		}
		if location == "" {
			continue
		}
		if !strings.HasPrefix(location, "node_modules/") || path.Clean(location) != location || strings.ContainsAny(location, "\\:\x00") {
			return Set{}, ErrDependency
		}
		var entry struct {
			Resolved  string `json:"resolved"`
			Integrity string `json:"integrity"`
			Link      bool   `json:"link"`
			InBundle  bool   `json:"inBundle"`
		}
		if len(raw) == 0 || raw[0] != '{' || json.Unmarshal(raw, &entry) != nil || entry.Link {
			return Set{}, ErrDependency
		}
		if entry.InBundle {
			continue
		}
		tarball := Tarball{entry.Resolved, entry.Integrity}
		if !validTarball(tarball) {
			return Set{}, ErrDependency
		}
		if old, ok := unique[tarball.URL]; ok && old != tarball.Integrity {
			return Set{}, ErrDependency
		}
		unique[tarball.URL] = tarball.Integrity
	}
	out := Set{SourceSHA256: m.SHA256, Tarballs: []Tarball{}}
	for u, integrity := range unique {
		out.Tarballs = append(out.Tarballs, Tarball{u, integrity})
	}
	sort.Slice(out.Tarballs, func(i, j int) bool { return out.Tarballs[i].URL < out.Tarballs[j].URL })
	return out, nil
}

func validTarball(t Tarball) bool {
	if len(t.URL) > 2048 {
		return false
	}
	u, err := url.Parse(t.URL)
	if err != nil || u.Scheme != "https" || u.Host != "registry.npmjs.org" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || u.Opaque != "" || path.Clean(u.Path) != u.Path || !strings.HasSuffix(u.Path, ".tgz") || !strings.Contains(u.Path, "/-/") || strings.ContainsAny(u.Path, "\\\x00") {
		return false
	}
	if !strings.HasPrefix(t.Integrity, "sha512-") {
		return false
	}
	encoded := strings.TrimPrefix(t.Integrity, "sha512-")
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	return err == nil && len(decoded) == 64 && base64.StdEncoding.EncodeToString(decoded) == encoded
}
