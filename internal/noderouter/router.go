// Package noderouter switches a dedicated Node runtime's public route after a
// candidate health check. It does not provision VMs or start customer processes.
package noderouter

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

var (
	ErrInvalid   = errors.New("invalid Node routing assignment")
	ErrConflict  = errors.New("routing revision conflicts with a recorded request")
	ErrStale     = errors.New("routing revision has been superseded")
	ErrUnhealthy = errors.New("candidate did not pass its health check")
	ErrRetired   = errors.New("Node routing operation is permanently retired")
)

type Config struct {
	ProjectID   string
	RuntimeID   string
	ContentHost string
	HealthPath  string
	Backends    map[string]string
}
type Candidate struct {
	ProjectID      string
	RuntimeID      string
	DeploymentID   string
	OperationID    string
	Revision       int64
	ArtifactSHA256 string
	Backend        string
}
type State struct {
	Fence  Candidate
	Status string
	Active *Candidate
}
type Router struct {
	mu        sync.Mutex
	owner     *os.File
	ownerPath string
	closed    bool
	upstreams int
	db        *sql.DB
	config    Config
	backends  map[string]*url.URL
	health    *http.Client
	transport *http.Transport
}

func digestID(id string) bool {
	v, e := hex.DecodeString(id)
	return e == nil && len(v) == 32 && hex.EncodeToString(v) == id
}

// Open pins configuration to a private routing database. Backend endpoints are
// operator-owned loopback HTTP listeners in the assigned isolated runtime, never
// customer URLs. The launcher must keep each endpoint bound to its verified
// release throughout staging, health checks and serving.
func Open(database string, config Config) (*Router, error) {
	if !digestID(config.ProjectID) || !digestID(config.RuntimeID) || len(config.Backends) < 1 || len(config.Backends) > 16 {
		return nil, ErrInvalid
	}
	host, e := url.Parse("https://" + config.ContentHost)
	if e != nil || host.Host != config.ContentHost || host.Hostname() == "" || host.User != nil || host.Path != "" || host.RawQuery != "" || host.Fragment != "" {
		return nil, ErrInvalid
	}
	health, e := url.ParseRequestURI(config.HealthPath)
	if e != nil || health.Path != config.HealthPath || health.RawQuery != "" || !strings.HasPrefix(config.HealthPath, "/") || strings.HasPrefix(config.HealthPath, "//") {
		return nil, ErrInvalid
	}
	urls := map[string]*url.URL{}
	copied := map[string]string{}
	seen := map[string]bool{}
	for name, target := range config.Backends {
		if name == "" || len(name) > 64 {
			return nil, ErrInvalid
		}
		u, err := url.Parse(target)
		if err != nil {
			return nil, ErrInvalid
		}
		port, err := strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 || u.Scheme != "http" || u.Host != "127.0.0.1:"+strconv.Itoa(port) || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || seen[target] {
			return nil, ErrInvalid
		}
		seen[target] = true
		urls[name] = u
		copied[name] = target
	}
	config.Backends = copied
	absolute, err := filepath.Abs(database)
	if err != nil {
		return nil, err
	}
	directory := filepath.Dir(absolute)
	if err = os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, ErrInvalid
	}
	f, err := os.OpenFile(absolute, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err == nil {
		if err = f.Close(); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	info, err = os.Lstat(absolute)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, ErrInvalid
	}
	location := url.URL{Scheme: "file", Path: absolute}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "synchronous(FULL)")
	q.Set("_txlock", "immediate")
	location.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", location.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = (&net.Dialer{Timeout: 3 * time.Second}).DialContext
	transport.ResponseHeaderTimeout = 5 * time.Second
	r := &Router{ownerPath: absolute + ".serve.lock", db: db, config: config, backends: urls, transport: transport, health: &http.Client{Transport: transport, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	if err = r.initialize(); err != nil {
		r.Close()
		return nil, err
	}
	return r, nil
}
func (r *Router) Close() error {
	r.mu.Lock()
	r.closed = true
	r.releaseOwnerLocked()
	r.mu.Unlock()
	r.transport.CloseIdleConnections()
	return r.db.Close()
}
func (r *Router) initialize() error {
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err = tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > 2 {
		return ErrInvalid
	}
	if version == 0 {
		if _, err = tx.Exec("CREATE TABLE node_route(id INTEGER PRIMARY KEY CHECK(id=1),config BLOB NOT NULL,state BLOB NOT NULL); PRAGMA user_version=1"); err != nil {
			return err
		}
	}
	if version < 2 {
		if _, err = tx.Exec("CREATE TABLE retired_operations(operation TEXT PRIMARY KEY,candidate BLOB NOT NULL); PRAGMA user_version=2"); err != nil {
			return err
		}
	}
	expected, err := json.Marshal(r.config)
	if err != nil {
		return err
	}
	var current []byte
	err = tx.QueryRow("SELECT config FROM node_route WHERE id=1").Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		raw, e := json.Marshal(State{})
		if e != nil {
			return e
		}
		if _, err = tx.Exec("INSERT INTO node_route(id,config,state) VALUES(1,?,?)", expected, raw); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if !bytes.Equal(expected, current) {
		return ErrConflict
	}
	return tx.Commit()
}
func readState(row interface{ Scan(...any) error }) (State, error) {
	var state State
	var raw []byte
	if err := row.Scan(&raw); err != nil {
		return state, err
	}
	err := json.Unmarshal(raw, &state)
	return state, err
}
func writeState(ctx context.Context, tx *sql.Tx, state State) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE node_route SET state=? WHERE id=1", raw)
	return err
}
func (r *Router) Snapshot(ctx context.Context) (State, error) {
	return readState(r.db.QueryRowContext(ctx, "SELECT state FROM node_route WHERE id=1"))
}

// Activate is trusted control-plane access, not an HTTP customer endpoint. The
// persisted fence precedes probing. A failed candidate never changes Active;
// rollback must use a new revision. A pending identical request can be reprobed
// after restart, since health probes do not start or mutate application processes.
func (r *Router) Activate(ctx context.Context, c Candidate) error {
	if !r.validCandidate(c) {
		return ErrInvalid
	}
	if err := r.beginUpstream(); err != nil {
		return err
	}
	defer r.endUpstream()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = rejectRetired(ctx, tx, c.OperationID); err != nil {
		return err
	}
	state, err := readState(tx.QueryRowContext(ctx, "SELECT state FROM node_route WHERE id=1"))
	if err != nil {
		return err
	}
	if c.Revision < state.Fence.Revision {
		return ErrStale
	}
	if c.Revision == state.Fence.Revision {
		if c != state.Fence {
			return ErrConflict
		}
		if state.Status == "active" {
			return tx.Commit()
		}
		if state.Status == "failed" {
			return ErrUnhealthy
		}
	} else {
		// Reusing the active listener for different bytes would mutate the old live
		// release before this health gate. The launcher must stage another listener.
		if state.Active != nil && state.Active.Backend == c.Backend && state.Active.ArtifactSHA256 != c.ArtifactSHA256 {
			return ErrConflict
		}
		state.Fence = c
		state.Status = "pending"
		if err = writeState(ctx, tx, state); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	target := *r.backends[c.Backend]
	target.Path = r.config.HealthPath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return err
	}
	req.Host = r.config.ContentHost
	response, probeErr := r.health.Do(req)
	healthy := probeErr == nil && response.StatusCode >= 200 && response.StatusCode < 300
	if response != nil {
		response.Body.Close()
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	tx, err = r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = rejectRetired(ctx, tx, c.OperationID); err != nil {
		return err
	}
	state, err = readState(tx.QueryRowContext(ctx, "SELECT state FROM node_route WHERE id=1"))
	if err != nil {
		return err
	}
	if state.Fence != c {
		return ErrStale
	}
	if state.Status == "active" {
		return tx.Commit()
	}
	if state.Status == "failed" {
		return ErrUnhealthy
	}
	state.Status = "failed"
	if healthy {
		state.Status = "active"
		state.Active = &c
	}
	if err = writeState(ctx, tx, state); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if !healthy {
		return ErrUnhealthy
	}
	return nil
}

// ServeHTTP serves only the configured content host. A separate authenticated
// control-plane service must expose Activate, and TLS belongs at the listener.
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if !strings.EqualFold(req.Host, r.config.ContentHost) {
		http.Error(w, "unknown host", http.StatusMisdirectedRequest)
		return
	}
	if err := r.beginUpstream(); err != nil {
		http.Error(w, "site unavailable", http.StatusServiceUnavailable)
		return
	}
	defer r.endUpstream()
	state, err := r.Snapshot(req.Context())
	if err != nil || state.Active == nil {
		http.Error(w, "site unavailable", http.StatusServiceUnavailable)
		return
	}
	target := r.backends[state.Active.Backend]
	if target == nil {
		http.Error(w, "site unavailable", http.StatusServiceUnavailable)
		return
	}
	proxy := httputil.ReverseProxy{Transport: r.transport, Rewrite: func(p *httputil.ProxyRequest) { p.SetURL(target); p.Out.Host = r.config.ContentHost; p.SetXForwarded() }, ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(w, "site unavailable", http.StatusBadGateway)
	}}
	proxy.ServeHTTP(w, req)
}
