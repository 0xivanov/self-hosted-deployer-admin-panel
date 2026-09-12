//go:build linux

package noderuntime

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodelaunch"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/noderouter"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/noderuntimeapi"
)

type Config struct {
	Assignment            Assignment        `json:"assignment"`
	StateDirectory        string            `json:"state_directory"`
	Slots                 []nodelaunch.Slot `json:"slots"`
	ContentHost           string            `json:"content_host"`
	ContentListen         string            `json:"content_listen"`
	ContentCertificate    string            `json:"content_certificate"`
	ContentKey            string            `json:"content_key"`
	HealthPath            string            `json:"health_path"`
	ManagementHost        string            `json:"management_host"`
	ManagementListen      string            `json:"management_listen"`
	ManagementCertificate string            `json:"management_certificate"`
	ManagementKey         string            `json:"management_key"`
	ManagementToken       string            `json:"management_token"`
}

func Load(path string) (Config, error) {
	var cfg Config
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 65536 {
		return cfg, ErrAssignment
	}
	f, err := os.Open(path)
	if err != nil {
		return cfg, err
	}
	defer f.Close()
	decoder := json.NewDecoder(io.LimitReader(f, 65537))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&cfg) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return Config{}, ErrAssignment
	}
	return cfg, nil
}

// Run starts the private management API, public content listener and inbox runner.
// Runtime/toolchain/account provisioning is deliberately external to this process.
func Run(ctx context.Context, cfg Config, report func(error)) error {
	if os.Geteuid() != 0 || cfg.Assignment.Architecture != runtime.GOARCH || !filepath.IsAbs(cfg.StateDirectory) || cfg.ContentHost == cfg.ManagementHost {
		return ErrAssignment
	}
	host, _, err := net.SplitHostPort(cfg.ManagementListen)
	if err != nil {
		return ErrAssignment
	}
	ip := net.ParseIP(host)
	if ip == nil || (!ip.IsLoopback() && !ip.IsPrivate()) {
		return ErrAssignment
	}
	if err = os.MkdirAll(cfg.StateDirectory, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(cfg.StateDirectory)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return ErrAssignment
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 || stat.Gid != 0 {
		return ErrAssignment
	}
	inbox, err := Open(filepath.Join(cfg.StateDirectory, "inbox.db"), cfg.Assignment)
	if err != nil {
		return err
	}
	defer inbox.Close()
	a := cfg.Assignment
	pool, err := nodelaunch.OpenPool(filepath.Join(cfg.StateDirectory, "pool.db"), nodelaunch.PoolConfig{ProjectID: a.ProjectID, RuntimeID: a.RuntimeID, ToolchainSHA256: a.ToolchainSHA256, Architecture: a.Architecture, Slots: cfg.Slots})
	if err != nil {
		return err
	}
	defer pool.Close()
	controlDir := filepath.Join(cfg.StateDirectory, "control")
	if err = os.MkdirAll(controlDir, 0700); err != nil {
		return err
	}
	control, err := os.OpenRoot(controlDir)
	if err != nil {
		return err
	}
	defer control.Close()
	gate, err := nodelaunch.OpenControlGate(control)
	if err != nil {
		return err
	}
	releases, err := os.OpenRoot("/opt/deployer-node/releases")
	if err != nil {
		return err
	}
	defer releases.Close()
	backends := map[string]string{}
	for _, slot := range cfg.Slots {
		port := fmt.Sprint(slot.Port)
		backends["port-"+port] = "http://127.0.0.1:" + port
	}
	router, err := noderouter.Open(filepath.Join(cfg.StateDirectory, "routes.db"), noderouter.Config{ProjectID: a.ProjectID, RuntimeID: a.RuntimeID, ContentHost: cfg.ContentHost, HealthPath: cfg.HealthPath, Backends: backends})
	if err != nil {
		return err
	}
	defer router.Close()
	runner := &Runner{Inbox: inbox, Pool: pool, Gate: gate, Router: router, Releases: releases}
	management, err := noderuntimeapi.DeploymentHandler(cfg.ManagementHost, a.ProjectID, a.RuntimeID, cfg.ManagementToken, runner, inbox)
	if err != nil {
		return err
	}
	contentTLS, err := tls.LoadX509KeyPair(cfg.ContentCertificate, cfg.ContentKey)
	if err != nil {
		return errors.New("content TLS configuration unavailable")
	}
	managementTLS, err := tls.LoadX509KeyPair(cfg.ManagementCertificate, cfg.ManagementKey)
	if err != nil {
		return errors.New("management TLS configuration unavailable")
	}
	contentListener, err := net.Listen("tcp", cfg.ContentListen)
	if err != nil {
		return err
	}
	defer contentListener.Close()
	managementListener, err := net.Listen("tcp", cfg.ManagementListen)
	if err != nil {
		return err
	}
	defer managementListener.Close()
	contentServer := &http.Server{Handler: router, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384}
	managementServer := &http.Server{Handler: management, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 35 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16384}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	failures := make(chan error, 2)
	go func() {
		failures <- contentServer.Serve(tls.NewListener(contentListener, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{contentTLS}}))
	}()
	go func() {
		failures <- managementServer.Serve(tls.NewListener(managementListener, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{managementTLS}}))
	}()
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		timer := time.NewTimer(0)
		defer timer.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-timer.C:
			}
			_, err := runner.ProcessNext(runCtx)
			if runCtx.Err() != nil {
				return
			}
			if err != nil && report != nil {
				report(err)
			}
			timer.Reset(time.Second)
		}
	}()
	select {
	case <-ctx.Done():
		err = nil
	case err = <-failures:
	}
	cancel()
	shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	managementServer.Shutdown(shutdown)
	contentServer.Shutdown(shutdown)
	managementServer.Close()
	contentServer.Close()
	<-workerDone
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
