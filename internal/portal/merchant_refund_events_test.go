//go:build integration

package portal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/merchantbilling"
	stripe "github.com/stripe/stripe-go/v86"
	"net/http/httptest"
	"testing"
	"time"
)

func refundEventBody(t *testing.T, eventID, account string, refund MerchantRefund, intent string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"id": eventID, "object": "event", "type": "refund.updated", "api_version": stripe.APIVersion, "created": 1700000000, "livemode": false, "account": account, "data": map[string]any{"object": map[string]any{"id": "re_event", "object": "refund", "payment_intent": intent, "metadata": map[string]string{"merchant_order": refund.OrderID, "merchant_refund": refund.ID}, "status": "succeeded"}}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func TestMerchantRefundEventsRecoverUnknownSubmission(t *testing.T) {
	s, path, a, session, order := paidOrderFixture(t)
	ctx := t.Context()
	refund, err := s.RequestMerchantRefund(ctx, session.Token, a.WorkspaceID, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	creates := 0
	p := refundProvider{createRefund: func(context.Context, string, merchantbilling.RefundRequest) (merchantbilling.Refund, error) {
		creates++
		return merchantbilling.Refund{}, errors.New("lost reply")
	}}
	if _, err = s.DispatchMerchantRefund(ctx, refund.ID, p); err == nil {
		t.Fatal("expected unknown")
	}
	h, err := NewHTTP(s, HTTPOptions{Origin: "https://portal.example.test", Merchant: p, MerchantCountries: []string{"BG"}, TestMerchantWebhookSecret: billingSecret})
	if err != nil {
		t.Fatal(err)
	}
	deliver := func(body []byte) {
		t.Helper()
		r := httptest.NewRequest("POST", h.origin+"/webhooks/stripe-merchant-test", bytes.NewReader(body))
		r.Header.Set("Stripe-Signature", billingSignature(body))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 204 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	raw := refundEventBody(t, "evt_refund", "acct_orders", refund, "pi_paid")
	deliver(raw)
	deliver(raw)
	deliver(refundEventBody(t, "evt_foreign_account", "acct_foreign", refund, "pi_paid"))
	deliver(refundEventBody(t, "evt_foreign_payment", "acct_orders", refund, "pi_foreign"))
	var count int
	if err = s.db.QueryRow("SELECT count(*) FROM merchant_events").Scan(&count); err != nil || count != 1 {
		t.Fatal("receipt mapping", count, err)
	}
	var state string
	if err = s.db.QueryRow("SELECT state FROM merchant_refunds WHERE id=?", refund.ID).Scan(&state); err != nil || state != "submitted" {
		t.Fatal("payload changed refund", state, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reads := 0
	p.readRefund = func(_ context.Context, account, id string, input merchantbilling.RefundRequest) (merchantbilling.Refund, error) {
		reads++
		if account != "acct_orders" || id != "re_event" || input.RequestID != refund.ID || input.PaymentIntentID != "pi_paid" {
			t.Fatal("wrong canonical refund scope")
		}
		return merchantbilling.Refund{ID: id, State: "failed", ObservedAt: time.Now().Unix()}, nil
	}
	done, failed, err := s.ProcessMerchantEvents(ctx, p, 10)
	if err != nil || done != 1 || failed != 0 {
		t.Fatal(done, failed, err)
	}
	if err = s.db.QueryRow("SELECT state FROM merchant_refunds WHERE id=?", refund.ID).Scan(&state); err != nil || state != "failed" {
		t.Fatal("event overrode canonical state", state, err)
	}
	done, failed, err = s.ProcessMerchantEvents(ctx, p, 10)
	if err != nil || done != 0 || failed != 0 || reads != 1 || creates != 1 {
		t.Fatal("duplicate refund event", done, failed, reads, creates, err)
	}
}

func TestMerchantRefundEventMigrationKeepsCheckoutReceipts(t *testing.T) {
	s, path, _, _, order := paidOrderFixture(t)
	if _, err := s.db.Exec("INSERT INTO merchant_events(id,account_id,order_id,session_id,event_type,body_hash,created_at,received_at) VALUES('evt_old','acct_orders',?,'cs_test_paid','checkout.session.completed','saved-hash',1,1)", order.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("ALTER TABLE merchant_events DROP COLUMN refund_request_id; ALTER TABLE merchant_events DROP COLUMN provider_refund_id; ALTER TABLE merchant_orders DROP COLUMN fulfilled_at; ALTER TABLE merchant_orders DROP COLUMN fulfilled_by; DROP TABLE merchant_order_recovery_grants; DROP TABLE merchant_order_recovery_codes; DROP TABLE merchant_buyer_sessions; DROP TABLE domain_orders; PRAGMA user_version=30"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var hash, request, provider string
	if err = reopened.db.QueryRow("SELECT body_hash,refund_request_id,provider_refund_id FROM merchant_events WHERE id='evt_old'").Scan(&hash, &request, &provider); err != nil || hash != "saved-hash" || request != "" || provider != "" {
		t.Fatal(hash, request, provider, err)
	}
}
