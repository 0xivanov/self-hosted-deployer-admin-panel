package portal

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
)

// ContainerProjectProvider reloads a private operator mapping on each request.
// Invalid or revoked configuration returns an empty map, never stale access.
type ContainerProjectProvider struct{ path string }

func NewContainerProjectProvider(path string) (*ContainerProjectProvider, error) {
	p := &ContainerProjectProvider{path: path}
	_, err := p.load()
	return p, err
}
func (p *ContainerProjectProvider) Snapshot() map[string]ContainerProjectConfig {
	m, err := p.load()
	if err != nil {
		return map[string]ContainerProjectConfig{}
	}
	return m
}
func (p *ContainerProjectProvider) load() (map[string]ContainerProjectConfig, error) {
	info, err := os.Lstat(p.path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > nodeProjectsMaxBytes {
		return nil, errors.New("container project mapping unavailable")
	}
	f, err := os.Open(p.path)
	if err != nil {
		return nil, errors.New("container project mapping unavailable")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, nodeProjectsMaxBytes+1))
	if err != nil || len(raw) > nodeProjectsMaxBytes {
		return nil, errors.New("container project mapping unavailable")
	}
	var m map[string]ContainerProjectConfig
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&m); err != nil {
		return nil, errors.New("invalid container project mapping")
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return nil, errors.New("invalid container project mapping")
	}
	return copyContainerProjects(m)
}
