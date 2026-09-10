//go:build integration

package hostingbilling

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	stripe "github.com/stripe/stripe-go/v86"
)

func TestConfiguredSubscriptionCheckout(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Method != "POST" || r.URL.Path != "/v1/checkout/sessions" || r.Form.Get("mode") != "subscription" || r.Form.Get("customer") != "cus_fixture" || r.Form.Get("line_items[0][price]") != "price_fixture" || r.Form.Get("line_items[0][quantity]") != "1" || r.Form.Get("client_reference_id") != "durable-request-key" || r.Header.Get("Idempotency-Key") != "durable-request-key" || r.Header.Get("Stripe-Version") != stripe.APIVersion || r.Header.Get("Stripe-Account") != "" {
			t.Error("unexpected hosting checkout request", r.Form)
		}
		if r.Form.Get("success_url") != "https://portal.example.test/billing/success" || r.Form.Get("cancel_url") != "https://portal.example.test/billing/cancel" {
			t.Error("return URLs changed")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"cs_test_fixture","object":"checkout.session","livemode":false,"url":"https://checkout.stripe.com/c/pay/cs_test_fixture"}`))
	}))
	defer server.Close()
	plans := map[string]string{"starter": "price_fixture"}
	c, err := newTestClient("sk_test_synthetic_fixture", "https://portal.example.test/billing/success", "https://portal.example.test/billing/cancel", plans, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	plans["starter"] = "price_changed"
	for range 2 {
		session, err := c.CreateCheckout(t.Context(), "cus_fixture", "starter", "durable-request-key")
		if err != nil || session.ID != "cs_test_fixture" {
			t.Fatal(session, err)
		}
	}
	if _, err = c.CreateCheckout(t.Context(), "cus_fixture", "client-selected-price", "durable-request-key"); err == nil {
		t.Fatal("unconfigured price accepted")
	}
	if calls.Load() != 2 {
		t.Fatal("invalid input reached Stripe")
	}
	if _, err = NewTestClient("sk_live_never_allowed", "https://portal.example.test/success", "https://portal.example.test/cancel", plans); err == nil {
		t.Fatal("live key accepted")
	}
}
func TestProviderErrorsAreSanitized(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte(`{"error":{"message":"private-provider-detail","type":"api_error"}}`))
	}))
	defer server.Close()
	c, err := newTestClient("sk_test_synthetic_fixture", "https://portal.example.test/success", "https://portal.example.test/cancel", map[string]string{"starter": "price_fixture"}, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err = c.CreateCheckout(t.Context(), "cus_fixture", "starter", "durable-request-key"); err == nil || strings.Contains(err.Error(), "private-provider-detail") {
		t.Fatal(err)
	}
}

func TestCreateCustomerUsesPersistentIdentity(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.URL.Path != "/v1/customers" || r.Form.Get("email") != "owner@example.test" || r.Form.Get("metadata[hosting_request]") != "persisted-request-id" || r.Header.Get("Idempotency-Key") != "persisted-request-id" {
			t.Error("wrong customer create request")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"cus_fixture","object":"customer","livemode":false}`))
	}))
	defer server.Close()
	c, err := newTestClient("sk_test_synthetic_fixture", "https://portal.example.test/success", "https://portal.example.test/cancel", map[string]string{"starter": "price_fixture"}, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	id, err := c.CreateCustomer(t.Context(), "owner@example.test", "persisted-request-id")
	if err != nil || id != "cus_fixture" {
		t.Fatal(id, err)
	}
}
