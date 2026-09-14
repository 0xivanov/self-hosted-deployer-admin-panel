//go:build integration

package portal

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/hostingbilling"
)

func TestBillingStatusSubscriptionLifecycleAndIsolation(t *testing.T) {
	t.Parallel()
	s, _, owner, session := billingCheckoutFixture(t)
	ctx := t.Context()
	checkout, err := s.RequestBillingCheckout(ctx, session.Token, owner.WorkspaceID, "starter")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BindBillingCheckout(ctx, checkout.ID, "cs_test_lifecycle", "https://checkout.stripe.com/c/pay/cs_test_lifecycle"); err != nil {
		t.Fatal(err)
	}
	checkoutEvent(t, s, "evt_lifecycle", "checkout.session.completed", "cs_test_lifecycle", checkout.CustomerID, checkout.ID, "sub_lifecycle")
	if _, err = s.ProcessBillingCheckoutEvent(ctx, "evt_lifecycle"); err != nil {
		t.Fatal(err)
	}
	status, err := s.WorkspaceBillingStatus(ctx, session.Token, owner.WorkspaceID)
	if err != nil || len(status.Subscriptions) != 1 {
		t.Fatal(status, err)
	}
	if v := status.Subscriptions[0]; v.State != "pending" || !v.Stale || v.CheckoutID != checkout.ID || v.Plan != "starter" {
		t.Fatal(v)
	}
	now := time.Now().Truncate(time.Second)
	s.now = func() time.Time { return now }
	for _, state := range []string{"active", "trialing", "past_due", "unpaid", "canceled", "incomplete", "incomplete_expired", "paused"} {
		err = s.ReconcileBillingSubscription(ctx, "sub_lifecycle", subscriptionReaderFunc(func(_ context.Context, id, customer, price string) (hostingbilling.SubscriptionSnapshot, error) {
			return hostingbilling.SubscriptionSnapshot{ID: id, CustomerID: customer, PriceID: price, Status: state, ObservedAt: now.Unix(), PeriodEnd: now.Unix() + 3600, CancelAtPeriodEnd: true, CollectionPaused: true, InvoiceID: "in_private", InvoiceStatus: "open"}, nil
		}))
		if err != nil {
			t.Fatal(err)
		}
		status, err = s.WorkspaceBillingStatus(ctx, session.Token, owner.WorkspaceID)
		if err != nil {
			t.Fatal(err)
		}
		v := status.Subscriptions[0]
		if v.State != state || v.Stale || !v.CancelAtPeriodEnd || !v.CollectionPaused || v.PeriodEnd != now.Unix()+3600 || v.InvoiceStatus != "open" {
			t.Fatal(v)
		}
		raw, _ := json.Marshal(status)
		for _, secret := range []string{checkout.CustomerID, checkout.PriceID, "in_private"} {
			if secret != "" && strings.Contains(string(raw), secret) {
				t.Fatalf("private provider identity exposed: %s", secret)
			}
		}
	}
	now = now.Add(5 * time.Minute)
	status, err = s.WorkspaceBillingStatus(ctx, session.Token, owner.WorkspaceID)
	if err != nil || !status.Subscriptions[0].Stale {
		t.Fatal(status, err)
	}
	other, foreign := verifiedAccount(t, s, "lifecycle-other@example.test")
	if _, err = s.WorkspaceBillingStatus(ctx, foreign.Token, owner.WorkspaceID); !errors.Is(err, ErrDenied) {
		t.Fatal("foreign owner", err)
	}
	if _, err = s.db.Exec("INSERT INTO memberships VALUES(?,?,'developer')", other.ID, owner.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.WorkspaceBillingStatus(ctx, foreign.Token, owner.WorkspaceID); !errors.Is(err, ErrDenied) {
		t.Fatal("developer", err)
	}
	status, err = s.WorkspaceBillingStatus(ctx, foreign.Token, other.WorkspaceID)
	if err != nil || len(status.Subscriptions) != 0 {
		t.Fatal("foreign subscription leak", status, err)
	}
	if _, err = s.db.Exec("UPDATE billing_subscriptions SET snapshot=NULL WHERE id='sub_lifecycle'"); err != nil {
		t.Fatal(err)
	}
	status, err = s.WorkspaceBillingStatus(ctx, session.Token, owner.WorkspaceID)
	if err != nil || status.Subscriptions[0].State != "pending" {
		t.Fatal(status, err)
	}
}
