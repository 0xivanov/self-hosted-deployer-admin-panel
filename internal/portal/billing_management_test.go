//go:build integration

package portal

import (
	"context"
	"errors"
	"testing"
)

type managementFunc func(context.Context, string) (string, error)

func (f managementFunc) CreateManagementSession(ctx context.Context, customer string) (string, error) {
	return f(ctx, customer)
}
func TestBillingManagementOwnerAndRevocation(t *testing.T) {
	t.Parallel()
	s, _, a, session := billingCheckoutFixture(t)
	ctx := t.Context()
	calls := 0
	p := managementFunc(func(_ context.Context, customer string) (string, error) {
		calls++
		if customer != "cus_owner" {
			t.Error("wrong customer")
		}
		return "https://billing.stripe.com/p/session/test_fixture", nil
	})
	if link, err := s.BillingManagementURL(ctx, p, session.Token, a.WorkspaceID); err != nil || link == "" {
		t.Fatal(link, err)
	}
	b, other := verifiedAccount(t, s, "management-other@example.test")
	if _, err := s.BillingManagementURL(ctx, p, other.Token, a.WorkspaceID); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("INSERT INTO memberships VALUES(?,?,'developer')", b.ID, a.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BillingManagementURL(ctx, p, other.Token, a.WorkspaceID); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("unauthorized request contacted provider")
	}
	revoked := managementFunc(func(ctx context.Context, customer string) (string, error) {
		if err := s.Logout(ctx, session.Token); err != nil {
			t.Fatal(err)
		}
		return "https://billing.stripe.com/p/session/test_fixture", nil
	})
	if link, err := s.BillingManagementURL(ctx, revoked, session.Token, a.WorkspaceID); !errors.Is(err, ErrDenied) || link != "" {
		t.Fatal("revoked session received management link", link, err)
	}
}

func TestBillingManagementHTTPProtection(t *testing.T) {
	t.Parallel()
	s, _, a, _ := billingCheckoutFixture(t)
	calls := 0
	p := managementFunc(func(context.Context, string) (string, error) {
		calls++
		return "https://billing.stripe.com/p/session/test_fixture", nil
	})
	h, err := NewHTTP(s, HTTPOptions{Origin: "https://portal.example.test", TestBilling: true, BillingManagement: p})
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf := httpLogin(t, h, a.Email)
	for _, tc := range []struct {
		name, csrf, body string
		code             int
	}{
		{"csrf missing", "", `{"workspace":"` + a.WorkspaceID + `"}`, 403},
		{"injected customer", csrf, `{"workspace":"` + a.WorkspaceID + `","customer":"cus_foreign"}`, 400},
		{"owner", csrf, `{"workspace":"` + a.WorkspaceID + `"}`, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := portalRequest(h, "POST", "/api/billing/manage", tc.body, h.origin, tc.csrf, cookie)
			if w.Code != tc.code {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
	if calls != 1 {
		t.Fatal("invalid HTTP requests contacted provider", calls)
	}
}
