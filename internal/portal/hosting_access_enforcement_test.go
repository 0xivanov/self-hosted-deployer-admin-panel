//go:build integration

package portal

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestHostingGateBlocksNewWorkPreservesHistory(t *testing.T) {
	s, _, a, session, p, u := publicationFixture(t)
	ctx := t.Context()
	job, err := s.RequestPublication(ctx, session.Token, p.ID, u.ID, "before-billing-gate")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ConfigureHostingPolicy(ctx, a.WorkspaceID, true); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateProject(ctx, session.Token, a.WorkspaceID, "unpaid", "static"); !errors.Is(err, ErrHostingPayment) {
		t.Fatal("project allowed", err)
	}
	if _, err = s.SaveUpload(ctx, session.Token, p.ID, testArchive(t)); !errors.Is(err, ErrHostingPayment) {
		t.Fatal("upload allowed", err)
	}
	if _, err = s.RequestPublication(ctx, session.Token, p.ID, u.ID, "after-billing-gate"); !errors.Is(err, ErrHostingPayment) {
		t.Fatal("publish allowed", err)
	}
	if _, err = s.ClaimPublication(ctx, p.ID); !errors.Is(err, ErrHostingPayment) {
		t.Fatal("queued work allowed", err)
	}
	repeated, err := s.RequestPublication(ctx, session.Token, p.ID, u.ID, "before-billing-gate")
	if err != nil || repeated.ID != job.ID {
		t.Fatal("lost idempotent history", err)
	}
	if _, err = s.GetProject(ctx, session.Token, p.ID); err != nil {
		t.Fatal("read blocked", err)
	}
	if err = s.ConfigureHostingPolicy(ctx, a.WorkspaceID, false); err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimPublication(ctx, p.ID)
	if err != nil || claim == nil || claim.Job.ID != job.ID {
		t.Fatal("cannot resume existing job", err)
	}
}

func TestHostingGatePreventsNodeDispatchAfterClaim(t *testing.T) {
	t.Run("build", func(t *testing.T) {
		s, _, a, c, root, bundle := dependencyFixture(t)
		if err := s.BindNodeBuildDependencies(t.Context(), c.Job.ID, c.ExecutionID, c.Lease, root, bundle); err != nil {
			t.Fatal(err)
		}
		if err := s.ConfigureHostingPolicy(t.Context(), a.WorkspaceID, true); err != nil {
			t.Fatal(err)
		}
		calls := 0
		err := s.DispatchNodeBuild(t.Context(), c.Job.ID, c.ExecutionID, c.Lease, root, nodeSubmit(func(context.Context, NodeExecutionRequest) error { calls++; return nil }))
		if !errors.Is(err, ErrHostingPayment) || calls != 0 {
			t.Fatal("unpaid build dispatched", err, calls)
		}
	})
	t.Run("deployment", func(t *testing.T) {
		s, _, a, c := dispatchDeploymentFixture(t)
		if err := s.ConfigureHostingPolicy(t.Context(), a.WorkspaceID, true); err != nil {
			t.Fatal(err)
		}
		calls := 0
		err := s.DispatchNodeDeployment(t.Context(), c.Job.ID, c.OperationID, c.Lease, runtimeSubmit(func(context.Context, NodeRuntimeRequest) error { calls++; return nil }))
		if !errors.Is(err, ErrHostingPayment) || calls != 0 {
			t.Fatal("unpaid runtime dispatched", err, calls)
		}
	})
}

func TestHostingGateHTTPStatusAndIsolation(t *testing.T) {
	s, _ := newStore(t)
	a, session := verifiedAccount(t, s, "hosting-gate@example.test")
	other, _ := verifiedAccount(t, s, "hosting-gate-other@example.test")
	if err := s.ConfigureHostingPolicy(t.Context(), a.WorkspaceID, true); err != nil {
		t.Fatal(err)
	}
	h, err := NewHTTP(s, HTTPOptions{Origin: "https://portal.example.test", TestBilling: true})
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: h.cookie, Value: session.Token}
	w := portalRequest(h, "GET", "/api/billing/access?workspace="+a.WorkspaceID, "", "", "", cookie)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"allowed":false`) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = portalRequest(h, "GET", "/api/billing/access?workspace="+other.WorkspaceID, "", "", "", cookie)
	if w.Code != 403 {
		t.Fatal("foreign billing exposed", w.Code)
	}
	w = portalRequest(h, "POST", "/api/projects", `{"workspace":"`+a.WorkspaceID+`","name":"unpaid","kind":"static"}`, h.origin, csrfFor(session.Token), cookie)
	if w.Code != 402 {
		t.Fatal("unpaid HTTP project", w.Code, w.Body.String())
	}
}
