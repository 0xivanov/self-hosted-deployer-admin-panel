//go:build integration

package portal

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/registryimage"
)

type containerObserveFunc func(context.Context, ContainerRuntimeRequest) (ContainerRuntimeObservation, error)

func (f containerObserveFunc) InspectContainerRuntime(ctx context.Context, q ContainerRuntimeRequest) (ContainerRuntimeObservation, error) {
	return f(ctx, q)
}
func containerRunning(t *testing.T) (*Store, Session, Project, ContainerDeployment, ContainerRuntimeRequest) {
	t.Helper()
	s, _, session, p, _, d := containerQueued(t)
	var request ContainerRuntimeRequest
	_, err := s.DispatchContainerDeployment(t.Context(), p.ID, d.RuntimeID, containerSubmitFunc(func(_ context.Context, q ContainerRuntimeRequest) error { request = q; return nil }))
	if err != nil {
		t.Fatal(err)
	}
	return s, session, p, d, request
}
func observationFor(s *Store, q ContainerRuntimeRequest, state string) ContainerRuntimeObservation {
	return ContainerRuntimeObservation{DeploymentID: q.Deployment.ID, ReleaseID: q.Release.ID, RuntimeID: q.Deployment.RuntimeID, Revision: q.Deployment.Revision, State: state, ObservedAt: s.now()}
}
func TestContainerReconciliationValidatesEvidence(t *testing.T) {
	for _, mode := range []string{"stale", "wrong-release", "wrong-revision", "future", "unknown", "pending", "succeeded"} {
		t.Run(mode, func(t *testing.T) {
			s, session, p, d, q := containerRunning(t)
			o := observationFor(s, q, "succeeded")
			switch mode {
			case "stale":
				o.ObservedAt = s.now().Add(-2 * time.Minute)
			case "wrong-release":
				o.ReleaseID = "wrong"
			case "wrong-revision":
				o.Revision++
			case "future":
				o.ObservedAt = s.now().Add(time.Minute)
			case "unknown":
				o.State = "healthy"
			case "pending":
				o.State = "pending"
			}
			_, err := s.ReconcileContainerDeployment(t.Context(), p.ID, d.RuntimeID, containerObserveFunc(func(context.Context, ContainerRuntimeRequest) (ContainerRuntimeObservation, error) { return o, nil }))
			if mode == "pending" || mode == "succeeded" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, ErrInvalid) {
				t.Fatalf("bad evidence accepted: %v", err)
			}
			jobs, err := s.ContainerDeployments(t.Context(), session.Token, p.ID)
			if err != nil {
				t.Fatal(err)
			}
			want := "running"
			if mode == "succeeded" {
				want = "succeeded"
			}
			if jobs[0].State != want {
				t.Fatalf("state=%s want=%s", jobs[0].State, want)
			}
		})
	}
}
func TestContainerReconciliationRetainsPreviousReleaseForRollback(t *testing.T) {
	s, session, p, d, q := containerRunning(t)
	_, err := s.ReconcileContainerDeployment(t.Context(), p.ID, d.RuntimeID, containerObserveFunc(func(context.Context, ContainerRuntimeRequest) (ContainerRuntimeObservation, error) {
		return observationFor(s, q, "succeeded"), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	in := q.Release.Input
	in.Port = 9090
	newRelease, err := s.PrepareContainerRelease(t.Context(), session.Token, p.ID, "prepare-second-release", in, func(context.Context, string, string) (registryimage.Candidate, error) { return q.Release.Image, nil })
	if err != nil {
		t.Fatal(err)
	}
	next, err := s.RequestContainerDeployment(t.Context(), session.Token, p.ID, newRelease.ID, "second-container-request", d.RuntimeID)
	if err != nil || next.Revision != 2 {
		t.Fatalf("second request: %+v %v", next, err)
	}
	var second ContainerRuntimeRequest
	_, err = s.DispatchContainerDeployment(t.Context(), p.ID, d.RuntimeID, containerSubmitFunc(func(_ context.Context, request ContainerRuntimeRequest) error {
		if request.Previous == nil || request.Previous.ID != q.Release.ID {
			t.Fatal("previous release lost")
		}
		second = request
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.ReconcileContainerDeployment(t.Context(), p.ID, d.RuntimeID, containerObserveFunc(func(context.Context, ContainerRuntimeRequest) (ContainerRuntimeObservation, error) {
		return observationFor(s, second, "succeeded"), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	rollback, err := s.RequestContainerDeployment(t.Context(), session.Token, p.ID, q.Release.ID, "rollback-container-request", d.RuntimeID)
	if err != nil || rollback.Revision != 3 {
		t.Fatalf("rollback request: %+v %v", rollback, err)
	}
	_, err = s.DispatchContainerDeployment(t.Context(), p.ID, d.RuntimeID, containerSubmitFunc(func(_ context.Context, request ContainerRuntimeRequest) error {
		if request.Previous == nil || request.Previous.ID != newRelease.ID || request.Release.ID != q.Release.ID || request.Release.Input.Port != 8080 || request.Deployment.Revision != 3 {
			t.Fatalf("rollback lost release/settings: %+v", request)
		}
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}

}
func TestContainerReconciliationRejectsConcurrentIntentChange(t *testing.T) {
	s, _, p, d, q := containerRunning(t)
	_, err := s.ReconcileContainerDeployment(t.Context(), p.ID, d.RuntimeID, containerObserveFunc(func(context.Context, ContainerRuntimeRequest) (ContainerRuntimeObservation, error) {
		if _, err := s.db.Exec("UPDATE container_deployments SET dispatch_intent=? WHERE id=?", []byte("changed"), d.ID); err != nil {
			t.Fatal(err)
		}
		return observationFor(s, q, "succeeded"), nil
	}))
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("changed intent accepted: %v", err)
	}
}
