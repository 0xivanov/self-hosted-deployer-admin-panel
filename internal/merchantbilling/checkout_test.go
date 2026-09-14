//go:build integration

package merchantbilling

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func checkoutOrder() CheckoutOrder {
	return CheckoutOrder{RequestID: requestFixture, Name: "Example product", Currency: "eur", AmountMinor: 1250}
}
func checkoutFixture() map[string]any {
	return map[string]any{"object": "checkout.session", "id": "cs_test_order", "livemode": false, "mode": "payment", "client_reference_id": requestFixture, "metadata": map[string]string{"merchant_order": requestFixture}, "currency": "eur", "amount_subtotal": 1250, "amount_total": 1250, "success_url": "https://portal.example.test/merchant/sales/success", "cancel_url": "https://portal.example.test/merchant/sales/cancel", "status": "open", "payment_status": "unpaid", "url": "https://checkout.stripe.com/c/pay/cs_test_order#stripe-fragment", "payment_method_types": []string{"card"}, "total_details": map[string]int{"amount_discount": 0, "amount_shipping": 0, "amount_tax": 0}, "automatic_tax": map[string]bool{"enabled": false}, "adaptive_pricing": map[string]bool{"enabled": false}}
}

func TestMerchantDirectCheckoutScopeAndIdentity(t *testing.T) {
	t.Parallel()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Stripe-Account") != "acct_fixture" {
			t.Error("merchant scope missing")
		}
		if r.Method == "POST" {
			if r.URL.Path != "/v1/checkout/sessions" || r.Header.Get("Idempotency-Key") != "merchant-checkout-"+requestFixture {
				t.Error("request identity")
			}
			r.ParseForm()
			for field, want := range map[string]string{"mode": "payment", "client_reference_id": requestFixture, "metadata[merchant_order]": requestFixture, "payment_intent_data[metadata][merchant_order]": requestFixture, "line_items[0][quantity]": "1", "line_items[0][price_data][currency]": "eur", "line_items[0][price_data][unit_amount]": "1250", "line_items[0][price_data][product_data][name]": "Example product", "line_items[0][price_data][tax_behavior]": "inclusive", "payment_method_types[0]": "card", "allow_promotion_codes": "false", "automatic_tax[enabled]": "false", "adaptive_pricing[enabled]": "false"} {
				if r.Form.Get(field) != want {
					t.Error("unexpected checkout field", field)
				}
			}
			for _, field := range []string{"customer", "payment_intent_data[transfer_data][destination]", "payment_intent_data[on_behalf_of]", "payment_intent_data[application_fee_amount]"} {
				if r.Form.Has(field) {
					t.Error("unexpected platform billing linkage", field)
				}
			}
		} else if r.Method != "GET" || r.URL.Path != "/v1/checkout/sessions/cs_test_order" {
			t.Error("wrong retrieve path")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(checkoutFixture())
	}))
	defer server.Close()
	c, err := newTestClient("sk_test_synthetic_fixture", "https://portal.example.test/merchant/return", "https://portal.example.test/merchant/refresh", []string{"BG"}, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for range 2 {
		v, err := c.CreateCheckout(t.Context(), "acct_fixture", checkoutOrder())
		if err != nil || v.ID != "cs_test_order" || v.State != "open" {
			t.Fatal(v, err)
		}
	}
	if _, err = c.RetrieveCheckout(t.Context(), "acct_fixture", "cs_test_order", checkoutOrder()); err != nil {
		t.Fatal(err)
	}
	bad := checkoutOrder()
	bad.AmountMinor = 0
	if _, err = c.CreateCheckout(t.Context(), "acct_fixture", bad); err == nil {
		t.Fatal("zero amount")
	}
	bad = checkoutOrder()
	bad.Currency = "invalid"
	if _, err = c.CreateCheckout(t.Context(), "acct_fixture", bad); err == nil {
		t.Fatal("currency")
	}
	if _, err = c.CreateCheckout(t.Context(), "acct_fixture/foreign", checkoutOrder()); err == nil {
		t.Fatal("scope injection")
	}
	if calls != 3 {
		t.Fatal("invalid input reached provider", calls)
	}
}

func TestMerchantCheckoutRejectsMismatchedProviderEvidence(t *testing.T) {
	t.Parallel()
	cases := map[string]func(map[string]any){
		"live":     func(v map[string]any) { v["livemode"] = true },
		"order":    func(v map[string]any) { v["client_reference_id"] = "foreign" },
		"metadata": func(v map[string]any) { v["metadata"] = map[string]string{"merchant_order": "foreign"} },
		"amount":   func(v map[string]any) { v["amount_total"] = 1251 },
		"currency": func(v map[string]any) { v["currency"] = "usd" },
		"id":       func(v map[string]any) { v["id"] = "cs_test_other" },
		"redirect": func(v map[string]any) { v["url"] = "https://checkout.stripe.com.evil.test/pay" },
		"return":   func(v map[string]any) { v["success_url"] = "https://evil.test" },
		"paid_open": func(v map[string]any) {
			v["payment_status"] = "paid"
			v["payment_intent"] = map[string]string{"id": "pi_order"}
		},
		"paid_no_intent": func(v map[string]any) { v["status"] = "complete"; v["payment_status"] = "paid"; v["url"] = "" },
		"discount":       func(v map[string]any) { v["total_details"] = map[string]int{"amount_discount": 1} },
		"adaptive":       func(v map[string]any) { v["adaptive_pricing"] = map[string]bool{"enabled": true} },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				v := checkoutFixture()
				change(v)
				json.NewEncoder(w).Encode(v)
			}))
			defer server.Close()
			c, err := newTestClient("sk_test_synthetic_fixture", "https://portal.example.test/merchant/return", "https://portal.example.test/merchant/refresh", []string{"BG"}, server.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if _, err = c.RetrieveCheckout(t.Context(), "acct_fixture", "cs_test_order", checkoutOrder()); err == nil {
				t.Fatal("invalid checkout accepted")
			}
		})
	}
}

func TestMerchantCheckoutPaidAndExpiredObservations(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"complete", "expired"} {
		t.Run(state, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				v := checkoutFixture()
				v["status"] = state
				v["url"] = ""
				if state == "complete" {
					v["payment_status"] = "paid"
					v["payment_intent"] = map[string]string{"id": "pi_order"}
				}
				json.NewEncoder(w).Encode(v)
			}))
			defer server.Close()
			c, err := newTestClient("sk_test_synthetic_fixture", "https://portal.example.test/merchant/return", "https://portal.example.test/merchant/refresh", []string{"BG"}, server.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			v, err := c.RetrieveCheckout(t.Context(), "acct_fixture", "cs_test_order", checkoutOrder())
			if err != nil || v.State != state || v.URL != "" {
				t.Fatal(v, err)
			}
			if state == "complete" && v.PaymentIntentID != "pi_order" {
				t.Fatal(v)
			}
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte(`{"error":{"message":"private-provider-detail","type":"api_error"}}`))
	}))
	defer server.Close()
	c, err := newTestClient("sk_test_synthetic_fixture", "https://portal.example.test/merchant/return", "https://portal.example.test/merchant/refresh", []string{"BG"}, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err = c.CreateCheckout(t.Context(), "acct_fixture", checkoutOrder()); err == nil || strings.Contains(err.Error(), "private-provider-detail") {
		t.Fatal(err)
	}
}
