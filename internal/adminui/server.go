// Package adminui provides a loopback-only browser client for the existing RPC API.
package adminui

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/appconfig"
	cli "github.com/0xivanov/self-hosted-deployer-admin-panel/internal/client"
	"gopkg.in/yaml.v3"
)

//go:embed static/*
var assets embed.FS

type Backend interface {
	Status(context.Context) (cli.ServerStatus, error)
	ListApps(context.Context) ([]cli.AppInfo, error)
	ListNodes(context.Context) ([]cli.NodeInfo, error)
	InspectApp(context.Context, string) (cli.AppInspectResult, error)
	GetAppStatus(context.Context, string) (cli.AppStatusResult, error)
	StreamLogs(context.Context, string, int32, bool, func(string) error) error
	DeployApp(context.Context, string) (cli.DeployResult, error)
}

type Options struct {
	Host, Environment, Endpoint, Identity string
	AllowWrites, Demo                     bool
}

type snapshot struct{ ID, YAML, After string }
type Server struct {
	backend   Backend
	options   Options
	token     string
	index     *template.Template
	mu        sync.Mutex // serialize mutations and session snapshots
	snapshots map[string]snapshot
}

func New(backend Backend, options Options) (*Server, error) {
	if !strings.HasPrefix(options.Host, "127.0.0.1:") || options.Identity == "" {
		return nil, errors.New("UI requires a loopback address and a bound server identity")
	}
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	index, err := template.ParseFS(assets, "static/index.html")
	if err != nil {
		return nil, err
	}
	return &Server{backend: backend, options: options, token: hex.EncodeToString(nonce), index: index, snapshots: make(map[string]snapshot)}, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	origin := "http://" + s.options.Host
	if r.Host != s.options.Host || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != origin) || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		failure(w, http.StatusForbidden, "Open the UI using its printed local address")
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Deployer-UI")), []byte(s.token)) != 1 {
			failure(w, 403, "UI session expired; reload the page")
			return
		}
		if r.Method != "GET" && r.Method != "POST" {
			failure(w, 405, "Method not allowed")
			return
		}
		if r.Method == "POST" && !s.options.AllowWrites {
			failure(w, 403, "This UI is read-only; restart with --allow-writes to make changes")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		status, err := s.backend.Status(ctx)
		if err != nil {
			failure(w, 502, err.Error())
			return
		}
		if status.ServerIdentity != s.options.Identity {
			failure(w, 409, "Server identity changed; restart with the correct context")
			return
		}
		s.api(w, r.WithContext(ctx))
		return
	}
	if r.Method != "GET" {
		failure(w, 405, "Method not allowed")
		return
	}
	switch r.URL.Path {
	case "/":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = s.index.Execute(w, struct{ Token string }{s.token})
	case "/app.js", "/styles.css":
		data, err := assets.ReadFile("static" + r.URL.Path)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path == "/app.js" {
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		} else {
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
		}
		_, _ = w.Write(data)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) api(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if r.URL.Path == "/api/overview" && r.Method == "GET" {
		apps, err := s.backend.ListApps(ctx)
		if err != nil {
			failure(w, 502, err.Error())
			return
		}
		nodes, err := s.backend.ListNodes(ctx)
		if err != nil {
			failure(w, 502, err.Error())
			return
		}
		respond(w, map[string]any{"environment": s.options.Environment, "endpoint": s.options.Endpoint, "apps": apps, "nodes": nodes, "read_only": !s.options.AllowWrites, "demo": s.options.Demo})
		return
	}
	if r.URL.Path == "/api/deploy" && r.Method == "POST" {
		var input struct {
			YAML string `json:"yaml"`
		}
		if !decode(w, r, &input) {
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.deploy(w, r, input.YAML)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/apps/"), "/")
	if !strings.HasPrefix(r.URL.Path, "/api/apps/") || len(parts) > 2 || parts[0] == "" {
		failure(w, 404, "Not found")
		return
	}
	name := parts[0]
	if !appconfig.ValidName.MatchString(name) {
		failure(w, 400, "Invalid application name")
		return
	}
	if len(parts) == 1 && r.Method == "GET" {
		info, err := s.backend.InspectApp(ctx, name)
		if err != nil {
			failure(w, 502, err.Error())
			return
		}
		status, err := s.backend.GetAppStatus(ctx, name)
		if err != nil {
			failure(w, 502, err.Error())
			return
		}
		data, err := yaml.Marshal(info.App.DesiredState)
		if err != nil {
			failure(w, 500, "Cannot render app configuration")
			return
		}
		s.mu.Lock()
		saved, exists := s.snapshots[name]
		s.mu.Unlock()
		var rollback any
		if exists && saved.After == fingerprint(info.App.DesiredState) {
			rollback = map[string]string{"deployment_id": saved.ID, "label": "Restore previous configuration"}
		}
		respond(w, map[string]any{"app": info.App, "status": status, "deployments": info.Deployments, "yaml": string(data), "rollback": rollback})
		return
	}
	if len(parts) == 2 && parts[1] == "logs" && r.Method == "GET" {
		var logs strings.Builder
		err := s.backend.StreamLogs(ctx, name, 200, false, func(line string) error {
			if logs.Len()+len(line) > 1024*1024 {
				return errors.New("Log response exceeds 1 MiB; use the CLI for a larger export")
			}
			logs.WriteString(line)
			if !strings.HasSuffix(line, "\n") {
				logs.WriteByte('\n')
			}
			return nil
		})
		if err != nil {
			failure(w, 502, err.Error())
			return
		}
		respond(w, map[string]string{"logs": logs.String()})
		return
	}
	if len(parts) == 2 && parts[1] == "rollback" && r.Method == "POST" {
		var input struct {
			ID string `json:"deployment_id"`
		}
		if !decode(w, r, &input) {
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		saved, exists := s.snapshots[name]
		if !exists || saved.ID != input.ID {
			failure(w, 409, "No matching saved configuration in this UI session")
			return
		}
		current, err := s.backend.InspectApp(ctx, name)
		if err != nil {
			failure(w, 502, err.Error())
			return
		}
		if fingerprint(current.App.DesiredState) != saved.After {
			failure(w, 409, "App changed since this snapshot; refresh and review its configuration")
			return
		}
		s.deploy(w, r, saved.YAML)
		return
	}
	failure(w, 404, "Not found")
}

func (s *Server) deploy(w http.ResponseWriter, r *http.Request, data string) {
	cfg, err := appconfig.Parse([]byte(data))
	if err != nil {
		failure(w, 400, err.Error())
		return
	}
	// Listing avoids treating an unavailable server as a missing app.
	apps, err := s.backend.ListApps(r.Context())
	if err != nil {
		failure(w, 502, err.Error())
		return
	}
	var previous *cli.AppInfo
	for i := range apps {
		if apps[i].Name == cfg.Name() {
			previous = &apps[i]
			break
		}
	}
	if preflight, ok := s.backend.(interface {
		PreflightApp(context.Context, string) (cli.PreflightResult, error)
	}); ok {
		if _, err := preflight.PreflightApp(r.Context(), data); err != nil {
			failure(w, 400, err.Error())
			return
		}
	}
	result, err := s.backend.DeployApp(r.Context(), data)
	if err != nil {
		failure(w, 502, err.Error())
		return
	}
	if previous != nil {
		saved, err := yaml.Marshal(previous.DesiredState)
		if err == nil {
			s.snapshots[cfg.Name()] = snapshot{ID: result.Deployment.ID, YAML: string(saved), After: fingerprint(result.App.DesiredState)}
		}
	}
	respond(w, result)
}

func fingerprint(cfg appconfig.Config) string { data, _ := json.Marshal(cfg); return string(data) }
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		failure(w, 415, "JSON content type required")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1024*1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(v); err != nil {
		failure(w, 400, "Invalid or oversized JSON request")
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		failure(w, 400, "Only one JSON object is allowed")
		return false
	}
	return true
}
func respond(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func failure(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// Address never accepts a public bind address from command-line input.
func Address(port int) (string, error) {
	if port < 1 || port > 65535 {
		return "", fmt.Errorf("port must be between 1 and 65535")
	}
	return fmt.Sprintf("127.0.0.1:%d", port), nil
}
