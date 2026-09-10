package portal

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestOwnerControlsAndRevocation(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)
	ctx := context.Background()
	a, as := verifiedAccount(t, s, "owner@example.test")
	b, bs := verifiedAccount(t, s, "member@example.test")
	if _, err := s.db.Exec("INSERT INTO memberships VALUES(?,?,'developer')", b.ID, a.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Members(ctx, bs.Token, a.WorkspaceID); !errors.Is(err, ErrDenied) {
		t.Fatal("developer can list members", err)
	}
	if err := s.ChangeMember(ctx, bs.Token, a.WorkspaceID, b.ID, "owner"); !errors.Is(err, ErrDenied) {
		t.Fatal("self escalation", err)
	}
	if err := s.ChangeMember(ctx, as.Token, b.WorkspaceID, b.ID, "viewer"); !errors.Is(err, ErrDenied) {
		t.Fatal("cross-workspace change", err)
	}
	if err := s.ChangeMember(ctx, as.Token, a.WorkspaceID, b.ID, "viewer"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, bs.Token); !errors.Is(err, ErrDenied) {
		t.Fatal("old session remains valid", err)
	}
	refreshed, err := s.Login(ctx, b.Email, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateProject(ctx, refreshed.Token, a.WorkspaceID, "blocked", "static"); !errors.Is(err, ErrDenied) {
		t.Fatal("viewer can write", err)
	}
	if err = s.ChangeMember(ctx, as.Token, a.WorkspaceID, b.ID, ""); err != nil {
		t.Fatal(err)
	}
	refreshed, err = s.Login(ctx, b.Email, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Projects(ctx, refreshed.Token, a.WorkspaceID); !errors.Is(err, ErrDenied) {
		t.Fatal("removed member retained access", err)
	}
	if err = s.ChangeMember(ctx, as.Token, a.WorkspaceID, b.ID, "owner"); !errors.Is(err, ErrDenied) {
		t.Fatal("change created membership without invitation", err)
	}
}
func TestLastOwnerCannotBeLostConcurrently(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)
	ctx := context.Background()
	a, as := verifiedAccount(t, s, "one@example.test")
	b, bs := verifiedAccount(t, s, "two@example.test")
	for _, role := range []string{"viewer", ""} {
		if err := s.ChangeMember(ctx, as.Token, a.WorkspaceID, a.ID, role); !errors.Is(err, ErrLastOwner) {
			t.Fatal("last owner lost", err)
		}
	}
	if _, err := s.db.Exec("INSERT INTO memberships VALUES(?,?,'owner')", b.ID, a.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	wg.Go(func() { results <- s.ChangeMember(ctx, as.Token, a.WorkspaceID, a.ID, "viewer") })
	wg.Go(func() { results <- s.ChangeMember(ctx, bs.Token, a.WorkspaceID, b.ID, "viewer") })
	wg.Wait()
	close(results)
	changed, blocked := 0, 0
	for err := range results {
		if err == nil {
			changed++
		} else if errors.Is(err, ErrLastOwner) {
			blocked++
		} else {
			t.Fatal(err)
		}
	}
	if changed != 1 || blocked != 1 {
		t.Fatal("concurrent owner removal", changed, blocked)
	}
}
func TestHTTPMembersRequireExplicitAction(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)
	a, _ := verifiedAccount(t, s, "owner@example.test")
	h, _ := NewHTTP(s, HTTPOptions{Origin: "https://portal.example.test"})
	cookie, csrf := httpLogin(t, h, a.Email)
	for _, tc := range []struct {
		name, body string
		want       int
	}{
		{"omitted role", `{"workspace":"` + a.WorkspaceID + `","user":"` + a.ID + `"}`, 400},
		{"last owner", `{"workspace":"` + a.WorkspaceID + `","user":"` + a.ID + `","role":""}`, 409},
		{"invalid role", `{"workspace":"` + a.WorkspaceID + `","user":"` + a.ID + `","role":"admin"}`, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := portalRequest(h, "POST", "/api/members", tc.body, h.origin, csrf, cookie)
			if w.Code != tc.want {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}
