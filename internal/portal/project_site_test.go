package portal

import (
	"errors"
	"testing"
)

func TestProjectSiteUsesOnlyOwnedActiveDomain(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	a, session := verifiedAccount(t, s, "site-owner@example.test")
	_, other := verifiedAccount(t, s, "site-other@example.test")
	p, err := s.CreateProject(ctx, session.Token, a.WorkspaceID, "Custom site", "static")
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.CreateCustomDomain(ctx, session.Token, p.ID, "site.example.com")
	if err != nil {
		t.Fatal(err)
	}
	h := &HTTP{store: s}
	fallback := "https://temporary.example.com"
	for _, state := range []string{"pending", "verified", "active", "removing"} {
		if _, err = s.db.Exec("UPDATE project_domains SET state=? WHERE id=?", state, d.ID); err != nil {
			t.Fatal(err)
		}
		got, err := h.projectSite(ctx, session.Token, p.ID, fallback)
		want := fallback
		if state == "active" {
			want = "https://site.example.com"
		}
		if err != nil || got != want {
			t.Fatalf("%s: %q %v", state, got, err)
		}
	}
	if _, err := h.projectSite(ctx, other.Token, p.ID, fallback); !errors.Is(err, ErrDenied) {
		t.Fatalf("foreign project: %v", err)
	}
}
