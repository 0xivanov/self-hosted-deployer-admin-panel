//go:build integration

package portal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/merchantbilling"
	stripe "github.com/stripe/stripe-go/v86"
)

func merchantEventBody(t *testing.T, id, account, order string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"id": id, "object": "event", "type": "checkout.session.completed", "api_version": stripe.APIVersion, "created": 1700000000, "livemode": false, "account": account, "data": map[string]any{"object": map[string]any{"id": "cs_test_event", "object": "checkout.session", "mode": "payment", "livemode": false, "client_reference_id": order, "metadata": map[string]string{"merchant_order": order}, "payment_status": "paid"}}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func TestMerchantEventsDurableLostReplyRecovery(t *testing.T) {
	s, path, _, _, product := orderFixture(t)
	ctx := t.Context()
	order, err := s.RequestMerchantOrder(ctx, randomToken(), product.ID, product.Revision, randomToken())
	if err != nil {
		t.Fatal(err)
	}
	p := shopProvider{checkoutProviderFixture: checkoutProviderFixture{create: func(context.Context, string, merchantbilling.CheckoutOrder) (merchantbilling.Checkout, error) {
		return merchantbilling.Checkout{}, errors.New("lost")
	}}}
	if _, err = s.DispatchMerchantOrder(ctx, order.ID, p); err == nil {
		t.Fatal("expected lost reply")
	}
	h, err := NewHTTP(s, HTTPOptions{Origin: "https://portal.example.test", Merchant: p, MerchantCountries: []string{"BG"}, TestMerchantWebhookSecret: billingSecret})
	if err != nil {
		t.Fatal(err)
	}
	raw := merchantEventBody(t, "evt_merchant", "acct_orders", order.ID)
	deliver := func(body []byte, path string, signed bool) int {
		r := httptest.NewRequest("POST", h.origin+path, bytes.NewReader(body))
		if signed {
			r.Header.Set("Stripe-Signature", billingSignature(body))
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if code := deliver(raw, "/webhooks/stripe-merchant-test", false); code != 400 {
		t.Fatal(code)
	}
	if code := deliver(raw, "/webhooks/stripe-merchant-test?alias=1", true); code != 404 {
		t.Fatal(code)
	}
	for range 2 {
		if code := deliver(raw, "/webhooks/stripe-merchant-test", true); code != 204 {
			t.Fatal(code)
		}
	}
	var count int
	if err = s.db.QueryRow("SELECT count(*) FROM merchant_events").Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	var state string
	if err = s.db.QueryRow("SELECT payment_status FROM merchant_orders WHERE id=?", order.ID).Scan(&state); err != nil || state != "unpaid" {
		t.Fatal("payload marked paid", state, err)
	}
	if code := deliver(merchantEventBody(t, "evt_foreign", "acct_foreign", order.ID), "/webhooks/stripe-merchant-test", true); code != 204 {
		t.Fatal(code)
	}
	if err = s.db.QueryRow("SELECT count(*) FROM merchant_events").Scan(&count); err != nil || count != 1 {
		t.Fatal("foreign mapping accepted", count, err)
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
	p.read = func(_ context.Context, account, id string, input merchantbilling.CheckoutOrder) (merchantbilling.Checkout, error) {
		reads++
		if account != "acct_orders" || id != "cs_test_event" || input.RequestID != order.ID {
			t.Fatal("wrong scope")
		}
		v := openCheckout(id)
		v.State = "complete"
		v.PaymentStatus = "paid"
		v.PaymentIntentID = "pi_event"
		v.URL = ""
		return v, nil
	}
	done, failed, err := s.ProcessMerchantEvents(ctx, p, 10)
	if err != nil || done != 1 || failed != 0 {
		t.Fatal(done, failed, err)
	}
	if err = s.db.QueryRow("SELECT payment_status FROM merchant_orders WHERE id=?", order.ID).Scan(&state); err != nil || state != "paid" {
		t.Fatal(state, err)
	}
	done, _, err = s.ProcessMerchantEvents(ctx, p, 10)
	if err != nil || done != 0 || reads != 1 {
		t.Fatal("duplicate processed", done, reads, err)
	}
}

func TestMerchantEventsRetryAndReceiptConflict(t *testing.T) {
	s, _, _, _, product := orderFixture(t)
	ctx := t.Context()
	order, err := s.RequestMerchantOrder(ctx, randomToken(), product.ID, product.Revision, randomToken())
	if err != nil {
		t.Fatal(err)
	}
	p := shopProvider{checkoutProviderFixture: checkoutProviderFixture{create: func(context.Context, string, merchantbilling.CheckoutOrder) (merchantbilling.Checkout, error) {
		return openCheckout("cs_test_event"), nil
	}}}
	if _, err = s.DispatchMerchantOrder(ctx, order.ID, p); err != nil {
		t.Fatal(err)
	}
	body := merchantEventBody(t, "evt_retry", "acct_orders", order.ID)
	event, err := merchantbilling.VerifyTestCheckoutEvent(body, billingSignature(body), billingSecret)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.AcceptMerchantEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	changed := event
	changed.SHA256 = randomToken()
	if err = s.AcceptMerchantEvent(ctx, changed); !errors.Is(err, ErrBillingConflict) {
		t.Fatal("conflicting receipt", err)
	}
	calls := 0
	p.read = func(context.Context, string, string, merchantbilling.CheckoutOrder) (merchantbilling.Checkout, error) {
		calls++
		return merchantbilling.Checkout{}, errors.New("private failure")
	}
	done, failed, err := s.ProcessMerchantEvents(ctx, p, 1)
	if err != nil || done != 0 || failed != 1 {
		t.Fatal(done, failed, err)
	}
	done, failed, err = s.ProcessMerchantEvents(ctx, p, 1)
	if err != nil || done != 0 || failed != 0 || calls != 1 {
		t.Fatal("retry delay bypassed", done, failed, calls, err)
	}
	if _, err = s.db.Exec("UPDATE merchant_events SET next_attempt=0"); err != nil {
		t.Fatal(err)
	}
	p.read = func(_ context.Context, account, id string, input merchantbilling.CheckoutOrder) (merchantbilling.Checkout, error) {
		return openCheckout(id), nil
	}
	done, failed, err = s.ProcessMerchantEvents(ctx, p, 1)
	if err != nil || done != 1 || failed != 0 {
		t.Fatal("retry failed", done, failed, err)
	}
	var state string
	if err = s.db.QueryRow("SELECT payment_status FROM merchant_orders WHERE id=?", order.ID).Scan(&state); err != nil || state != "unpaid" {
		t.Fatal("trusted event instead of provider", state, err)
	}
}
