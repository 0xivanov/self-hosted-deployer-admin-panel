// Package registryimage validates registry metadata before an image can become
// a release. Validation does not pull layers, execute containers, or publish.
package registryimage

import (
	"errors"
	"regexp"
	"strings"
)

var ErrReference = errors.New("use a Docker Hub or GHCR image with an explicit tag or sha256 digest")
var repositoryPart = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*$`)
var tagPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
var digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

type Reference struct{ Registry, Repository, Version string }

func Parse(raw string) (Reference, error) {
	var r Reference
	if raw != strings.TrimSpace(raw) || len(raw) > 512 || strings.ContainsAny(raw, "\\?# \t\r\n") {
		return r, ErrReference
	}
	name := raw
	if strings.Count(raw, "@") > 0 {
		if strings.Count(raw, "@") != 1 {
			return r, ErrReference
		}
		parts := strings.SplitN(raw, "@", 2)
		name, r.Version = parts[0], parts[1]
		if !digestPattern.MatchString(r.Version) {
			return r, ErrReference
		}
	} else {
		i := strings.LastIndex(raw, ":")
		if i < 0 || i < strings.LastIndex(raw, "/") {
			return r, ErrReference
		}
		name, r.Version = raw[:i], raw[i+1:]
		if !tagPattern.MatchString(r.Version) {
			return r, ErrReference
		}
	}
	parts := strings.Split(name, "/")
	r.Registry = "docker.io"
	if len(parts) > 1 && (strings.ContainsAny(parts[0], ".:") || parts[0] == "localhost") {
		r.Registry = parts[0]
		parts = parts[1:]
	}
	if r.Registry != "docker.io" && r.Registry != "ghcr.io" {
		return Reference{}, ErrReference
	}
	if r.Registry == "docker.io" && len(parts) == 1 {
		parts = append([]string{"library"}, parts...)
	}
	if len(parts) < 2 {
		return Reference{}, ErrReference
	}
	for _, part := range parts {
		if !repositoryPart.MatchString(part) {
			return Reference{}, ErrReference
		}
	}
	r.Repository = strings.Join(parts, "/")
	if len(r.Repository) > 255 {
		return Reference{}, ErrReference
	}
	return r, nil
}
func (r Reference) String() string {
	sep := ":"
	if digestPattern.MatchString(r.Version) {
		sep = "@"
	}
	return r.Registry + "/" + r.Repository + sep + r.Version
}
