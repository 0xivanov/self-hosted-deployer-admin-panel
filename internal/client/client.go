// Package client delegates control-plane operations to the authenticated CLI.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type CLI struct {
	executable, directory, config   string
	Endpoint, Environment, Identity string
}
type savedContext struct {
	ServerURL     string `json:"server_url"`
	AdminToken    string `json:"admin_token"`
	CredentialRef string `json:"credential_ref,omitempty"`
	Identity      string `json:"server_identity,omitempty"`
}
type savedConfig struct {
	ServerURL  string                  `json:"server_url"`
	AdminToken string                  `json:"admin_token"`
	Current    string                  `json:"current_context,omitempty"`
	Contexts   map[string]savedContext `json:"contexts,omitempty"`
}

func privateRead(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("credential/config files must be private regular files (0600)")
	}
	return os.ReadFile(path)
}
func New(executable, configPath, selected string) (*CLI, error) {
	executable, err := exec.LookPath(executable)
	if err != nil {
		return nil, err
	}
	if configPath == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return nil, err
		}
		configPath = filepath.Join(base, "deployer", "config.json")
	}
	data, err := privateRead(configPath)
	if err != nil {
		return nil, err
	}
	var saved savedConfig
	if err = json.Unmarshal(data, &saved); err != nil {
		return nil, err
	}
	if selected == "" {
		selected = os.Getenv("DEPLOYER_CONTEXT")
	}
	if selected == "" {
		selected = saved.Current
	}
	chosen := savedContext{ServerURL: saved.ServerURL, AdminToken: saved.AdminToken}
	if selected != "" {
		var ok bool
		chosen, ok = saved.Contexts[selected]
		if !ok {
			return nil, fmt.Errorf("unknown context %q", selected)
		}
	}
	if chosen.CredentialRef != "" {
		switch {
		case strings.HasPrefix(chosen.CredentialRef, "env:"):
			chosen.AdminToken = os.Getenv(strings.TrimPrefix(chosen.CredentialRef, "env:"))
		case strings.HasPrefix(chosen.CredentialRef, "file:"):
			token, e := privateRead(strings.TrimPrefix(chosen.CredentialRef, "file:"))
			if e != nil {
				return nil, e
			}
			chosen.AdminToken = strings.TrimSpace(string(token))
		default:
			return nil, errors.New("unsupported credential reference")
		}
	}
	endpoint, e := url.Parse(chosen.ServerURL)
	if e != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.Scheme != "https" {
		return nil, errors.New("live UI requires an HTTPS endpoint without URL credentials")
	}
	if chosen.AdminToken == "" {
		return nil, errors.New("no admin credential in selected context")
	}
	chosen.CredentialRef = ""
	dir, err := os.MkdirTemp("", "deployer-admin-")
	if err != nil {
		return nil, err
	}
	c := &CLI{executable: executable, directory: dir, config: filepath.Join(dir, "config.json"), Endpoint: chosen.ServerURL, Environment: selected}
	if c.Environment == "" {
		c.Environment = "Current environment"
	}
	frozen := savedConfig{Current: "panel", Contexts: map[string]savedContext{"panel": chosen}}
	write := func() error {
		v, e := json.Marshal(frozen)
		if e != nil {
			return e
		}
		return os.WriteFile(c.config, v, 0600)
	}
	if err = write(); err != nil {
		c.Close()
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	status, err := c.Status(ctx)
	if err != nil {
		c.Close()
		return nil, err
	}
	if status.ServerIdentity == "" {
		c.Close()
		return nil, errors.New("update the deployer CLI: server status must include server_identity")
	}
	if chosen.Identity != "" && chosen.Identity != status.ServerIdentity {
		c.Close()
		return nil, errors.New("server identity mismatch")
	}
	chosen.Identity = status.ServerIdentity
	c.Identity = status.ServerIdentity
	frozen.Contexts["panel"] = chosen
	if err = write(); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}
func (c *CLI) Close() error { return os.RemoveAll(c.directory) }

type limitedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.limit {
		return 0, errors.New("CLI output exceeded the response limit")
	}
	return b.Buffer.Write(p)
}
func (c *CLI) run(ctx context.Context, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, c.executable, append([]string{"--config", c.config, "--context", "panel", "--output", "json"}, args...)...)
	var out = limitedBuffer{limit: 4 << 20}
	var stderr = limitedBuffer{limit: 64 << 10}
	command.Stdout = &out
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("deployer command failed: %s", strings.TrimSpace(stderr.String()))
	}
	return out.Bytes(), nil
}
func (c *CLI) read(ctx context.Context, result any, args ...string) error {
	data, err := c.run(ctx, args...)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, result)
}
func (c *CLI) Status(ctx context.Context) (v ServerStatus, e error) {
	e = c.read(ctx, &v, "server", "status")
	return
}
func (c *CLI) ListApps(ctx context.Context) ([]AppInfo, error) {
	var v struct {
		Apps []AppInfo `json:"apps"`
	}
	e := c.read(ctx, &v, "apps", "list")
	return v.Apps, e
}
func (c *CLI) ListNodes(ctx context.Context) ([]NodeInfo, error) {
	var v struct {
		Nodes []NodeInfo `json:"nodes"`
	}
	e := c.read(ctx, &v, "nodes", "list")
	return v.Nodes, e
}
func (c *CLI) InspectApp(ctx context.Context, name string) (v AppInspectResult, e error) {
	e = c.read(ctx, &v, "apps", "inspect", name)
	return
}
func (c *CLI) GetAppStatus(ctx context.Context, name string) (v AppStatusResult, e error) {
	e = c.read(ctx, &v, "status", name)
	return
}
func (c *CLI) StreamLogs(ctx context.Context, name string, tail int32, _ bool, receive func(string) error) error {
	data, e := c.run(ctx, "logs", "--tail", fmt.Sprint(tail), name)
	if e != nil {
		return e
	}
	return receive(string(data))
}
func (c *CLI) withYAML(ctx context.Context, data, command string, result any) error {
	f, err := os.CreateTemp(c.directory, "app-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.WriteString(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return c.read(ctx, result, command, "--file", f.Name())
}
func (c *CLI) PreflightApp(ctx context.Context, data string) (v PreflightResult, e error) {
	e = c.withYAML(ctx, data, "preflight", &v)
	return
}
func (c *CLI) DeployApp(ctx context.Context, data string) (v DeployResult, e error) {
	e = c.withYAML(ctx, data, "deploy", &v)
	return
}

// ChangeNode uses the existing CLI lifecycle and its backend audit trail.
func (c *CLI) ChangeNode(ctx context.Context, id, action string) error {
	var result any
	switch action {
	case "remove":
		if err := c.read(ctx, &result, "nodes", "drain", id); err != nil {
			return err
		}
		return c.read(ctx, &result, "nodes", "remove", "--yes", id)
	case "purge":
		return c.read(ctx, &result, "nodes", "purge", "--yes", id)
	default:
		return errors.New("unsupported node action")
	}
}
