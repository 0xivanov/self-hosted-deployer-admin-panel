package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodebuildapi"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodeexecutor"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run() error {
	listen := flag.String("listen", "127.0.0.1:9443", "")
	host := flag.String("host", "127.0.0.1:9443", "")
	project := flag.String("project", "", "")
	toolchain := flag.String("toolchain", "", "")
	arch := flag.String("architecture", "arm64", "")
	token := new(string)
	tokenFile := flag.String("token-file", "", "private bearer token file")
	cert := flag.String("tls-cert", "", "")
	key := flag.String("tls-key", "", "")
	execs := flag.String("executions", "", "")
	deps := flag.String("dependencies", "", "")
	pc := flag.String("pipeline-config", "", "")
	ps := flag.String("pipeline-script", "", "")
	py := flag.String("python", "/usr/bin/python3", "")
	flag.Parse()
	info, err := os.Lstat(*tokenFile)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 128 {
		return errors.New("private token file required")
	}
	f, err := os.Open(*tokenFile)
	if err != nil {
		return errors.New("token file unavailable")
	}
	raw, err := io.ReadAll(io.LimitReader(f, 129))
	f.Close()
	if err != nil || len(raw) > 128 {
		return errors.New("invalid token file")
	}
	*token = strings.TrimSpace(string(raw))
	for _, address := range []string{*listen, *host} {
		hostname, _, err := net.SplitHostPort(address)
		if err != nil || net.ParseIP(hostname) == nil || !net.ParseIP(hostname).IsLoopback() {
			return errors.New("executor must bind loopback")
		}
	}
	for _, v := range []*string{project, toolchain, token, cert, key, execs, deps, pc, ps} {
		if *v == "" {
			return errors.New("all executor flags are required")
		}
	}
	e, err := nodeexecutor.New(nodeexecutor.Config{Project: *project, Toolchain: *toolchain, Architecture: *arch, Executions: *execs, Dependencies: *deps, PipelineConfig: *pc, PipelineScript: *ps, Python: *py})
	if err != nil {
		return err
	}
	defer e.Close()
	h, err := nodebuildapi.Handler(*host, *project, *toolchain, *arch, *token, e)
	if err != nil {
		return err
	}
	s := &http.Server{Addr: *listen, Handler: h, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 35 * time.Second, WriteTimeout: 40 * time.Second, MaxHeaderBytes: 16384, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		c, x := context.WithTimeout(context.Background(), 10*time.Second)
		defer x()
		_ = s.Shutdown(c)
	}()
	if err = s.ListenAndServeTLS(*cert, *key); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
