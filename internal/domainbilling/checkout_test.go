package domainbilling

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func domainOrder() Order {
	return Order{ID: "order_123", Domain: "example.com", Currency: "usd", AmountMinor: 1250}
}

func domainCheckoutFixture() map[string]any {
	return map[string]any{
		"object":               "checkout.session",
		"id":                   "cs_test_order",
		"livemode":             false,
		"mode":                 "payment",
		"client_reference_id":  "order_123",
		"metadata":             map[string]string{"domain_order": "order_123", "domain": "example.com"},
		"currency":             "usd",
		"amount_subtotal":      1250,
		"amount_total":         1250,
		"status":               "open",
		"payment_status":       "unpaid",
		"url":                  "https://checkout.stripe.com/c/pay/cs_test_order",
		"payment_method_types": []string{"card"},
		"total_details":        map[string]int{"amount_discount": 0, "amount_shipping": 0, "amount_tax": 0},
		"automatic_tax":        map[string]bool{"enabled": false},
		"adaptive_pricing":     map[string]bool{"enabled": false},
	}
}

func TestCheckoutCreateAndReadBindsOrder(t *testing.T) {
	t.Parallel()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.URL.Path == "/v1/checkout/sessions" && r.Method == http.MethodPost {
			for field, want := range map[string]string{
				"mode":                                        "payment",
				"client_reference_id":                         "order_123",
				"metadata[domain_order]":                      "order_123",
				"metadata[domain]":                            "example.com",
				"line_items[0][quantity]":                     "1",
				"line_items[0][price_data][currency]":         "usd",
				"line_items[0][price_data][unit_amount]":      "1250",
				"payment_intent_data[metadata][domain_order]": "order_123",
			} {
				if r.Form.Get(field) != want {
					t.Errorf("%s = %q, want %q", field, r.Form.Get(field), want)
				}
			}
			if r.Header.Get("Idempotency-Key") != "domain-checkout-order_123" {
				t.Errorf("unexpected idempotency key %q", r.Header.Get("Idempotency-Key"))
			}
		} else if r.URL.Path != "/v1/checkout/sessions/cs_test_order" || r.Method != http.MethodGet {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(domainCheckoutFixture())
	}))
	defer server.Close()

	c, err := newClient("sk_test_synthetic_fixture", "https://portal.example.test/success", "https://portal.example.test/cancel", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	created, err := c.CreateCheckout(t.Context(), domainOrder())
	if err != nil || created.ID != "cs_test_order" || created.Paid {
		t.Fatal(created, err)
	}
	read, err := c.ReadCheckout(t.Context(), "cs_test_order", domainOrder())
	if err != nil || read != created {
		t.Fatal(read, err)
	}
	if calls != 2 {
		t.Fatalf("got %d provider calls, want 2", calls)
	}
}

func TestReadCheckoutRequiresPaidComplete(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture := domainCheckoutFixture()
		fixture["status"] = "complete"
		fixture["payment_status"] = "paid"
		fixture["url"] = ""
		_ = json.NewEncoder(w).Encode(fixture)
	}))
	defer server.Close()
	c, err := newClient("sk_test_synthetic_fixture", "https://portal.example.test/success", "https://portal.example.test/cancel", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	got, err := c.ReadCheckout(t.Context(), "cs_test_order", domainOrder())
	if err != nil || !got.Paid {
		t.Fatal(got, err)
	}
}

func TestReadCheckoutRejectsForeignEvidence(t *testing.T) {
	t.Parallel()
	for name, change := range map[string]func(map[string]any){
		"live":      func(v map[string]any) { v["livemode"] = true },
		"mode":      func(v map[string]any) { v["mode"] = "subscription" },
		"reference": func(v map[string]any) { v["client_reference_id"] = "other" },
		"metadata": func(v map[string]any) {
			v["metadata"] = map[string]string{"domain_order": "other", "domain": "example.com"}
		},
		"amount":   func(v map[string]any) { v["amount_total"] = 1251 },
		"currency": func(v map[string]any) { v["currency"] = "eur" },
		"redirect": func(v map[string]any) { v["url"] = "https://checkout.stripe.com.evil.test/pay" },
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fixture := domainCheckoutFixture()
				change(fixture)
				_ = json.NewEncoder(w).Encode(fixture)
			}))
			defer server.Close()
			c, err := newClient("sk_test_synthetic_fixture", "https://portal.example.test/success", "https://portal.example.test/cancel", server.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if _, err = c.ReadCheckout(t.Context(), "cs_test_order", domainOrder()); err == nil {
				t.Fatal("foreign checkout evidence accepted")
			}
		})
	}
}

func TestNewTestClientRejectsLiveKeyAndInvalidURLs(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"sk_live_never_allowed", "sk_test_short"} {
		if _, err := NewTestClient(key, "https://portal.example.test/success", "https://portal.example.test/cancel"); err == nil {
			t.Fatalf("accepted key %q", key)
		}
	}
	if _, err := NewTestClient("sk_test_synthetic_fixture", "http://portal.example.test/success", "https://portal.example.test/cancel"); err == nil {
		t.Fatal("accepted non-HTTPS return URL")
	}
	if _, err := NewTestClient("sk_test_synthetic_fixture", "https://one.example.test/success", "https://two.example.test/cancel"); err == nil {
		t.Fatal("accepted return URLs on different origins")
	}
}

func TestProviderErrorsAreSanitized(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"private-provider-detail"}}`))
	}))
	defer server.Close()
	c, err := newClient("sk_test_synthetic_fixture", "https://portal.example.test/success", "https://portal.example.test/cancel", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err = c.CreateCheckout(t.Context(), domainOrder()); err == nil || strings.Contains(err.Error(), "private-provider-detail") {
		t.Fatal(err)
	}
}

func TestPortalDomainReturnURL(t *testing.T) {
	c, err := NewTestClient("sk_test_fixture_key", "https://portal.example/?domain_checkout=return", "https://portal.example/?domain_checkout=return")
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
}
