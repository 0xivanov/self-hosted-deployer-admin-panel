//go:build integration

package hostingbilling

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDiscoverInvoiceCharge(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
		valid  bool
	}{
		{"matching", nil, true}, {"no payment", nil, true}, {"multiple payments", nil, false}, {"truncated", nil, false},
		{"foreign customer", func(v map[string]any) { v["invoice"].(map[string]any)["customer"] = "cus_other" }, false},
		{"foreign subscription", func(v map[string]any) {
			v["invoice"].(map[string]any)["parent"].(map[string]any)["subscription_details"].(map[string]any)["subscription"] = "sub_other"
		}, false},
		{"unexpanded intent", func(v map[string]any) { v["payment"].(map[string]any)["payment_intent"] = "pi_fixture" }, false},
		{"live", func(v map[string]any) { v["livemode"] = true }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inv := map[string]any{"id": "in_fixture", "object": "invoice", "livemode": false, "customer": "cus_fixture", "currency": "eur", "parent": map[string]any{"type": "subscription_details", "subscription_details": map[string]any{"subscription": "sub_fixture"}}}
			intent := map[string]any{"id": "pi_fixture", "object": "payment_intent", "livemode": false, "customer": "cus_fixture", "status": "succeeded", "amount_received": 1000, "currency": "eur", "latest_charge": "ch_discovered"}
			payment := map[string]any{"object": "invoice_payment", "livemode": false, "status": "paid", "invoice": inv, "payment": map[string]any{"type": "payment_intent", "payment_intent": intent}, "amount_paid": 1000, "currency": "eur"}
			if tc.change != nil {
				tc.change(payment)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query()
				if r.Method != "GET" || r.URL.Path != "/v1/invoice_payments" || q.Get("invoice") != "in_fixture" || q.Get("status") != "paid" || q.Get("limit") != "2" || q.Get("expand[0]") != "data.invoice" || q.Get("expand[1]") != "data.payment.payment_intent" {
					t.Error("incorrect lookup", r.URL.String())
				}
				data := []any{payment}
				if tc.name == "no payment" {
					data = nil
				}
				if tc.name == "multiple payments" {
					data = append(data, payment)
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data, "has_more": tc.name == "truncated"})
			}))
			defer server.Close()
			c, err := newTestClient("sk_test_synthetic_fixture", "https://portal.example.test/success", "https://portal.example.test/cancel", map[string]string{"starter": "price_fixture"}, server.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			id, err := c.DiscoverInvoiceCharge(t.Context(), "in_fixture", "cus_fixture", "sub_fixture")
			if tc.valid {
				if err != nil || (tc.name == "matching" && id != "ch_discovered") || (tc.name == "no payment" && id != "") {
					t.Fatal(id, err)
				}
			} else if err == nil {
				t.Fatal("unsafe discovery accepted", id)
			}
		})
	}
}
