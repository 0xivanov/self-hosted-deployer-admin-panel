//go:build linux

package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/containerbuild"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodebuildapi"
)

const maxProjectControllers = 5

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func readPrivate(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 16*1024 {
		return nil, errors.New("private file invalid")
	}
	return os.ReadFile(path)
}
func readConfig(path string) (containerbuild.Config, error) {
	b, err := readPrivate(path)
	if err != nil {
		return containerbuild.Config{}, errors.New("config unavailable")
	}
	var c containerbuild.Config
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	if err = d.Decode(&c); err != nil {
		return c, errors.New("invalid config")
	}
	if err = d.Decode(&struct{}{}); err != io.EOF {
		return c, errors.New("invalid config")
	}
	for _, p := range []string{c.TokenFile, c.TLSCert, c.TLSKey, c.StateDirectory, c.DependenciesDirectory} {
		if p == "" || !filepath.IsAbs(p) {
			return c, errors.New("config paths must be absolute")
		}
	}
	if !privateListen(c.Listen) {
		return c, errors.New("listen must use a private or VPN IP address")
	}
	return c, nil
}
func privateListen(address string) bool {
	host, port, err := net.SplitHostPort(address)
	portNumber, portErr := strconv.Atoi(port)
	if err != nil || portErr != nil || portNumber < 1 || portNumber > 65535 {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast())
}
func run() error {
	p := flag.String("config", "", "private executor config JSON")
	flag.Parse()
	if *p == "" || flag.NArg() != 0 {
		return errors.New("config required")
	}
	c, err := readConfig(*p)
	if err != nil {
		return err
	}
	b, err := readPrivate(c.TokenFile)
	if err != nil {
		return errors.New("token unavailable")
	}
	token := strings.TrimSpace(string(b))
	var ex *containerbuild.Executor
	var h http.Handler
	var router *projectRouter
	if c.Project == "*" {
		router, err = newProjectRouter(c, token)
		if err != nil {
			return err
		}
		h = router
	} else {
		ex, err = containerbuild.New(c)
		if err != nil {
			return err
		}
		defer func() { _ = closeExecutor(ex) }()
		h, err = nodebuildapi.Handler(c.Host, c.Project, c.ToolchainSHA256, c.Architecture, token, ex)
	}
	if err != nil {
		return err
	}
	srv := &http.Server{Addr: c.Listen, Handler: h, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 40 * time.Second, WriteTimeout: 40 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 * 1024, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.ListenAndServeTLS(c.TLSCert, c.TLSKey) }()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	select {
	case err = <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if router != nil {
			return errors.Join(srv.Shutdown(shutdown), router.shutdown(shutdown))
		}
		return errors.Join(srv.Shutdown(shutdown), ex.Shutdown(shutdown))
	}
}

type projectController struct {
	executor *containerbuild.Executor
	handler  http.Handler
}
type projectRouter struct {
	cfg         containerbuild.Config
	token       string
	expected    [32]byte
	mu          sync.Mutex
	controllers map[string]projectController
}

func newProjectRouter(cfg containerbuild.Config, token string) (*projectRouter, error) {
	r := &projectRouter{cfg: cfg, token: token, expected: sha256.Sum256([]byte("Bearer " + token)), controllers: map[string]projectController{}}
	for _, root := range []string{cfg.StateDirectory, cfg.DependenciesDirectory} {
		if info, err := os.Lstat(root); err == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return nil, errors.New("project storage root is unsafe")
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	if err := os.MkdirAll(cfg.StateDirectory, 0700); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(cfg.StateDirectory)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !validProjectID(entry.Name()) {
			continue
		}
		deleted, err := r.isDeleted(entry.Name())
		if err != nil {
			return nil, err
		}
		if deleted {
			continue
		}
		if len(r.controllers) >= maxProjectControllers {
			return nil, errors.New("too many persisted project controllers")
		}
		controller, err := r.open(entry.Name())
		if err != nil {
			return nil, err
		}
		r.controllers[entry.Name()] = controller
	}
	return r, nil
}
func validProjectID(id string) bool {
	if len(id) != 64 {
		return false
	}
	b, err := hex.DecodeString(id)
	return err == nil && hex.EncodeToString(b) == id
}
func (r *projectRouter) authenticate(req *http.Request, project string) bool {
	auth := sha256.Sum256([]byte(req.Header.Get("Authorization")))
	return req.TLS != nil && req.Host == r.cfg.Host && validProjectID(project) && len(req.Header.Values("Authorization")) == 1 && subtle.ConstantTimeCompare(r.expected[:], auth[:]) == 1 && len(req.Header.Values("Origin")) == 0 && req.Header.Get("Sec-Fetch-Site") == ""
}
func (r *projectRouter) open(project string) (projectController, error) {
	deleted, err := r.isDeleted(project)
	if err != nil {
		return projectController{}, err
	}
	if deleted {
		return projectController{}, errors.New("project has been deleted")
	}
	cfg := r.cfg
	cfg.Project = project
	cfg.StateDirectory = filepath.Join(r.cfg.StateDirectory, project)
	cfg.DependenciesDirectory = filepath.Join(r.cfg.DependenciesDirectory, project)
	if err := os.MkdirAll(cfg.StateDirectory, 0700); err != nil {
		return projectController{}, err
	}
	if err := os.MkdirAll(cfg.DependenciesDirectory, 0700); err != nil {
		return projectController{}, err
	}
	ex, err := containerbuild.New(cfg)
	if err != nil {
		return projectController{}, err
	}
	h, err := nodebuildapi.Handler(cfg.Host, project, cfg.ToolchainSHA256, cfg.Architecture, r.token, ex)
	if err != nil {
		_ = ex.Shutdown(context.Background())
		return projectController{}, err
	}
	return projectController{executor: ex, handler: h}, nil
}
func (r *projectRouter) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	project := req.Header.Get("X-Project-ID")
	if !r.authenticate(req, project) {
		http.Error(w, "Management access denied", http.StatusForbidden)
		return
	}
	if req.Method == http.MethodDelete && req.URL.EscapedPath() == "/v1/project" && req.URL.RawQuery == "" && req.ContentLength == 0 {
		r.deleteProject(w, req.Context(), project)
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	controller, ok := r.controllers[project]
	if !ok {
		if len(r.controllers) >= maxProjectControllers {
			http.Error(w, "Build executor unavailable", http.StatusServiceUnavailable)
			return
		}
		var err error
		controller, err = r.open(project)
		if err != nil {
			http.Error(w, "Build executor unavailable", http.StatusServiceUnavailable)
			return
		}
		r.controllers[project] = controller
	}
	controller.handler.ServeHTTP(w, req)
}

func (r *projectRouter) deleteProject(w http.ResponseWriter, ctx context.Context, project string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if deleted, err := r.isDeleted(project); err != nil {
		http.Error(w, "project deletion unavailable", http.StatusInternalServerError)
		return
	} else if deleted {
		if err := r.removeProjectDirs(project); err != nil {
			http.Error(w, "project deletion unavailable", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if controller, ok := r.controllers[project]; ok {
		if err := controller.executor.Shutdown(ctx); err != nil {
			http.Error(w, "project deletion unavailable", http.StatusInternalServerError)
			return
		}
		delete(r.controllers, project)
	}
	if err := r.markDeleted(project); err != nil {
		http.Error(w, "project deletion unavailable", http.StatusInternalServerError)
		return
	}
	if err := r.removeProjectDirs(project); err != nil {
		http.Error(w, "project deletion unavailable", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (r *projectRouter) isDeleted(project string) (bool, error) {
	if !validProjectID(project) {
		return false, errors.New("invalid project id")
	}
	root, err := os.OpenRoot(r.cfg.StateDirectory)
	if err != nil {
		return false, err
	}
	defer root.Close()
	f, err := root.Open(project + ".deleted")
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, f.Close()
}

func (r *projectRouter) markDeleted(project string) error {
	root, err := os.OpenRoot(r.cfg.StateDirectory)
	if err != nil {
		return err
	}
	defer root.Close()
	f, err := root.OpenFile(project+".deleted", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = f.Sync(); err == nil {
		err = f.Close()
	} else {
		_ = f.Close()
	}
	if err != nil {
		return err
	}
	return syncDirectory(r.cfg.StateDirectory)
}

func (r *projectRouter) removeProjectDirs(project string) error {
	for _, parent := range []string{r.cfg.StateDirectory, r.cfg.DependenciesDirectory} {
		info, err := os.Lstat(parent)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("project storage root is unsafe")
		}
		root, err := os.OpenRoot(parent)
		if err != nil {
			return err
		}
		err = root.RemoveAll(project)
		if err == nil {
			err = syncDirectory(parent)
		}
		_ = root.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func syncDirectory(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	err = d.Sync()
	if closeErr := d.Close(); err == nil {
		err = closeErr
	}
	return err
}
func (r *projectRouter) shutdown(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var err error
	for id, c := range r.controllers {
		err = errors.Join(err, c.executor.Shutdown(ctx))
		delete(r.controllers, id)
	}
	return err
}

func closeExecutor(ex *containerbuild.Executor) error {
	if closer, ok := any(ex).(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}
