//go:build integration

package portal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/hostingbilling"
)

func TestBillingPaymentHistoryPagesAndIsolation(t *testing.T) {
	t.Parallel()
	s, _, owner, session := billingCheckoutFixture(t)
	ctx := t.Context()
	checkout, err := s.RequestBillingCheckout(ctx, session.Token, owner.WorkspaceID, "starter")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BindBillingCheckout(ctx, checkout.ID, "cs_test_history", "https://checkout.stripe.com/c/pay/cs_test_history"); err != nil {
		t.Fatal(err)
	}
	checkoutEvent(t, s, "evt_history", "checkout.session.completed", "cs_test_history", checkout.CustomerID, checkout.ID, "sub_history")
	if _, err = s.ProcessBillingCheckoutEvent(ctx, "evt_history"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Second)
	s.now = func() time.Time { return now }
	reader := chargeReaderFunc(func(_ context.Context, id string) (hostingbilling.ChargeObservation, error) {
		return hostingbilling.ChargeObservation{ChargeID: id, CustomerID: checkout.CustomerID, SubscriptionID: "sub_history", InvoiceID: "in_private", PaymentIntentID: "pi_private", Currency: "eur", AmountCaptured: 1000, AmountRefunded: 250, Disputed: true, DisputesChecked: true, Disputes: []hostingbilling.ChargeDispute{{ID: "dp_private", Status: "under_review", Amount: 1000}}, ObservedAt: now.Unix()}, nil
	})
	for i := 0; i < 21; i++ {
		if err = s.ReconcileBillingCharge(ctx, reader, fmt.Sprintf("ch_%03d", i)); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.WorkspaceBillingPayments(ctx, session.Token, owner.WorkspaceID, "")
	if err != nil || len(page.Payments) != 20 || page.NextCursor != "ch_001" {
		t.Fatal(page, err)
	}
	payment := page.Payments[0]
	if payment.ID != "ch_020" || payment.Pending || payment.Stale || payment.AmountRefunded != 250 || payment.AmountCaptured != 1000 || !payment.Disputed || len(payment.DisputeStatuses) != 1 || payment.DisputeStatuses[0] != "under_review" {
		t.Fatal(payment)
	}
	raw, _ := json.Marshal(page)
	for _, private := range []string{checkout.CustomerID, "sub_history", "in_private", "pi_private", "dp_private"} {
		if strings.Contains(string(raw), private) {
			t.Fatal("provider identity exposed", private)
		}
	}
	now = now.Add(5 * time.Minute)
	// Updating an observation must not reorder cursor pages.
	if err = s.ReconcileBillingCharge(ctx, reader, "ch_000"); err != nil {
		t.Fatal(err)
	}
	second, err := s.WorkspaceBillingPayments(ctx, session.Token, owner.WorkspaceID, page.NextCursor)
	if err != nil || len(second.Payments) != 1 || second.Payments[0].ID != "ch_000" || second.NextCursor != "" {
		t.Fatal(second, err)
	}
	page, err = s.WorkspaceBillingPayments(ctx, session.Token, owner.WorkspaceID, "")
	if err != nil || !page.Payments[0].Stale {
		t.Fatal(page, err)
	}
	if _, err = s.db.Exec("UPDATE billing_charges SET snapshot=NULL WHERE id='ch_020'"); err != nil {
		t.Fatal(err)
	}
	page, err = s.WorkspaceBillingPayments(ctx, session.Token, owner.WorkspaceID, "")
	if err != nil || !page.Payments[0].Pending || page.Payments[0].AmountCaptured != 0 {
		t.Fatal(page, err)
	}
	other, foreign := verifiedAccount(t, s, "history-other@example.test")
	if _, err = s.WorkspaceBillingPayments(ctx, foreign.Token, owner.WorkspaceID, ""); !errors.Is(err, ErrDenied) {
		t.Fatal("foreign owner", err)
	}
	if _, err = s.db.Exec("INSERT INTO memberships VALUES(?,?,'developer')", other.ID, owner.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.WorkspaceBillingPayments(ctx, foreign.Token, owner.WorkspaceID, ""); !errors.Is(err, ErrDenied) {
		t.Fatal("developer", err)
	}
	otherPage, err := s.WorkspaceBillingPayments(ctx, foreign.Token, other.WorkspaceID, "")
	if err != nil || len(otherPage.Payments) != 0 {
		t.Fatal("foreign payment leak", otherPage, err)
	}
	h, err := NewHTTP(s, HTTPOptions{Origin: "https://portal.example.test", TestBilling: true})
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: h.cookie, Value: session.Token}
	path := "/api/billing/payments?workspace=" + owner.WorkspaceID
	if w := portalRequest(h, "GET", path, "", "", "", cookie); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := portalRequest(h, "GET", path+"&before=invalid", "", "", "", cookie); w.Code != 400 {
		t.Fatal(w.Code)
	}
	if w := portalRequest(h, "GET", path, "", "", "", nil); w.Code != 401 {
		t.Fatal(w.Code)
	}
	h.billingEnabled = false
	if w := portalRequest(h, "GET", path, "", "", "", cookie); w.Code != 404 {
		t.Fatal(w.Code)
	}
}
