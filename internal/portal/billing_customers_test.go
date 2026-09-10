//go:build integration

package portal

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestBillingCustomerOwnershipPersistenceAndBinding(t *testing.T) {
	t.Parallel()
	s, path := newStore(t)
	ctx := t.Context()
	a, session := verifiedAccount(t, s, "billing-owner@example.test")
	b, other := verifiedAccount(t, s, "billing-other@example.test")
	if _, err := s.RequestBillingCustomer(ctx, other.Token, a.WorkspaceID); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("INSERT INTO memberships VALUES(?,?,'developer')", b.ID, a.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RequestBillingCustomer(ctx, other.Token, a.WorkspaceID); !errors.Is(err, ErrDenied) {
		t.Fatal("developer billing access", err)
	}
	var wg sync.WaitGroup
	results := make(chan BillingCustomer, 2)
	for range 2 {
		wg.Go(func() {
			c, err := s.RequestBillingCustomer(ctx, session.Token, a.WorkspaceID)
			if err != nil {
				t.Error(err)
			}
			results <- c
		})
	}
	wg.Wait()
	close(results)
	var expected BillingCustomer
	for c := range results {
		if expected.RequestID == "" {
			expected = c
		} else if expected != c {
			t.Fatal("duplicate customer intent", expected, c)
		}
	}
	if expected.Email != a.Email || expected.CustomerID != "" {
		t.Fatal(expected)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	work, err := s.BillingCustomerWork(ctx, expected.RequestID)
	if err != nil || work != expected {
		t.Fatal(work, err)
	}
	if err = s.BindBillingCustomer(ctx, expected.RequestID, "cus_fixture"); err != nil {
		t.Fatal(err)
	}
	if err = s.BindBillingCustomer(ctx, expected.RequestID, "cus_fixture"); err != nil {
		t.Fatal("non-idempotent bind", err)
	}
	if err = s.BindBillingCustomer(ctx, expected.RequestID, "cus_replacement"); !errors.Is(err, ErrBillingConflict) {
		t.Fatal(err)
	}
	second, err := s.RequestBillingCustomer(ctx, other.Token, b.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BindBillingCustomer(ctx, second.RequestID, "cus_fixture"); !errors.Is(err, ErrBillingConflict) {
		t.Fatal("customer shared across workspaces", err)
	}
	c, err := s.BillingCustomer(ctx, session.Token, a.WorkspaceID)
	if err != nil || c.CustomerID != "cus_fixture" {
		t.Fatal(c, err)
	}
	if _, err = s.BillingCustomer(ctx, other.Token, a.WorkspaceID); !errors.Is(err, ErrDenied) {
		t.Fatal("developer read billing identity", err)
	}
}
func TestBillingCustomerWorkStopsExpiredOrRevokedRequests(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)
	ctx := t.Context()
	a, session := verifiedAccount(t, s, "old-billing@example.test")
	now := time.Now()
	s.now = func() time.Time { return now }
	c, err := s.RequestBillingCustomer(ctx, session.Token, a.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(23 * time.Hour)
	if _, err = s.BillingCustomerWork(ctx, c.RequestID); !errors.Is(err, ErrBillingConflict) {
		t.Fatal("old uncertain create retried", err)
	}
	now = now.Add(-23 * time.Hour)
	if _, err = s.db.Exec("UPDATE memberships SET role='developer' WHERE user_id=?", a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.BillingCustomerWork(ctx, c.RequestID); !errors.Is(err, ErrDenied) {
		t.Fatal("revoked owner work allowed", err)
	}
	// A provider response already obtained may still be retained for reconciliation.
	if err = s.BindBillingCustomer(ctx, c.RequestID, "cus_already_created"); err != nil {
		t.Fatal(err)
	}
}
