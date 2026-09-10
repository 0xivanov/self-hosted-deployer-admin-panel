//go:build integration

package portal

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/domains"
)

type domainQuoteFunc func(context.Context, string) (domains.RegistrarQuote, error)

func (f domainQuoteFunc) QuoteDomain(ctx context.Context, name string) (domains.RegistrarQuote, error) {
	return f(ctx, name)
}

func TestDomainQuotesPersistenceIsolationExpiry(t *testing.T) {
	t.Parallel()
	s, path := newStore(t)
	a, session := verifiedAccount(t, s, "domain-owner@example.test")
	b, other := verifiedAccount(t, s, "domain-other@example.test")
	now := s.now()
	calls := 0
	p := domainQuoteFunc(func(_ context.Context, name string) (domains.RegistrarQuote, error) {
		calls++
		if name != "example.com" {
			t.Fatal(name)
		}
		return domains.RegistrarQuote{Domain: name, Available: true, PremiumChecked: true, Currency: "eur", RegistrationMinor: 900, RenewalMinor: 1000, CheckedAt: now}, nil
	})
	q, err := s.RequestDomainQuote(t.Context(), p, session.Token, a.WorkspaceID, "Example.COM", 200)
	if err != nil || q.Offer.RegistrationMinor != 1100 || q.Offer.RenewalMinor != 1200 {
		t.Fatal(q, err)
	}
	for _, workspace := range []string{a.WorkspaceID, b.WorkspaceID} {
		if _, err = s.DomainQuote(t.Context(), other.Token, workspace, q.ID); !errors.Is(err, ErrDenied) {
			t.Fatal(err)
		}
	}
	if _, err = s.db.Exec("INSERT INTO memberships VALUES(?,?,'developer')", b.ID, a.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RequestDomainQuote(t.Context(), p, other.Token, a.WorkspaceID, "example.com", 200); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("unauthorized provider request", calls)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopened.now = func() time.Time { return now.Add(5 * time.Minute) }
	saved, err := reopened.DomainQuote(t.Context(), session.Token, a.WorkspaceID, q.ID)
	if err != nil || !saved.Expired || saved.Offer.Domain != q.Offer.Domain || saved.Offer.RegistrationMinor != q.Offer.RegistrationMinor || saved.Offer.RenewalMinor != q.Offer.RenewalMinor || !saved.Offer.ExpiresAt.Equal(q.Offer.ExpiresAt) {
		t.Fatal(saved, err)
	}
	raw, err := json.Marshal(saved)
	if err != nil || strings.Contains(string(raw), "900") || strings.Contains(string(raw), "evidence") {
		t.Fatal(string(raw), err)
	}
}

func TestDomainQuoteRevocationAndLimit(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)
	a, session := verifiedAccount(t, s, "domain-revoke@example.test")
	p := domainQuoteFunc(func(ctx context.Context, name string) (domains.RegistrarQuote, error) {
		if err := s.Logout(ctx, session.Token); err != nil {
			t.Fatal(err)
		}
		return domains.RegistrarQuote{Domain: name, Available: true, PremiumChecked: true, Currency: "eur", RegistrationMinor: 900, RenewalMinor: 1000, CheckedAt: s.now()}, nil
	})
	if _, err := s.RequestDomainQuote(t.Context(), p, session.Token, a.WorkspaceID, "example.com", 0); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRow("SELECT count(*) FROM domain_quotes").Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	owner, valid := verifiedAccount(t, s, "domain-limit@example.test")
	if _, err := s.db.Exec("WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<1000) INSERT INTO domain_quotes SELECT 'limit-'||x,?,?, '{}','{}',0 FROM n", owner.WorkspaceID, owner.ID); err != nil {
		t.Fatal(err)
	}
	never := domainQuoteFunc(func(context.Context, string) (domains.RegistrarQuote, error) {
		t.Fatal("provider called despite quota")
		return domains.RegistrarQuote{}, nil
	})
	if _, err := s.RequestDomainQuote(t.Context(), never, valid.Token, owner.WorkspaceID, "example.com", 0); !errors.Is(err, ErrDomainQuoteLimit) {
		t.Fatal(err)
	}
}

func TestDomainQuoteRejectsInvalidEvidence(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)
	a, session := verifiedAccount(t, s, "domain-invalid@example.test")
	for _, tc := range []struct {
		name   string
		change func(*domains.RegistrarQuote)
	}{
		{"foreign domain", func(q *domains.RegistrarQuote) { q.Domain = "foreign.com" }},
		{"expired", func(q *domains.RegistrarQuote) { q.CheckedAt = s.now().Add(-5 * time.Minute) }},
		{"unknown premium", func(q *domains.RegistrarQuote) { q.PremiumChecked = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := domainQuoteFunc(func(context.Context, string) (domains.RegistrarQuote, error) {
				q := domains.RegistrarQuote{Domain: "example.com", Available: true, PremiumChecked: true, Currency: "eur", RegistrationMinor: 900, RenewalMinor: 1000, CheckedAt: s.now()}
				tc.change(&q)
				return q, nil
			})
			if _, err := s.RequestDomainQuote(t.Context(), p, session.Token, a.WorkspaceID, "example.com", 0); !errors.Is(err, domains.ErrQuote) {
				t.Fatal(err)
			}
		})
	}
	var count int
	if err := s.db.QueryRow("SELECT count(*) FROM domain_quotes").Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}
