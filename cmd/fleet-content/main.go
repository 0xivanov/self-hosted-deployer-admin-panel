// fleet-content is the credential-free entrypoint baked into customer images.
package main

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var handler http.Handler
	childDone := make(chan error, 1)
	if os.Getenv("SITE_KIND") == "node" {
		// Existing uploads bind loopback. Keep that contract inside the pod and
		// expose only the content proxy on the Kubernetes service port.
		target, _ := url.Parse("http://127.0.0.1:3000")
		proxy := httputil.NewSingleHostReverseProxy(target)
		proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			http.Error(w, "Website is starting", http.StatusServiceUnavailable)
		}
		handler = proxy
		cmd := exec.CommandContext(ctx, "node", "/opt/deployer-node/toolchain/lib/node_modules/npm/bin/npm-cli.js", "start", "--ignore-scripts")
		cmd.Dir = "/app"
		cmd.Env = append(os.Environ(), "PORT=3000", "HOST=127.0.0.1", "NODE_ENV=production")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			return err
		}
		go func() { childDone <- cmd.Wait() }()
	} else {
		archive, err := zip.OpenReader("/site.zip")
		if err != nil {
			return err
		}
		defer archive.Close()
		files := http.FileServer(http.FS(archive))
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "GET" && r.Method != "HEAD" {
				w.Header().Set("Allow", "GET, HEAD")
				http.Error(w, "Method not allowed", 405)
				return
			}
			name := strings.TrimPrefix(r.URL.Path, "/")
			if name == "" || strings.HasSuffix(name, "/") {
				name += "index.html"
			}
			info, err := fs.Stat(archive, name)
			if err != nil || info.IsDir() {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("X-Content-Type-Options", "nosniff")
			files.ServeHTTP(w, r)
		})
	}
	server := &http.Server{Addr: ":8080", Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 32 << 10}
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	var result error
	select {
	case <-ctx.Done():
	case result = <-childDone:
		if result == nil {
			result = errors.New("website process exited")
		}
	case result = <-done:
	}
	stop()
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server.Shutdown(shutdown)
	if errors.Is(result, http.ErrServerClosed) {
		return nil
	}
	return result
}
