package fleetdeploy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/portal"
)

// containerRuntime is the worker side of the portal's committed container
// deployment request. The request is the idempotency record: it is stored
// before any call to the deployer and is compared in full on every replay.
type containerRuntime struct {
	w *Worker
	a assignment
}

type containerOperation struct {
	Request portal.ContainerRuntimeRequest `json:"request"`
	Domain  string                         `json:"domain"`
	Stage   string                         `json:"stage"`
}

func (r *containerRuntime) statePath(id string) string {
	return filepath.Join(r.w.cfg.StateDirectory, appName(r.a.id)+"-container-"+id+".json")
}

func (r *containerRuntime) fencePath() string {
	return filepath.Join(r.w.cfg.StateDirectory, appName(r.a.id)+"-container-fence.json")
}

func validContainerRequest(a assignment, q portal.ContainerRuntimeRequest) bool {
	d := q.Deployment
	if a.p.Kind != "container" || a.p.Architecture != "arm64" || q.ActivateBefore <= 0 || !hexID(a.id) || !validDomain(a.p.Domain) ||
		!hexID(d.ID) || !hexID(d.ProjectID) || !hexID(d.ReleaseID) || !hexID(d.RuntimeID) ||
		d.ProjectID != a.id || d.RuntimeID != a.p.RuntimeID || d.ReleaseID != q.Release.ID ||
		d.Revision < 1 || d.State != "running" || q.Release.ProjectID != a.id || q.ActivateBefore <= 0 ||
		!hexID(q.Release.ID) || q.Release.Revision < 1 || !validContainerRelease(a, q.Release) {
		return false
	}
	if q.Previous != nil {
		if !hexID(q.Previous.ID) || q.Previous.Revision < 1 || q.Previous.ProjectID != a.id || !validContainerRelease(a, *q.Previous) {
			return false
		}
	}
	return true
}

func readContainerOperation(path string) (containerOperation, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return containerOperation{}, err
	}
	var op containerOperation
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err = d.Decode(&op); err != nil {
		return containerOperation{}, errors.New("invalid container deployment state")
	}
	encoded, err := json.Marshal(op)
	if err != nil || !bytes.Equal(b, encoded) || (op.Stage != "preparing" && op.Stage != "dispatched" && op.Stage != "not_submitted") || op.Domain == "" {
		return containerOperation{}, errors.New("invalid container deployment state")
	}
	return op, nil
}

func (r *containerRuntime) readFence() (int64, string, error) {
	b, err := os.ReadFile(r.fencePath())
	if errors.Is(err, os.ErrNotExist) {
		return 0, "", nil
	}
	if err != nil {
		return 0, "", err
	}
	var f struct {
		Revision int64  `json:"revision"`
		ID       string `json:"deployment"`
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err = d.Decode(&f); err != nil || f.Revision < 1 || !hexID(f.ID) {
		return 0, "", errors.New("invalid container revision fence")
	}
	encoded, err := json.Marshal(f)
	if err != nil || !bytes.Equal(b, encoded) {
		return 0, "", errors.New("invalid container revision fence")
	}
	return f.Revision, f.ID, nil
}

func (r *containerRuntime) save(op containerOperation) error {
	b, err := json.Marshal(op)
	if err != nil {
		return err
	}
	return writeFsync(r.statePath(op.Request.Deployment.ID), b)
}

// rejectBeforeSubmission records proof that DeployApp was never called. Keep
// upstream errors out of the durable record: they may contain private details.
// If writing fails, the old preparing/dispatched record remains conservative.
func (r *containerRuntime) rejectBeforeSubmission(op containerOperation, cause error) error {
	op.Stage = "not_submitted"
	if err := r.save(op); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

func (r *containerRuntime) SubmitContainerRuntime(ctx context.Context, q portal.ContainerRuntimeRequest) error {
	if r.w == nil || r.w.lock == nil {
		return errors.New("exclusive fleet worker lock required")
	}
	if !validContainerRequest(r.a, q) {
		return errors.New("invalid container deployment request")
	}
	path := r.statePath(q.Deployment.ID)
	if _, err := os.ReadFile(path); err == nil {
		op, readErr := readContainerOperation(path)
		if readErr != nil || !reflect.DeepEqual(op.Request, q) || op.Domain != r.a.p.Domain {
			return errors.New("container deployment request conflict")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if q.ActivateBefore <= time.Now().Unix() || q.ActivateBefore > time.Now().Add(90*time.Second).Unix() {
		return errors.New("activation deadline expired")
	}
	if revision, deployment, err := r.readFence(); err != nil {
		return err
	} else if revision > q.Deployment.Revision || (revision == q.Deployment.Revision && deployment != q.Deployment.ID) {
		return errors.New("stale container deployment revision")
	}
	// This durable preparing record and the project fence both precede every
	// deployer RPC, including status and preflight.
	op := containerOperation{Request: q, Domain: r.a.p.Domain, Stage: "preparing"}
	if err := r.save(op); err != nil {
		return err
	}
	fence := struct {
		Revision   int64  `json:"revision"`
		Deployment string `json:"deployment"`
	}{q.Deployment.Revision, q.Deployment.ID}
	b, err := json.Marshal(fence)
	if err != nil {
		return err
	}
	if err = writeFsync(r.fencePath(), b); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return r.rejectBeforeSubmission(op, err)
	}
	spec, err := renderContainerYAML(r.a, q.Release)
	if err != nil {
		return r.rejectBeforeSubmission(op, err)
	}
	c, err := r.w.factory(r.a.id)
	if err != nil {
		return r.rejectBeforeSubmission(op, err)
	}
	defer c.Close()
	if _, err = c.PreflightApp(ctx, spec); err != nil {
		return r.rejectBeforeSubmission(op, err)
	}
	if ctx.Err() != nil || q.ActivateBefore <= time.Now().Unix() {
		return r.rejectBeforeSubmission(op, errors.New("activation deadline expired before dispatch"))
	}
	op.Stage = "dispatched"
	if err = r.save(op); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return r.rejectBeforeSubmission(op, err)
	}
	if q.ActivateBefore <= time.Now().Unix() {
		return r.rejectBeforeSubmission(op, errors.New("activation deadline expired after dispatch intent"))
	}
	_, err = c.DeployApp(ctx, spec)
	return err
}

func containerObservation(q portal.ContainerRuntimeRequest, state string) portal.ContainerRuntimeObservation {
	return portal.ContainerRuntimeObservation{DeploymentID: q.Deployment.ID, ReleaseID: q.Release.ID, RuntimeID: q.Deployment.RuntimeID, Revision: q.Deployment.Revision, State: state, ObservedAt: time.Now()}
}

func (r *containerRuntime) InspectContainerRuntime(ctx context.Context, q portal.ContainerRuntimeRequest) (portal.ContainerRuntimeObservation, error) {
	if r.w == nil || r.w.lock == nil {
		return portal.ContainerRuntimeObservation{}, errors.New("exclusive fleet worker lock required")
	}
	if !validContainerRequest(r.a, q) {
		return portal.ContainerRuntimeObservation{}, errors.New("invalid container deployment request")
	}
	revision, deployment, err := r.readFence()
	if err != nil {
		return portal.ContainerRuntimeObservation{}, err
	}
	if revision > q.Deployment.Revision || (revision == q.Deployment.Revision && deployment != q.Deployment.ID) {
		return portal.ContainerRuntimeObservation{}, errors.New("container revision fence mismatch")
	}
	op, err := readContainerOperation(r.statePath(q.Deployment.ID))
	if errors.Is(err, os.ErrNotExist) {
		if r.w.lock != nil && q.ActivateBefore <= time.Now().Unix() {
			if q.Previous != nil {
				s, statusErr := r.w.status(ctx, r.a)
				if statusErr != nil || !healthyContainer(s, r.a, *q.Previous) {
					return containerObservation(q, "pending"), nil
				}
			}
			return containerObservation(q, "failed"), nil
		}
		return containerObservation(q, "pending"), nil
	}
	if err != nil || !reflect.DeepEqual(op.Request, q) || op.Domain != r.a.p.Domain {
		return portal.ContainerRuntimeObservation{}, errors.New("container deployment request conflict")
	}
	if op.Stage == "preparing" || op.Stage == "not_submitted" {
		if op.Stage == "preparing" && q.ActivateBefore > time.Now().Unix() {
			return containerObservation(q, "pending"), nil
		}
		if q.Previous != nil {
			s, statusErr := r.w.status(ctx, r.a)
			if statusErr != nil || !healthyContainer(s, r.a, *q.Previous) {
				return containerObservation(q, "pending"), nil
			}
		}
		return containerObservation(q, "failed"), nil
	}
	if revision != q.Deployment.Revision || deployment != q.Deployment.ID {
		return portal.ContainerRuntimeObservation{}, errors.New("missing dispatched container revision fence")
	}
	s, err := r.w.status(ctx, r.a)
	if err != nil {
		return containerObservation(q, "pending"), nil
	}
	if healthyContainer(s, r.a, q.Release) {
		return containerObservation(q, "succeeded"), nil
	}
	return containerObservation(q, "pending"), nil
}

var _ portal.ContainerRuntime = (*containerRuntime)(nil)
