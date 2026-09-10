//go:build integration

package portal

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func billingCheckoutFixture(t *testing.T) (*Store, string, Account, Session) {
	t.Helper()
	s, path := newStore(t)
	a, session := verifiedAccount(t, s, "checkout-owner@example.test")
	c, err := s.RequestBillingCustomer(t.Context(), session.Token, a.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BindBillingCustomer(t.Context(), c.RequestID, "cus_owner"); err != nil {
		t.Fatal(err)
	}
	if err = s.ConfigureBillingPlan(t.Context(), "starter", "price_first", true); err != nil {
		t.Fatal(err)
	}
	return s, path, a, session
}
func TestCheckoutIntentIsolationAndPersistence(t *testing.T) {
	t.Parallel()
	s, path, a, session := billingCheckoutFixture(t)
	ctx := t.Context()
	b, other := verifiedAccount(t, s, "checkout-other@example.test")
	if _, err := s.RequestBillingCheckout(ctx, other.Token, a.WorkspaceID, "starter"); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("INSERT INTO memberships VALUES(?,?,'developer')", b.ID, a.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RequestBillingCheckout(ctx, other.Token, a.WorkspaceID, "starter"); !errors.Is(err, ErrDenied) {
		t.Fatal("developer checkout", err)
	}
	if _, err := s.RequestBillingCheckout(ctx, session.Token, a.WorkspaceID, "price_from_browser"); !errors.Is(err, ErrInvalid) {
		t.Fatal("unconfigured plan", err)
	}
	results := make(chan BillingCheckout, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			c, err := s.RequestBillingCheckout(ctx, session.Token, a.WorkspaceID, "starter")
			if err != nil {
				t.Error(err)
			}
			results <- c
		})
	}
	wg.Wait()
	close(results)
	var expected BillingCheckout
	for c := range results {
		if expected.ID == "" {
			expected = c
		} else if expected != c {
			t.Fatal("duplicate checkout intent")
		}
	}
	if expected.CustomerID != "cus_owner" || expected.PriceID != "price_first" || expected.State != "pending" {
		t.Fatal(expected)
	}
	if _, err := s.RequestBillingCheckout(ctx, session.Token, a.WorkspaceID, "another-plan"); !errors.Is(err, ErrBillingConflict) {
		t.Fatal("pending plan replaced", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	work, err := s.BillingCheckoutWork(ctx, expected.ID)
	if err != nil || work != expected {
		t.Fatal(work, err)
	}
	link := "https://checkout.stripe.com/c/pay/cs_test_one"
	if err = s.BindBillingCheckout(ctx, expected.ID, "cs_test_one", link); err != nil {
		t.Fatal(err)
	}
	if err = s.BindBillingCheckout(ctx, expected.ID, "cs_test_one", link); err != nil {
		t.Fatal("retry bind", err)
	}
	if err = s.BindBillingCheckout(ctx, expected.ID, "cs_test_two", link); !errors.Is(err, ErrBillingConflict) {
		t.Fatal("session replaced", err)
	}
	opened, err := s.BillingCheckout(ctx, session.Token, a.WorkspaceID, expected.ID)
	if err != nil || opened.State != "open" || opened.URL != link {
		t.Fatal(opened, err)
	}
	if _, err = s.BillingCheckout(ctx, other.Token, a.WorkspaceID, expected.ID); !errors.Is(err, ErrDenied) {
		t.Fatal("developer billing link leak", err)
	}
	customer, err := s.RequestBillingCustomer(ctx, other.Token, b.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BindBillingCustomer(ctx, customer.RequestID, "cus_other"); err != nil {
		t.Fatal(err)
	}
	second, err := s.RequestBillingCheckout(ctx, other.Token, b.WorkspaceID, "starter")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BindBillingCheckout(ctx, second.ID, "cs_test_one", link); !errors.Is(err, ErrBillingConflict) {
		t.Fatal("cross-workspace session reused", err)
	}
	if err = s.BindBillingCheckout(ctx, second.ID, "cs_test_other", "https://evil.test/pay"); !errors.Is(err, ErrInvalid) {
		t.Fatal("untrusted checkout link", err)
	}
}
func TestCheckoutWorkProtectsPriceAndAuthority(t *testing.T) {
	t.Parallel()
	s, _, a, session := billingCheckoutFixture(t)
	ctx := t.Context()
	now := time.Now()
	s.now = func() time.Time { return now }
	c, err := s.RequestBillingCheckout(ctx, session.Token, a.WorkspaceID, "starter")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ConfigureBillingPlan(ctx, "starter", "price_changed", true); err != nil {
		t.Fatal(err)
	}
	if _, err = s.BillingCheckoutWork(ctx, c.ID); !errors.Is(err, ErrDenied) {
		t.Fatal("price change ignored", err)
	}
	same, err := s.RequestBillingCheckout(ctx, session.Token, a.WorkspaceID, "starter")
	if err != nil || same.PriceID != "price_first" || same.ID != c.ID {
		t.Fatal("snapshot mutated", same, err)
	}
	if err = s.ConfigureBillingPlan(ctx, "starter", "price_first", true); err != nil {
		t.Fatal(err)
	}
	now = now.Add(23 * time.Hour)
	if _, err = s.BillingCheckoutWork(ctx, c.ID); !errors.Is(err, ErrBillingConflict) {
		t.Fatal("old uncertain intent retried", err)
	}
	now = now.Add(-23 * time.Hour)
	if _, err = s.db.Exec("UPDATE memberships SET role='viewer' WHERE user_id=?", a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.BillingCheckoutWork(ctx, c.ID); !errors.Is(err, ErrDenied) {
		t.Fatal("revoked owner checkout", err)
	}
}
