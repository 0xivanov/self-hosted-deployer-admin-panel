//go:build integration

package portal

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"

	stripe "github.com/stripe/stripe-go/v86"
)

func checkoutEvent(t *testing.T, s *Store, id, kind, session, customer, reference, subscription string) {
	t.Helper()
	status := "complete"
	if kind == "checkout.session.expired" {
		status = "expired"
	}
	object := map[string]any{"id": session, "object": "checkout.session", "livemode": false, "mode": "subscription", "status": status, "customer": customer, "client_reference_id": reference, "subscription": subscription}
	b, err := json.Marshal(map[string]any{"id": id, "object": "event", "api_version": stripe.APIVersion, "type": kind, "created": 1700000000, "livemode": false, "data": map[string]any{"object": object}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcceptBillingWebhook(t.Context(), b, billingSignature(b), billingSecret); err != nil {
		t.Fatal(err)
	}
}
func TestCheckoutEventBindingAndDeliveryOrder(t *testing.T) {
	t.Parallel()
	s, _, a, session := billingCheckoutFixture(t)
	ctx := t.Context()
	c, err := s.RequestBillingCheckout(ctx, session.Token, a.WorkspaceID, "starter")
	if err != nil {
		t.Fatal(err)
	}
	checkoutEvent(t, s, "evt_early", "checkout.session.completed", "cs_test_saved", c.CustomerID, c.ID, "sub_expected")
	if _, err = s.ProcessBillingCheckoutEvent(ctx, "evt_early"); !errors.Is(err, ErrBillingUnmatched) {
		t.Fatal("unknown session assigned via reference", err)
	}
	if err = s.BindBillingCheckout(ctx, c.ID, "cs_test_saved", "https://checkout.stripe.com/c/pay/cs_test_saved"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, customer, reference string }{
		{"customer", "cus_foreign", c.ID}, {"reference", c.CustomerID, "forged-reference"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := "evt_bad_" + tc.name
			checkoutEvent(t, s, id, "checkout.session.completed", "cs_test_saved", tc.customer, tc.reference, "sub_expected")
			if _, err = s.ProcessBillingCheckoutEvent(ctx, id); !errors.Is(err, ErrBillingConflict) {
				t.Fatal("event mismatch accepted", err)
			}
		})
	}
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			if done, err := s.ProcessBillingCheckoutEvent(ctx, "evt_early"); !done || err != nil {
				t.Error(done, err)
			}
		})
	}
	wg.Wait()
	var workspace, state string
	var count int
	if err = s.db.QueryRow("SELECT workspace_id,state FROM billing_subscriptions WHERE id='sub_expected'").Scan(&workspace, &state); err != nil || workspace != a.WorkspaceID || state != "awaiting_reconciliation" {
		t.Fatal(workspace, state, err)
	}
	if err = s.db.QueryRow("SELECT count(*) FROM audit_events WHERE action=?", "billing.checkout_completed:"+c.ID+":event:evt_early").Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate fulfillment audit", count, err)
	}
	checkoutEvent(t, s, "evt_late_expiry", "checkout.session.expired", "cs_test_saved", c.CustomerID, c.ID, "")
	if done, err := s.ProcessBillingCheckoutEvent(ctx, "evt_late_expiry"); !done || err != nil {
		t.Fatal(done, err)
	}
	got, err := s.BillingCheckout(ctx, session.Token, a.WorkspaceID, c.ID)
	if err != nil || got.State != "completed" {
		t.Fatal("completion downgraded", got, err)
	}
	checkoutEvent(t, s, "evt_changed_sub", "checkout.session.completed", "cs_test_saved", c.CustomerID, c.ID, "sub_replacement")
	if _, err = s.ProcessBillingCheckoutEvent(ctx, "evt_changed_sub"); !errors.Is(err, ErrBillingConflict) {
		t.Fatal("subscription replaced", err)
	}
}
func TestVerifiedExpiryAllowsNewCheckout(t *testing.T) {
	t.Parallel()
	s, _, a, session := billingCheckoutFixture(t)
	ctx := t.Context()
	c, err := s.RequestBillingCheckout(ctx, session.Token, a.WorkspaceID, "starter")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BindBillingCheckout(ctx, c.ID, "cs_test_expired", "https://checkout.stripe.com/c/pay/cs_test_expired"); err != nil {
		t.Fatal(err)
	}
	checkoutEvent(t, s, "evt_expiry", "checkout.session.expired", "cs_test_expired", c.CustomerID, c.ID, "")
	if done, err := s.ProcessBillingCheckoutEvent(ctx, "evt_expiry"); !done || err != nil {
		t.Fatal(done, err)
	}
	next, err := s.RequestBillingCheckout(ctx, session.Token, a.WorkspaceID, "starter")
	if err != nil || next.ID == c.ID {
		t.Fatal(next, err)
	}
	checkoutEvent(t, s, "evt_conflicting_complete", "checkout.session.completed", "cs_test_expired", c.CustomerID, c.ID, "sub_unexpected")
	if _, err = s.ProcessBillingCheckoutEvent(ctx, "evt_conflicting_complete"); !errors.Is(err, ErrBillingConflict) {
		t.Fatal("expired checkout reactivated", err)
	}
	var count int
	if err = s.db.QueryRow("SELECT count(*) FROM billing_subscriptions").Scan(&count); err != nil || count != 0 {
		t.Fatal("expiry granted subscription", count, err)
	}
}
