package nodeexecutor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodepipeline"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/npmfetch"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/portal"
)

var ErrUnavailable = errors.New("Node executor unavailable")

type Config struct{ Project, Toolchain, Architecture, Executions, Dependencies, PipelineConfig, PipelineScript, Python string }
type Executor struct {
	root    *os.Root
	deps    *os.Root
	lock    *os.File
	cfg     Config
	mu      sync.Mutex
	running *exec.Cmd
	active  string
	done    chan struct{}
	closed  bool
}

func validID(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && hex.EncodeToString(b) == s
}
func New(c Config) (*Executor, error) {
	if !validID(c.Project) || !validID(c.Toolchain) || (c.Architecture != "arm64" && c.Architecture != "amd64") {
		return nil, ErrUnavailable
	}
	for _, p := range []string{c.Executions, c.Dependencies, c.PipelineConfig, c.PipelineScript, c.Python} {
		if !filepath.IsAbs(p) {
			return nil, ErrUnavailable
		}
	}
	for _, p := range []string{c.Executions, c.Dependencies} {
		i, e := os.Lstat(p)
		if e != nil || !i.IsDir() || i.Mode().Perm()&0077 != 0 {
			return nil, ErrUnavailable
		}
	}
	for _, p := range []string{c.PipelineConfig, c.PipelineScript, c.Python} {
		i, e := os.Lstat(p)
		if e != nil || !i.Mode().IsRegular() || i.Mode().Perm()&0022 != 0 {
			return nil, ErrUnavailable
		}
	}
	i, _ := os.Lstat(c.Python)
	if i.Mode()&0111 == 0 {
		return nil, ErrUnavailable
	}
	info, err := os.Lstat(c.PipelineConfig)
	if err != nil || info.Mode().Perm()&0077 != 0 || info.Size() > 16384 {
		return nil, ErrUnavailable
	}
	raw, err := os.ReadFile(c.PipelineConfig)
	var pipeline struct{ DependenciesDirectory string }
	if err != nil || json.Unmarshal(raw, &pipeline) != nil || filepath.Clean(pipeline.DependenciesDirectory) != filepath.Clean(c.Dependencies) {
		return nil, ErrUnavailable
	}
	r, e := os.OpenRoot(c.Executions)
	if e != nil {
		return nil, ErrUnavailable
	}
	d, e := os.OpenRoot(c.Dependencies)
	if e != nil {
		r.Close()
		return nil, ErrUnavailable
	}
	l, e := r.OpenFile(".executor.lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if e != nil || syscall.Flock(int(l.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		if l != nil {
			l.Close()
		}
		d.Close()
		r.Close()
		return nil, ErrUnavailable
	}
	return &Executor{root: r, deps: d, lock: l, cfg: c}, nil
}
func (e *Executor) Close() error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	done := e.done
	e.mu.Unlock()
	if done != nil {
		<-done
	}
	if e.lock != nil {
		_ = syscall.Flock(int(e.lock.Fd()), syscall.LOCK_UN)
		_ = e.lock.Close()
	}
	_ = e.deps.Close()
	return e.root.Close()
}
func syncDir(r *os.Root) error {
	f, err := r.Open(".")
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
func write(r *os.Root, n string, b []byte) error {
	f, e := r.OpenFile(n, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return e
	}
	if _, e = f.Write(b); e == nil {
		e = f.Sync()
	}
	if x := f.Close(); e == nil {
		e = x
	}
	return e
}
func (e *Executor) SubmitNodeExecution(ctx context.Context, q portal.NodeExecutionRequest) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	hash := sha256.Sum256(q.Archive)
	if e.closed || ctx.Err() != nil || q.ProjectID != e.cfg.Project || q.ToolchainSHA256 != e.cfg.Toolchain || q.Plan.Architecture != e.cfg.Architecture || !validID(q.ExecutionID) || !validID(q.BuildID) || len(q.Archive) == 0 || len(q.Archive) > 10<<20 || hex.EncodeToString(hash[:]) != q.Plan.SourceSHA256 || !strings.HasPrefix(q.Bundle.Directory, "dependencies-") || !validID(strings.TrimPrefix(q.Bundle.Directory, "dependencies-")) || !validID(q.Bundle.ManifestSHA256) {
		return ErrUnavailable
	}
	if _, err := e.root.Lstat(q.ExecutionID); err == nil {
		old, err := e.request(q.ExecutionID)
		copy := q
		copy.Archive = nil
		if err != nil || !reflect.DeepEqual(old, copy) {
			return ErrUnavailable
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrUnavailable
	}
	if e.running != nil || q.NotAfter <= time.Now().Unix() || q.NotAfter > time.Now().Add(time.Minute).Unix() {
		return ErrUnavailable
	}
	bundle, err := npmfetch.DownloadBundle(ctx, q.Archive, q.Plan.SourceSHA256, e.deps, npmfetch.NewClient())
	if err != nil {
		return ErrUnavailable
	}
	if bundle.ManifestSHA256 != q.Bundle.ManifestSHA256 {
		_ = e.deps.RemoveAll(bundle.Directory)
		return ErrUnavailable
	}
	if _, err = e.deps.Lstat(q.Bundle.Directory); err == nil {
		_ = e.deps.RemoveAll(bundle.Directory)
	} else if errors.Is(err, os.ErrNotExist) {
		if err = e.deps.Rename(bundle.Directory, q.Bundle.Directory); err != nil {
			return ErrUnavailable
		}
	} else {
		return ErrUnavailable
	}
	if _, err = npmfetch.VerifyBundle(ctx, e.deps, q.Bundle, q.Plan.SourceSHA256); err != nil {
		return ErrUnavailable
	}
	if err = syncDir(e.deps); err != nil {
		return ErrUnavailable
	}
	if ctx.Err() != nil || time.Now().Unix() >= q.NotAfter {
		return ErrUnavailable
	}
	if err = e.root.Mkdir(q.ExecutionID, 0700); err != nil {
		return ErrUnavailable
	}
	r, err := e.root.OpenRoot(q.ExecutionID)
	if err != nil {
		return ErrUnavailable
	}
	defer r.Close()
	raw, err := json.Marshal(q)
	if err != nil {
		return ErrUnavailable
	}
	if err = write(r, "source.zip", q.Archive); err != nil {
		return ErrUnavailable
	}
	if err = write(r, "request.json", raw); err != nil {
		return ErrUnavailable
	}
	if err = syncDir(r); err != nil {
		return ErrUnavailable
	}
	if err = syncDir(e.root); err != nil {
		return ErrUnavailable
	}
	// Once persisted, this identity is never launched a second time, even after
	// a crash between recording the request and starting the controller.
	if time.Now().Unix() >= q.NotAfter {
		return ErrUnavailable
	}
	cmd := exec.Command(e.cfg.Python, e.cfg.PipelineScript, e.cfg.PipelineConfig, filepath.Join(e.cfg.Executions, q.ExecutionID))
	if err = cmd.Start(); err != nil {
		return ErrUnavailable
	}
	done := make(chan struct{})
	e.running, e.active, e.done = cmd, q.ExecutionID, done
	go func() {
		_ = cmd.Wait()
		e.mu.Lock()
		e.running = nil
		e.active = ""
		close(done)
		e.mu.Unlock()
	}()
	return nil
}
func (e *Executor) request(id string) (portal.NodeExecutionRequest, error) {
	var q portal.NodeExecutionRequest
	if !validID(id) {
		return q, ErrUnavailable
	}
	r, x := e.root.OpenRoot(id)
	if x != nil {
		return q, ErrUnavailable
	}
	defer r.Close()
	f, x := r.Open("request.json")
	if x != nil {
		return q, ErrUnavailable
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 16385))
	d.DisallowUnknownFields()
	if d.Decode(&q) != nil || d.Decode(&struct{}{}) != io.EOF || q.ExecutionID != id || q.ProjectID != e.cfg.Project || q.ToolchainSHA256 != e.cfg.Toolchain {
		return q, ErrUnavailable
	}
	return q, nil
}
func (e *Executor) InspectNodeExecution(ctx context.Context, id string) (portal.NodeExecutionObservation, error) {
	if !validID(id) {
		return portal.NodeExecutionObservation{}, ErrUnavailable
	}
	q, x := e.request(id)
	if x != nil {
		return portal.NodeExecutionObservation{}, x
	}
	r, x := e.root.OpenRoot(id)
	if x != nil {
		return portal.NodeExecutionObservation{}, ErrUnavailable
	}
	defer r.Close()
	o, x := (nodepipeline.Reader{Root: r, Request: q}).InspectNodeExecution(ctx, id)
	if x == nil {
		return o, nil
	}
	e.mu.Lock()
	active := e.running != nil && e.active == id
	e.mu.Unlock()
	if active {
		return portal.NodeExecutionObservation{ProjectID: q.ProjectID, ExecutionID: id, SourceSHA256: q.Plan.SourceSHA256, ToolchainSHA256: q.ToolchainSHA256, Architecture: q.Plan.Architecture, Outcome: "running", ObservedAt: time.Now()}, nil
	}
	return portal.NodeExecutionObservation{}, ErrUnavailable
}
func (e *Executor) ReadNodeArtifact(ctx context.Context, id string) (portal.NodeArtifactObservation, []byte, error) {
	if !validID(id) {
		return portal.NodeArtifactObservation{}, nil, ErrUnavailable
	}
	q, x := e.request(id)
	if x != nil {
		return portal.NodeArtifactObservation{}, nil, x
	}
	r, x := e.root.OpenRoot(id)
	if x != nil {
		return portal.NodeArtifactObservation{}, nil, ErrUnavailable
	}
	defer r.Close()
	return (nodepipeline.Reader{Root: r, Request: q}).ReadNodeArtifact(ctx, id)
}

var _ portal.NodeBuildExecutor = (*Executor)(nil)
