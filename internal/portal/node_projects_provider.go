package portal

import (
	"encoding/json"
	"errors"
	"io"
	"os"
)

const nodeProjectsMaxBytes = 64 * 1024

// NodeProjectProvider reloads operator assignments for each request. Invalid
// reloads return an empty snapshot so stale permissions are never retained.
type NodeProjectProvider struct{ path string }

func NewNodeProjectProvider(path string) (*NodeProjectProvider, error) {
	p := &NodeProjectProvider{path: path}
	if _, err := p.load(); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *NodeProjectProvider) Snapshot() map[string]NodeProjectConfig {
	assignments, err := p.load()
	if err != nil {
		return map[string]NodeProjectConfig{}
	}
	return assignments
}

func (p *NodeProjectProvider) load() (map[string]NodeProjectConfig, error) {
	info, err := os.Lstat(p.path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > nodeProjectsMaxBytes {
		return nil, errors.New("Node project mapping unavailable")
	}
	f, err := os.Open(p.path)
	if err != nil {
		return nil, errors.New("Node project mapping unavailable")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, nodeProjectsMaxBytes+1))
	if err != nil || len(raw) > nodeProjectsMaxBytes {
		return nil, errors.New("Node project mapping unavailable")
	}
	var input map[string]NodeProjectConfig
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, errors.New("invalid Node project mapping")
	}
	return copyNodeProjects(input)
}
