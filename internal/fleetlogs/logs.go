// Package fleetlogs provides a narrow local read-only bridge to fleet logs.
package fleetlogs

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/buildlog"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/client"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/fleetdeploy"
	"io"
	stdlog "log"
	"net"
	"net/http"
	"strings"
	"time"
)

func validID(id string) bool {
	v, e := hex.DecodeString(id)
	return e == nil && len(v) == 32 && hex.EncodeToString(v) == id
}
func ReadFleet(ctx context.Context, config, project string) (string, error) {
	if !validID(project) {
		return "", errors.New("invalid project")
	}
	cfg, err := fleetdeploy.LoadConfig(config)
	if err != nil {
		return "", err
	}
	if _, ok := cfg.Projects[project]; !ok {
		return "", errors.New("project is not assigned")
	}
	c, err := client.New(cfg.DeployerBinary, cfg.DeployerConfig, cfg.Context)
	if err != nil {
		return "", err
	}
	defer c.Close()
	var tail buildlog.Tail
	err = c.StreamLogs(ctx, "site-"+project[:24], 100, false, func(line string) error { _, e := tail.Write([]byte(line)); return e })
	if err != nil {
		return "", err
	}
	return buildlog.Sanitize(string(tail.Data)), nil
}
func Handler(read func(context.Context, string) (string, error)) http.Handler {
	slots := make(chan struct{}, 2)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != "GET" || r.URL.Path != "/logs" || !validID(r.URL.Query().Get("project")) {
			http.Error(w, "Invalid log request", 400)
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			http.Error(w, "Log service busy", 429)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		log, err := read(ctx, r.URL.Query().Get("project"))
		if err != nil {
			stdlog.Printf("Runtime log read failed: %s", buildlog.Sanitize(err.Error()))
			http.Error(w, "Runtime output unavailable", 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"log": buildlog.Sanitize(log)})
	})
}
func Reader(socket string) func(context.Context, string) (string, error) {
	transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}, DisableKeepAlives: true}
	c := &http.Client{Transport: transport, Timeout: 25 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect rejected") }}
	return func(ctx context.Context, project string) (string, error) {
		if !validID(project) {
			return "", errors.New("invalid project")
		}
		req, err := http.NewRequestWithContext(ctx, "GET", "http://fleet.local/logs?project="+project, nil)
		if err != nil {
			return "", err
		}
		response, err := c.Do(req)
		if err != nil {
			return "", err
		}
		defer response.Body.Close()
		if response.StatusCode != 200 {
			return "", errors.New("runtime output unavailable")
		}
		raw, err := io.ReadAll(io.LimitReader(response.Body, 16385))
		if err != nil || len(raw) > 16384 {
			return "", errors.New("invalid log response")
		}
		var data struct {
			Log string `json:"log"`
		}
		d := json.NewDecoder(strings.NewReader(string(raw)))
		d.DisallowUnknownFields()
		if err = d.Decode(&data); err != nil {
			return "", err
		}
		return buildlog.Sanitize(data.Log), nil
	}
}
