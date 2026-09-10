//go:build integration

package portal

import (
	"encoding/json"
	stripe "github.com/stripe/stripe-go/v86"
	"testing"
)

func TestInvoiceRefreshIdentity(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, kind string
		change     func(map[string]any)
		valid      bool
	}{
		{"paid", "invoice.paid", nil, true},
		{"failed", "invoice.payment_failed", nil, true},
		{"authentication required", "invoice.payment_action_required", nil, true},
		{"voided", "invoice.voided", nil, true},
		{"uncollectible", "invoice.marked_uncollectible", nil, true},
		{"finalized", "invoice.finalized", nil, true},
		{"foreign customer", "invoice.paid", func(v map[string]any) { v["customer"] = "cus_foreign" }, false},
		{"foreign subscription", "invoice.paid", func(v map[string]any) {
			v["parent"].(map[string]any)["subscription_details"].(map[string]any)["subscription"] = "sub_foreign"
		}, false},
		{"standalone invoice", "invoice.paid", func(v map[string]any) { v["parent"] = nil }, false},
		{"account scoped customer", "invoice.paid", func(v map[string]any) { v["customer_account"] = "acct_other" }, false},
		{"live object", "invoice.paid", func(v map[string]any) { v["livemode"] = true }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, a, session := billingCheckoutFixture(t)
			ctx := t.Context()
			c, err := s.RequestBillingCheckout(ctx, session.Token, a.WorkspaceID, "starter")
			if err != nil {
				t.Fatal(err)
			}
			if err = s.BindBillingCheckout(ctx, c.ID, "cs_test_invoice", "https://checkout.stripe.com/c/pay/cs_test_invoice"); err != nil {
				t.Fatal(err)
			}
			checkoutEvent(t, s, "evt_invoice_bind", "checkout.session.completed", "cs_test_invoice", c.CustomerID, c.ID, "sub_invoice")
			if _, err = s.ProcessBillingCheckoutEvent(ctx, "evt_invoice_bind"); err != nil {
				t.Fatal(err)
			}
			object := map[string]any{"id": "in_signal", "object": "invoice", "livemode": false, "customer": c.CustomerID, "status": "paid", "parent": map[string]any{"type": "subscription_details", "subscription_details": map[string]any{"subscription": "sub_invoice"}}}
			if tc.change != nil {
				tc.change(object)
			}
			body, err := json.Marshal(map[string]any{"id": "evt_invoice_signal", "object": "event", "api_version": stripe.APIVersion, "type": tc.kind, "created": 1700000000, "livemode": false, "data": map[string]any{"object": object}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.AcceptBillingWebhook(ctx, body, billingSignature(body), billingSecret); err != nil {
				t.Fatal(err)
			}
			worked, err := s.BillingWorkOnce(ctx, workerProvider{})
			if !worked {
				t.Fatal("invoice event not discovered")
			}
			if tc.valid && err != nil {
				t.Fatal(err)
			}
			if !tc.valid && err == nil {
				t.Fatal("unsafe invoice accepted")
			}
			var state string
			if err = s.db.QueryRow("SELECT state FROM billing_events WHERE id='evt_invoice_signal'").Scan(&state); err != nil {
				t.Fatal(err)
			}
			if tc.valid && state != "processed" || !tc.valid && state != "pending" {
				t.Fatal(state)
			}
			var subscriptionState string
			if err = s.db.QueryRow("SELECT state FROM billing_subscriptions WHERE id='sub_invoice'").Scan(&subscriptionState); err != nil || subscriptionState != "awaiting_reconciliation" {
				t.Fatal("invoice granted access", subscriptionState, err)
			}
		})
	}
}
