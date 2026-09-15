//go:build integration

package portal

import (
	"context"
	"encoding/json"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/merchantbilling"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestBuyerRefundStatusIsPrivateAndCurrent(t *testing.T) {
	s, _, a, session, order := paidOrderFixture(t)
	ctx := t.Context()
	buyer := newBuyerToken(t, s)
	if _, err := s.db.Exec("UPDATE merchant_orders SET buyer_hash=? WHERE id=?", digest(buyer), order.ID); err != nil {
		t.Fatal(err)
	}
	refund, err := s.RequestMerchantRefund(ctx, session.Token, a.WorkspaceID, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	p := refundProvider{createRefund: func(context.Context, string, merchantbilling.RefundRequest) (merchantbilling.Refund, error) {
		return merchantbilling.Refund{ID: "re_buyer", State: "pending", ObservedAt: time.Now().Unix()}, nil
	}}
	if _, err = s.DispatchMerchantRefund(ctx, refund.ID, p); err != nil {
		t.Fatal(err)
	}
	h, err := NewHTTP(s, HTTPOptions{Origin: "https://portal.example.test", Merchant: p, MerchantCountries: []string{"BG"}})
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: merchantBuyerCookie, Value: buyer}
	path := "/api/shop/order?order=" + order.ID
	read := func(want string) {
		t.Helper()
		w := portalRequest(h, "GET", path, "", "", "", cookie)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var got shopOrder
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Refund == nil || got.Refund.State != want || got.Refund.AmountMinor != 1250 || got.PaymentStatus != "paid" {
			t.Fatal(got)
		}
		for _, private := range []string{refund.ID, "re_buyer", "pi_paid", "acct_orders", buyer, "buyer_hash", "provider_id", "actor_id"} {
			if strings.Contains(w.Body.String(), private) {
				t.Fatal("private refund field", private)
			}
		}
	}
	read("pending")
	for _, state := range []string{"succeeded", "failed"} {
		p.readRefund = func(context.Context, string, string, merchantbilling.RefundRequest) (merchantbilling.Refund, error) {
			return merchantbilling.Refund{ID: "re_buyer", State: state, ObservedAt: time.Now().Unix()}, nil
		}
		if _, err = s.ReconcileMerchantRefund(ctx, refund.ID, "re_buyer", p); err != nil {
			t.Fatal(err)
		}
		read(state)
	}
	foreign := &http.Cookie{Name: merchantBuyerCookie, Value: newBuyerToken(t, s)}
	if w := portalRequest(h, "GET", path, "", "", "", foreign); w.Code != 404 {
		t.Fatal("foreign buyer", w.Code)
	}
	if w := portalRequest(h, "GET", path, "", "", "", nil); w.Code != 401 {
		t.Fatal("anonymous buyer", w.Code)
	}
}
