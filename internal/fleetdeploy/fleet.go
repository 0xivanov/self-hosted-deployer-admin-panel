package fleetdeploy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/client"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/noderouter"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/portal"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/publisher"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/staticpublish"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type Project struct {
	Kind            string `json:"kind"`
	Domain          string `json:"domain"`
	RuntimeID       string `json:"runtime_id"`
	ToolchainSHA256 string `json:"toolchain_sha256"`
	Architecture    string `json:"architecture"`
}
type Config struct {
	EnableCandidateOperations  bool               `json:"enable_candidate_operations,omitempty"`
	ContainerCredentialKeyFile string             `json:"container_credential_key_file,omitempty"`
	EnableContainerDeployments bool               `json:"enable_container_deployments,omitempty"`
	Database                   string             `json:"database"`
	DeployerBinary             string             `json:"deployer_binary"`
	DeployerConfig             string             `json:"deployer_config"`
	StateDirectory             string             `json:"state_directory"`
	ImageBuilder               string             `json:"image_builder"`
	Context                    string             `json:"context,omitempty"`
	Projects                   map[string]Project `json:"projects"`
}
type Deployer interface {
	PreflightApp(context.Context, string) (client.PreflightResult, error)
	DeployApp(context.Context, string) (client.DeployResult, error)
	GetAppStatus(context.Context, string) (client.AppStatusResult, error)
	Close() error
}
type Factory func(string) (Deployer, error)

func hexID(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && hex.EncodeToString(b) == s
}
func validDomain(s string) bool {
	u, e := url.Parse("https://" + s)
	return e == nil && u.Host == s && u.Hostname() != "" && u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == ""
}
func validImage(s string) bool {
	i := strings.LastIndex(s, "@sha256:")
	if i < 1 || len(s) != i+8+64 {
		return false
	}
	return hexID(s[i+8:]) && !strings.ContainsAny(s[:i], " \t\r\n@")
}

func LoadConfig(path string) (Config, error) {
	i, e := os.Stat(path)
	if e != nil {
		return Config{}, e
	}
	if !i.Mode().IsRegular() || i.Mode().Perm()&0077 != 0 {
		return Config{}, errors.New("fleet config must be private")
	}
	b, e := os.ReadFile(path)
	if e != nil {
		return Config{}, e
	}
	var c Config
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	if e = d.Decode(&c); e != nil {
		return c, e
	}
	if c.Database == "" || c.DeployerBinary == "" || c.DeployerConfig == "" || c.StateDirectory == "" || c.ImageBuilder == "" {
		return c, errors.New("required fleet config field missing")
	}
	if c.EnableCandidateOperations && !c.EnableContainerDeployments {
		return c, errors.New("candidate operations require container deployments to be enabled")
	}
	for id, p := range c.Projects {
		if !hexID(id) || (p.Kind != "static" && p.Kind != "node" && p.Kind != "container") || !validDomain(p.Domain) {
			return c, fmt.Errorf("invalid project %q", id)
		}
		if p.Kind == "container" && (!c.EnableContainerDeployments || !hexID(p.RuntimeID) || p.Architecture != "arm64") {
			return c, fmt.Errorf("invalid or disabled container assignment %q", id)
		}
		if p.Kind == "node" && (!hexID(p.RuntimeID) || !hexID(p.ToolchainSHA256) || p.Architecture != "arm64") {
			return c, fmt.Errorf("invalid node assignment %q", id)
		}
	}
	return c, nil
}

type Worker struct {
	containerEnvironments *portal.ContainerEnvironments
	containerCredentials  *portal.ContainerCredentials
	store                 *portal.Store
	cfg                   Config
	factory               Factory
	lock                  *os.File
}

func New(s *portal.Store, c Config) (*Worker, error) {
	if s == nil {
		return nil, errors.New("portal store required")
	}
	if e := os.MkdirAll(c.StateDirectory, 0700); e != nil {
		return nil, e
	}
	f, e := os.OpenFile(filepath.Join(c.StateDirectory, "worker.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		f.Close()
		return nil, errors.New("fleet worker already running")
	}
	w := &Worker{store: s, cfg: c, lock: f}
	if c.ContainerCredentialKeyFile != "" {
		if !c.EnableContainerDeployments {
			w.Close()
			return nil, errors.New("container credentials require container deployment support")
		}
		key, err := portal.LoadContainerCredentialKey(c.ContainerCredentialKeyFile)
		if err != nil {
			w.Close()
			return nil, err
		}
		w.containerCredentials, err = portal.NewContainerCredentials(s, key)
		if err != nil {
			w.Close()
			return nil, err
		}
		w.containerEnvironments, err = portal.NewContainerEnvironments(s, key)
		if err != nil {
			w.Close()
			return nil, err
		}
	}
	w.factory = func(_ string) (Deployer, error) { return client.New(c.DeployerBinary, c.DeployerConfig, c.Context) }
	return w, nil
}
func (w *Worker) Close() error {
	if w.lock == nil {
		return nil
	}
	lock := w.lock
	w.lock = nil
	syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	return lock.Close()
}
func appName(id string) string {
	if len(id) > 24 {
		id = id[:24]
	}
	return "site-" + id
}
func (w *Worker) Once(ctx context.Context) (bool, error) {
	for id, p := range w.cfg.Projects {
		a := assignment{id, p}
		var n bool
		var e error
		switch p.Kind {
		case "static":
			n, e = w.staticOnce(ctx, a)
		case "node":
			n, e = w.nodeOnce(ctx, a)
		case "container":
			if !w.cfg.EnableContainerDeployments {
				return false, errors.New("container deployment is disabled")
			}
			n, e = w.store.WorkContainerDeployment(ctx, a.id, a.p.RuntimeID, &containerRuntime{w: w, a: a})
		default:
			return false, errors.New("unsupported fleet project kind")
		}
		if e != nil {
			return n, e
		}
		if n {
			return true, nil
		}
	}
	return false, nil
}
func (w *Worker) Run(ctx context.Context, report func(error)) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		_, e := w.Once(ctx)
		if e != nil && report != nil {
			report(e)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

type assignment struct {
	id string
	p  Project
}

func renderYAML(a assignment, image string) string {
	rep, mode, spread := 2, "resilient", true
	return fmt.Sprintf("name: %s\nimage: %s\nservice:\n  port: 8080\n  health:\n    path: /\nrouting:\n  domain: %s\ndeploy:\n  replicas: %d\n  strategy: rolling\nplacement:\n  arch: linux/arm64\n  spread: %t\nstate:\n  mode: stateless\nresilience:\n  mode: %s\nhosting:\n  version: v1\n  maxReplicas: 2\n  resources:\n    requests:\n      cpu: 50m\n      memory: 64Mi\n      ephemeralStorage: 64Mi\n    limits:\n      cpu: 500m\n      memory: 256Mi\n      ephemeralStorage: 256Mi\n", appName(a.id), image, a.p.Domain, rep, spread, mode)
}
func (w *Worker) build(ctx context.Context, kind, project, digest string, archive []byte) (string, error) {
	f, e := os.CreateTemp(w.cfg.StateDirectory, "archive-")
	if e != nil {
		return "", e
	}
	name := f.Name()
	defer os.Remove(name)
	f.Chmod(0600)
	if _, e = f.Write(archive); e == nil {
		e = f.Close()
	} else {
		f.Close()
	}
	if e != nil {
		return "", e
	}
	o, e := exec.CommandContext(ctx, w.cfg.ImageBuilder, kind, project, digest, name).Output()
	if e != nil {
		return "", e
	}
	var v struct{ Image string }
	d := json.NewDecoder(strings.NewReader(string(o)))
	d.DisallowUnknownFields()
	if e = d.Decode(&v); e != nil || !validImage(v.Image) {
		return "", errors.New("image builder did not return immutable image")
	}
	if d.Decode(&struct{}{}) != io.EOF {
		return "", errors.New("image builder returned trailing data")
	}
	return v.Image, nil
}
func (w *Worker) deploy(ctx context.Context, a assignment, y string) (client.DeployResult, error) {
	c, e := w.factory(a.id)
	if e != nil {
		return client.DeployResult{}, e
	}
	defer c.Close()
	if _, e = c.PreflightApp(ctx, y); e != nil {
		return client.DeployResult{}, e
	}
	return c.DeployApp(ctx, y)
}
func (w *Worker) status(ctx context.Context, a assignment) (client.AppStatusResult, error) {
	c, e := w.factory(a.id)
	if e != nil {
		return client.AppStatusResult{}, e
	}
	defer c.Close()
	return c.GetAppStatus(ctx, appName(a.id))
}
func healthy(s client.AppStatusResult, image, domain string) bool {
	return s.App.Image == image && s.LatestDeployment.Status == "healthy" && s.RuntimeStatus == "healthy" && s.DesiredReplicas == 2 && s.AvailableReplicas >= 2 && len(s.Routes) > 0 && s.Routes[0].Status == "healthy" && s.Routes[0].Domain == domain
}
func writeFsync(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".state-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err == nil {
		err = d.Sync()
		d.Close()
	}
	return err
}
func (w *Worker) staticOnce(ctx context.Context, a assignment) (bool, error) {
	p, e := publisher.New(w.store, &publicationRuntime{w: w, a: a})
	if e != nil {
		return false, e
	}
	return p.Once(ctx)
}

type publicationRuntime struct {
	w *Worker
	a assignment
}

func (r *publicationRuntime) Project() string { return r.a.id }
func (r *publicationRuntime) Publish(ctx context.Context, revision int64, archive []byte) (staticpublish.Status, error) {
	sum := fmt.Sprintf("%x", sha256.Sum256(archive))
	var saved struct {
		Image, Release string
		Revision       int64
		Dispatched     bool
	}
	statePath := filepath.Join(r.w.cfg.StateDirectory, appName(r.a.id)+"-publication.json")
	if b, e := os.ReadFile(statePath); e == nil {
		if e = json.Unmarshal(b, &saved); e != nil {
			return staticpublish.Status{}, errors.New("invalid publication state")
		}
	}
	if saved.Revision > revision {
		return staticpublish.Status{}, errors.New("stale publication revision")
	}
	if saved.Revision == revision && saved.Release != "" && saved.Release != sum {
		return staticpublish.Status{}, errors.New("publication revision conflicts with stored release")
	}
	if saved.Revision == revision && saved.Release == sum && saved.Dispatched {
		s, e := r.w.status(ctx, r.a)
		if e == nil && healthy(s, saved.Image, r.a.p.Domain) {
			return staticpublish.Status{Release: sum, Revision: revision}, nil
		}
		return staticpublish.Status{}, errors.New("publication outcome unresolved; retained")
	}
	img := saved.Image
	var e error
	if saved.Revision != revision || saved.Release != sum || !validImage(img) {
		img, e = r.w.build(ctx, "static", r.a.id, sum, archive)
		if e != nil {
			return staticpublish.Status{}, errors.New("image build failed")
		}
		if e = writeFsync(statePath, []byte(fmt.Sprintf("{\"image\":%q,\"release\":%q,\"revision\":%d,\"dispatched\":false}", img, sum, revision))); e != nil {
			return staticpublish.Status{}, e
		}
	}
	s, e := r.w.status(ctx, r.a)
	if e == nil && healthy(s, img, r.a.p.Domain) {
		return staticpublish.Status{Release: sum, Revision: revision}, nil
	}
	c, err := r.w.factory(r.a.id)
	if err != nil {
		return staticpublish.Status{}, err
	}
	defer c.Close()
	spec := renderYAML(r.a, img)
	if _, err = c.PreflightApp(ctx, spec); err != nil {
		return staticpublish.Status{}, err
	}
	if ctx.Err() != nil {
		return staticpublish.Status{}, ctx.Err()
	}
	dispatched := struct {
		Image, Release string
		Revision       int64
		Dispatched     bool
	}{img, sum, revision, true}
	encoded, err := json.Marshal(dispatched)
	if err != nil {
		return staticpublish.Status{}, err
	}
	if err = writeFsync(statePath, encoded); err != nil {
		return staticpublish.Status{}, err
	}
	if d, err := c.DeployApp(ctx, spec); err != nil || d.Deployment.Status != "healthy" {
		return staticpublish.Status{}, errors.New("core deployment unresolved")
	}
	s, e = r.w.status(ctx, r.a)
	if e != nil || !healthy(s, img, r.a.p.Domain) {
		return staticpublish.Status{}, errors.New("core runtime is not healthy")
	}
	return staticpublish.Status{Release: sum, Revision: revision}, nil
}

type runtime struct {
	w                                      *Worker
	a                                      assignment
	image, artifact, operation, deployment string
	revision                               int64
	stage                                  string
	prior                                  *noderouter.Candidate
	previousImage                          string
}

type nodeOperation struct {
	Image, Artifact, Operation, Deployment string
	Revision                               int64
	Stage                                  string
	Prior                                  *noderouter.Candidate
	PreviousImage                          string
	Request                                portal.NodeRuntimeRequest
}

func (r *runtime) SubmitNodeRuntime(ctx context.Context, q portal.NodeRuntimeRequest) error {
	if !hexID(q.OperationID) || !hexID(q.DeploymentID) || !hexID(q.ProjectID) || !hexID(q.RuntimeID) || !hexID(q.ArtifactSHA256) || q.ProjectID != r.a.id || q.RuntimeID != r.a.p.RuntimeID || q.ToolchainSHA256 != r.a.p.ToolchainSHA256 || q.Architecture != r.a.p.Architecture || q.Revision < 1 {
		return errors.New("invalid Node deployment request")
	}
	opPath := filepath.Join(r.w.cfg.StateDirectory, appName(r.a.id)+"-"+q.OperationID+".json")
	if b, err := os.ReadFile(opPath); err == nil {
		var saved nodeOperation
		if json.Unmarshal(b, &saved) != nil || saved.Operation != q.OperationID || saved.Deployment != q.DeploymentID || saved.Artifact != q.ArtifactSHA256 || saved.Revision != q.Revision {
			return errors.New("operation identity conflict")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	fencePath := filepath.Join(r.w.cfg.StateDirectory, appName(r.a.id)+"-fence.json")
	if b, err := os.ReadFile(fencePath); err == nil {
		var f struct {
			Revision  int64
			Operation string
		}
		if json.Unmarshal(b, &f) != nil || !hexID(f.Operation) || f.Revision < 1 {
			return errors.New("invalid revision fence")
		}
		if f.Revision > q.Revision || (f.Revision == q.Revision && f.Operation != q.OperationID) {
			return errors.New("stale revision")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	prior, err := r.w.store.NodeRuntimePrior(ctx, r.a.id, r.a.p.RuntimeID)
	if err != nil {
		return err
	}
	record := nodeOperation{Artifact: q.ArtifactSHA256, Operation: q.OperationID, Deployment: q.DeploymentID, Revision: q.Revision, Stage: "preparing", Request: q}
	if prior != nil {
		record.Prior = prior.Routing.Active
		status, err := r.w.status(ctx, r.a)
		if err != nil {
			return err
		}
		record.PreviousImage = status.App.Image
	}
	save := func() error {
		b, err := json.Marshal(record)
		if err != nil {
			return err
		}
		return writeFsync(opPath, b)
	}
	if err = save(); err != nil {
		return err
	}
	if err = writeFsync(fencePath, []byte(fmt.Sprintf("{\"revision\":%d,\"operation\":%q}", q.Revision, q.OperationID))); err != nil {
		return err
	}
	// Preparation never runs customer code and cannot activate a route. A crash
	// in this stage is conclusively rejected by the next exclusive worker.
	if q.ActivateBefore <= time.Now().Unix() || q.ActivateBefore > time.Now().Add(90*time.Second).Unix() {
		return errors.New("activation deadline expired")
	}
	record.Image, err = r.w.build(ctx, "node", r.a.id, q.ArtifactSHA256, q.Archive)
	if err != nil {
		return err
	}
	c, err := r.w.factory(r.a.id)
	if err != nil {
		return err
	}
	defer c.Close()
	spec := renderYAML(r.a, record.Image)
	if _, err = c.PreflightApp(ctx, spec); err != nil {
		return err
	}
	if ctx.Err() != nil || q.ActivateBefore <= time.Now().Unix() {
		return errors.New("activation deadline expired before dispatch")
	}
	record.Stage = "dispatched"
	if err = save(); err != nil {
		return err
	}
	_, err = c.DeployApp(ctx, spec)
	return err
}
func (r *runtime) InspectNodeRuntime(ctx context.Context, run, project, op string) (portal.NodeRuntimeObservation, error) {
	if run != r.a.p.RuntimeID || project != r.a.id || r.loadState(op) != nil {
		return portal.NodeRuntimeObservation{}, errors.New("runtime assignment mismatch")
	}
	if r.stage == "preparing" {
		if r.prior != nil {
			status, err := r.w.status(ctx, r.a)
			if err != nil || !healthy(status, r.previousImage, r.a.p.Domain) {
				return portal.NodeRuntimeObservation{}, errors.New("previous route health unresolved")
			}
		}
		c := noderouter.Candidate{ProjectID: project, RuntimeID: run, DeploymentID: r.deployment, OperationID: op, Revision: r.revision, ArtifactSHA256: r.artifact, Backend: "core://" + appName(project)}
		return portal.NodeRuntimeObservation{Routing: noderouter.State{Fence: c, Active: r.prior, Status: "failed"}, ToolchainSHA256: r.a.p.ToolchainSHA256, Architecture: r.a.p.Architecture, Settled: true, CandidateStopped: true, ObservedAt: time.Now()}, nil
	}
	s, e := r.w.status(ctx, r.a)
	if e != nil {
		return portal.NodeRuntimeObservation{}, e
	}
	c := noderouter.Candidate{ProjectID: project, RuntimeID: run, DeploymentID: r.deployment, OperationID: op, Revision: r.revision, ArtifactSHA256: r.artifact, Backend: "core://" + appName(project)}
	o := portal.NodeRuntimeObservation{Routing: noderouter.State{Fence: c}, ToolchainSHA256: r.a.p.ToolchainSHA256, Architecture: r.a.p.Architecture, ObservedAt: time.Now()}
	if healthy(s, r.image, r.a.p.Domain) {
		o.Settled = true
		o.Healthy = true
		o.Routing.Status = "active"
		o.Routing.Active = &c
	} else {
		o.Routing.Status = "pending"
	}
	return o, nil
}
func (w *Worker) nodeOnce(ctx context.Context, a assignment) (bool, error) {
	r := &runtime{w: w, a: a}
	return w.store.WorkNodeDeployment(ctx, a.id, a.p.RuntimeID, a.p.ToolchainSHA256, a.p.Architecture, r)
}
func (r *runtime) loadState(op string) error {
	if !hexID(op) {
		return errors.New("invalid operation")
	}
	b, err := os.ReadFile(filepath.Join(r.w.cfg.StateDirectory, appName(r.a.id)+"-"+op+".json"))
	if err != nil {
		return err
	}
	var v nodeOperation
	if err = json.Unmarshal(b, &v); err != nil || v.Operation != op || (v.Stage != "preparing" && !validImage(v.Image)) || !hexID(v.Artifact) || !hexID(v.Deployment) || v.Revision < 1 {
		return errors.New("invalid persisted operation")
	}
	r.image, r.artifact, r.operation, r.deployment, r.revision = v.Image, v.Artifact, v.Operation, v.Deployment, v.Revision
	r.stage, r.prior, r.previousImage = v.Stage, v.Prior, v.PreviousImage
	return nil
}
