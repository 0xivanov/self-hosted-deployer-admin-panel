//go:build integration

package portal

import (
	"context"
	"errors"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/registryimage"
	"strings"
	"testing"
)

type containerSubmitFunc func(context.Context, ContainerRuntimeRequest) error

func (f containerSubmitFunc) SubmitContainerRuntime(c context.Context, q ContainerRuntimeRequest) error {
	return f(c, q)
}
func containerQueued(t *testing.T) (*Store, Account, Session, Project, ContainerRelease, ContainerDeployment) {
	t.Helper()
	s, a, session, p := containerProjectFixture(t)
	in := ContainerReleaseInput{Reference: "ghcr.io/acme/demo:tag", Port: 8080, HealthPath: "/"}
	r, err := s.PrepareContainerRelease(t.Context(), session.Token, p.ID, "prepare-container-deploy", in, func(context.Context, string, string) (registryimage.Candidate, error) {
		return containerCandidate(in.Reference), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.RequestContainerDeployment(t.Context(), session.Token, p.ID, r.ID, "request-container-deploy", strings.Repeat("d", 64))
	if err != nil {
		t.Fatal(err)
	}
	return s, a, session, p, r, d
}
func TestContainerDeploymentQueueAndCancel(t *testing.T) {
	s, _, session, p, r, d := containerQueued(t)
	same, err := s.RequestContainerDeployment(t.Context(), session.Token, p.ID, r.ID, "request-container-deploy", d.RuntimeID)
	if err != nil || same != d {
		t.Fatalf("retry: %+v %v", same, err)
	}
	if _, err = s.RequestContainerDeployment(t.Context(), session.Token, p.ID, r.ID, "another-container-deploy", d.RuntimeID); !errors.Is(err, ErrPublishing) {
		t.Fatalf("concurrent request: %v", err)
	}
	if _, err = s.DeleteProject(t.Context(), session.Token, p.ID, p.Name); !errors.Is(err, ErrProjectBusy) {
		t.Fatalf("deleted pending project: %v", err)
	}
	_, other := verifiedAccount(t, s, "foreign-deployment@example.test")
	if err = s.CancelContainerDeployment(t.Context(), other.Token, p.ID, d.ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("foreign cancel: %v", err)
	}
	if err = s.CancelContainerDeployment(t.Context(), session.Token, p.ID, d.ID); err != nil {
		t.Fatal(err)
	}
	next, err := s.RequestContainerDeployment(t.Context(), session.Token, p.ID, r.ID, "next-container-deployment", d.RuntimeID)
	if err != nil || next.Revision != 2 {
		t.Fatalf("new revision: %+v %v", next, err)
	}
}
func TestContainerDispatchPersistsIntentAndNeverResubmitsLostReply(t *testing.T) {
	s, _, session, p, _, d := containerQueued(t)
	calls := 0
	runtime := containerSubmitFunc(func(ctx context.Context, q ContainerRuntimeRequest) error {
		calls++
		var state string
		var raw []byte
		if err := s.db.QueryRow("SELECT state,dispatch_intent FROM container_deployments WHERE id=?", d.ID).Scan(&state, &raw); err != nil {
			t.Fatal(err)
		}
		if state != "running" || len(raw) == 0 || q.Deployment.ID != d.ID || q.Release.ProjectID != p.ID || q.ActivateBefore <= s.now().Unix() {
			t.Fatalf("missing durable intent: %+v", q)
		}
		return errors.New("lost reply")
	})
	worked, err := s.DispatchContainerDeployment(t.Context(), p.ID, d.RuntimeID, runtime)
	if !worked || err == nil {
		t.Fatalf("dispatch: %v %v", worked, err)
	}
	worked, err = s.DispatchContainerDeployment(t.Context(), p.ID, d.RuntimeID, runtime)
	if worked || err != nil || calls != 1 {
		t.Fatalf("duplicate submit: %v %v calls=%d", worked, err, calls)
	}
	if err = s.CancelContainerDeployment(t.Context(), session.Token, p.ID, d.ID); !errors.Is(err, ErrPublishing) {
		t.Fatalf("cancelled uncertain deployment: %v", err)
	}
	jobs, err := s.ContainerDeployments(t.Context(), session.Token, p.ID)
	if err != nil || len(jobs) != 1 || jobs[0].State != "running" {
		t.Fatalf("uncertain state lost: %+v %v", jobs, err)
	}
}
func TestContainerDispatchRechecksActor(t *testing.T) {
	s, a, _, p, _, d := containerQueued(t)
	if _, err := s.db.Exec("UPDATE users SET disabled=1 WHERE id=?", a.ID); err != nil {
		t.Fatal(err)
	}
	worked, err := s.DispatchContainerDeployment(t.Context(), p.ID, d.RuntimeID, containerSubmitFunc(func(context.Context, ContainerRuntimeRequest) error { t.Fatal("revoked actor dispatched"); return nil }))
	if !worked || err != nil {
		t.Fatalf("revoked dispatch: %v %v", worked, err)
	}
	var state string
	if err = s.db.QueryRow("SELECT state FROM container_deployments WHERE id=?", d.ID).Scan(&state); err != nil || state != "cancelled" {
		t.Fatalf("state: %s %v", state, err)
	}
}
