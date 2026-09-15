//go:build integration

package portal

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestMerchantFulfillmentOwnerReplayAndBuyerVisibility(t *testing.T) {
	s, path, a, session, order := paidOrderFixture(t)
	ctx := t.Context()
	buyer := randomToken()
	if _, err := s.db.Exec("UPDATE merchant_orders SET buyer_hash=? WHERE id=?", digest(buyer), order.ID); err != nil {
		t.Fatal(err)
	}
	h, err := NewHTTP(s, HTTPOptions{Origin: "https://portal.example.test", Merchant: shopProvider{}, MerchantCountries: []string{"BG"}})
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: h.cookie, Value: session.Token}
	body := `{"workspace":"` + a.WorkspaceID + `","order":"` + order.ID + `"}`
	if w := portalRequest(h, "POST", "/api/merchant/orders/fulfill", body, h.origin, "", cookie); w.Code != 403 {
		t.Fatal("csrf", w.Code)
	}
	w := portalRequest(h, "POST", "/api/merchant/orders/fulfill", body, h.origin, csrfFor(session.Token), cookie)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var saved MerchantOrder
	if err = json.Unmarshal(w.Body.Bytes(), &saved); err != nil || saved.FulfilledAt == 0 {
		t.Fatal(saved, err)
	}
	if strings.Contains(w.Body.String(), "fulfilled_by") || strings.Contains(w.Body.String(), a.ID) {
		t.Fatal("actor exposed")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	repeated, err := s.FulfillMerchantOrder(ctx, session.Token, a.WorkspaceID, order.ID)
	if err != nil || repeated.FulfilledAt != saved.FulfilledAt || repeated.FulfilledBy != a.ID {
		t.Fatal(repeated, err)
	}
	var count int
	if err = s.db.QueryRow("SELECT count(*) FROM audit_events WHERE action=?", "merchant.order_fulfilled:"+order.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate attestation", count, err)
	}
	h, err = NewHTTP(s, HTTPOptions{Origin: "https://portal.example.test", Merchant: shopProvider{}, MerchantCountries: []string{"BG"}})
	if err != nil {
		t.Fatal(err)
	}
	w = portalRequest(h, "GET", "/api/shop/order?order="+order.ID, "", "", "", &http.Cookie{Name: merchantBuyerCookie, Value: buyer})
	var got shopOrder
	if err = json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != 200 || got.FulfilledAt != saved.FulfilledAt {
		t.Fatal(w.Code, got, err)
	}
	if strings.Contains(w.Body.String(), a.ID) {
		t.Fatal("buyer saw owner identity")
	}
}
func TestMerchantFulfillmentRejectsUnpaidForeignAndRefunded(t *testing.T) {
	s, _, a, session, order := paidOrderFixture(t)
	ctx := t.Context()
	other, foreign := verifiedAccount(t, s, "fulfillment-other@example.test")
	if _, err := s.FulfillMerchantOrder(ctx, foreign.Token, other.WorkspaceID, order.ID); !errors.Is(err, ErrDenied) {
		t.Fatal("foreign order", err)
	}
	if _, err := s.db.Exec("INSERT INTO memberships VALUES(?,?,'developer')", other.ID, a.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FulfillMerchantOrder(ctx, foreign.Token, a.WorkspaceID, order.ID); !errors.Is(err, ErrDenied) {
		t.Fatal("developer", err)
	}
	if _, err := s.db.Exec("UPDATE merchant_orders SET payment_status='unpaid' WHERE id=?", order.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FulfillMerchantOrder(ctx, session.Token, a.WorkspaceID, order.ID); !errors.Is(err, ErrBillingConflict) {
		t.Fatal("unpaid", err)
	}
	if _, err := s.db.Exec("UPDATE merchant_orders SET payment_status='paid' WHERE id=?", order.ID); err != nil {
		t.Fatal(err)
	}
	refund, err := s.RequestMerchantRefund(ctx, session.Token, a.WorkspaceID, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"requested", "submitted", "pending", "requires_action", "succeeded"} {
		if _, err = s.db.Exec("UPDATE merchant_refunds SET state=? WHERE id=?", state, refund.ID); err != nil {
			t.Fatal(err)
		}
		if _, err = s.FulfillMerchantOrder(ctx, session.Token, a.WorkspaceID, order.ID); !errors.Is(err, ErrBillingConflict) {
			t.Fatal("refund should block", state, err)
		}
	}
	if _, err = s.db.Exec("UPDATE merchant_refunds SET state='failed' WHERE id=?", refund.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.FulfillMerchantOrder(ctx, session.Token, a.WorkspaceID, order.ID); err != nil {
		t.Fatal("failed refund blocks paid order", err)
	}
}
