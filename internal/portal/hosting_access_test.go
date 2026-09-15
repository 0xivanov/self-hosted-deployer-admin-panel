//go:build integration

package portal

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/hostingbilling"
)

func TestHostingAccessPolicyLegacyAndRequiredSubscription(t *testing.T) {
	t.Parallel()
	s, path, owner, session := billingCheckoutFixture(t)
	ctx := t.Context()
	now := time.Unix(1_700_000_000, 0)
	s.now = func() time.Time { return now }
	if err := s.ConfigureHostingPolicy(ctx, owner.WorkspaceID, true); err != nil {
		t.Fatal(err)
	}
	access, err := s.WorkspaceHostingAccess(ctx, session.Token, owner.WorkspaceID)
	if err != nil || access.Mode != "test_subscription" || access.Allowed {
		t.Fatalf("missing evidence = %#v, %v", access, err)
	}

	checkout, err := s.RequestBillingCheckout(ctx, session.Token, owner.WorkspaceID, "starter")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BindBillingCheckout(ctx, checkout.ID, "cs_test_hosting", "https://checkout.stripe.com/c/pay/cs_test_hosting"); err != nil {
		t.Fatal(err)
	}
	checkoutEvent(t, s, "evt_hosting", "checkout.session.completed", "cs_test_hosting", checkout.CustomerID, checkout.ID, "sub_hosting")
	if _, err = s.ProcessBillingCheckoutEvent(ctx, "evt_hosting"); err != nil {
		t.Fatal(err)
	}
	putHostingEvidence(t, s, checkout.CustomerID, now)
	access, err = s.WorkspaceHostingAccess(ctx, session.Token, owner.WorkspaceID)
	if err != nil || !access.Allowed {
		t.Fatalf("qualifying evidence = %#v, %v", access, err)
	}

	if _, err = s.CreateProject(ctx, session.Token, owner.WorkspaceID, "paid-site", "static"); err != nil {
		t.Fatal("qualified workspace cannot create project", err)
	}
	mutations := []struct {
		name   string
		change func(*hostingbilling.SubscriptionSnapshot, *hostingbilling.ChargeObservation)
	}{
		{"stale subscription", func(sub *hostingbilling.SubscriptionSnapshot, _ *hostingbilling.ChargeObservation) {
			sub.ObservedAt -= 900
		}},
		{"future subscription", func(sub *hostingbilling.SubscriptionSnapshot, _ *hostingbilling.ChargeObservation) {
			sub.ObservedAt += 1
		}},
		{"period ended", func(sub *hostingbilling.SubscriptionSnapshot, _ *hostingbilling.ChargeObservation) {
			sub.PeriodEnd = now.Unix()
		}},
		{"paused collection", func(sub *hostingbilling.SubscriptionSnapshot, _ *hostingbilling.ChargeObservation) {
			sub.CollectionPaused = true
		}},
		{"unpaid invoice", func(sub *hostingbilling.SubscriptionSnapshot, _ *hostingbilling.ChargeObservation) {
			sub.InvoiceStatus = "open"
		}},
		{"stale charge", func(_ *hostingbilling.SubscriptionSnapshot, charge *hostingbilling.ChargeObservation) {
			charge.ObservedAt -= 900
		}},
		{"future charge", func(_ *hostingbilling.SubscriptionSnapshot, charge *hostingbilling.ChargeObservation) {
			charge.ObservedAt++
		}},
		{"trial subscription", func(sub *hostingbilling.SubscriptionSnapshot, _ *hostingbilling.ChargeObservation) {
			sub.Status = "trialing"
		}},
		{"remaining payment", func(sub *hostingbilling.SubscriptionSnapshot, _ *hostingbilling.ChargeObservation) {
			sub.InvoiceRemaining = 1
		}},
		{"refunded charge", func(_ *hostingbilling.SubscriptionSnapshot, charge *hostingbilling.ChargeObservation) {
			charge.AmountRefunded = 1
		}},
		{"disputed charge", func(_ *hostingbilling.SubscriptionSnapshot, charge *hostingbilling.ChargeObservation) {
			charge.Disputed = true
		}},
		{"unchecked disputes", func(_ *hostingbilling.SubscriptionSnapshot, charge *hostingbilling.ChargeObservation) {
			charge.DisputesChecked = false
		}},
		{"unresolved dispute", func(_ *hostingbilling.SubscriptionSnapshot, charge *hostingbilling.ChargeObservation) {
			charge.Disputes = []hostingbilling.ChargeDispute{{ID: "dp_hosting", Status: "under_review"}}
		}},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			putHostingEvidence(t, s, checkout.CustomerID, now)
			var sub hostingbilling.SubscriptionSnapshot
			var charge hostingbilling.ChargeObservation
			if err := readHostingEvidence(s, &sub, &charge); err != nil {
				t.Fatal(err)
			}
			tc.change(&sub, &charge)
			writeHostingEvidence(t, s, sub, charge)
			got, err := s.WorkspaceHostingAccess(ctx, session.Token, owner.WorkspaceID)
			if err != nil || got.Allowed {
				t.Fatalf("mutation allowed: %#v, %v", got, err)
			}
		})
	}

	// Malformed identity is an integrity error, never a grant.
	putHostingEvidence(t, s, checkout.CustomerID, now)
	var malformed hostingbilling.ChargeObservation
	if err = json.Unmarshal([]byte(`{"ChargeID":"ch_hosting","CustomerID":"cus_other"}`), &malformed); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("UPDATE billing_charges SET snapshot=? WHERE id='ch_hosting'", mustHostingJSON(malformed)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.WorkspaceHostingAccess(ctx, session.Token, owner.WorkspaceID); err == nil {
		t.Fatal("malformed charge identity accepted")
	}

	other, otherSession := verifiedAccount(t, s, "hosting-access-other@example.test")
	access, err = s.WorkspaceHostingAccess(ctx, otherSession.Token, other.WorkspaceID)
	if err != nil || access.Mode != "legacy" || !access.Allowed {
		t.Fatalf("workspace isolation = %#v, %v", access, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if access, err = s.WorkspaceHostingAccess(ctx, session.Token, owner.WorkspaceID); err != nil || access.Mode != "test_subscription" {
		t.Fatalf("policy after restart = %#v, %v", access, err)
	}
}

func putHostingEvidence(t *testing.T, s *Store, customer string, now time.Time) {
	t.Helper()
	sub := hostingbilling.SubscriptionSnapshot{ID: "sub_hosting", CustomerID: customer, PriceID: "price_first", Status: "active", PeriodStart: now.Unix() - 60, PeriodEnd: now.Unix() + 3600, InvoiceID: "in_hosting", InvoiceStatus: "paid", ObservedAt: now.Unix()}
	charge := hostingbilling.ChargeObservation{ChargeID: "ch_hosting", CustomerID: customer, PaymentIntentID: "pi_hosting", InvoiceID: "in_hosting", SubscriptionID: "sub_hosting", AmountCaptured: 1000, DisputesChecked: true, ObservedAt: now.Unix()}
	if _, err := s.db.Exec("INSERT OR IGNORE INTO billing_charges(id,subscription_id,customer_id,invoice_id,payment_intent_id) VALUES('ch_hosting','sub_hosting',?,'in_hosting','pi_hosting')", customer); err != nil {
		t.Fatal(err)
	}
	writeHostingEvidence(t, s, sub, charge)
}
func writeHostingEvidence(t *testing.T, s *Store, sub hostingbilling.SubscriptionSnapshot, charge hostingbilling.ChargeObservation) {
	t.Helper()
	if _, err := s.db.Exec("UPDATE billing_subscriptions SET snapshot=? WHERE id=?", mustHostingJSON(sub), sub.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE billing_charges SET snapshot=? WHERE id=?", mustHostingJSON(charge), charge.ChargeID); err != nil {
		t.Fatal(err)
	}
}
func readHostingEvidence(s *Store, sub *hostingbilling.SubscriptionSnapshot, charge *hostingbilling.ChargeObservation) error {
	var a, b []byte
	if err := s.db.QueryRow("SELECT snapshot FROM billing_subscriptions WHERE id='sub_hosting'").Scan(&a); err != nil {
		return err
	}
	if err := s.db.QueryRow("SELECT snapshot FROM billing_charges WHERE id='ch_hosting'").Scan(&b); err != nil {
		return err
	}
	if err := json.Unmarshal(a, sub); err != nil {
		return err
	}
	return json.Unmarshal(b, charge)
}
func mustHostingJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
