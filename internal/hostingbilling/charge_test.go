//go:build integration

package hostingbilling

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestChargeObservationInvoiceMapping(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		change func(map[string]any, map[string]any)
		valid  bool
	}{
		{"paid", nil, true},
		{"partial refund", func(c, p map[string]any) { c["amount_refunded"] = 400 }, true},
		{"disputed", func(c, p map[string]any) { c["disputed"] = true }, true},
		{"live charge", func(c, p map[string]any) { c["livemode"] = true }, false},
		{"uncaptured", func(c, p map[string]any) { c["captured"] = false }, false},
		{"excess refund", func(c, p map[string]any) { c["amount_refunded"] = 2000 }, false},
		{"split allocation", func(c, p map[string]any) { p["amount_paid"] = 500 }, false},
		{"foreign payment", func(c, p map[string]any) {
			p["payment"] = map[string]any{"type": "payment_intent", "payment_intent": "pi_foreign"}
		}, false},
		{"foreign invoice customer", func(c, p map[string]any) { p["invoice"].(map[string]any)["customer"] = "cus_foreign" }, false},
		{"unexpanded invoice", func(c, p map[string]any) { p["invoice"] = "in_fixture" }, false},
		{"standalone invoice", func(c, p map[string]any) { p["invoice"].(map[string]any)["parent"] = nil }, false},
		{"multiple mappings", nil, false},
		{"truncated mappings", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			charge := map[string]any{"id": "ch_fixture", "object": "charge", "livemode": false, "customer": "cus_fixture", "payment_intent": "pi_fixture", "paid": true, "captured": true, "status": "succeeded", "amount": 1000, "amount_captured": 1000, "amount_refunded": 0, "currency": "eur", "disputed": false}
			invoice := map[string]any{"id": "in_fixture", "object": "invoice", "livemode": false, "customer": "cus_fixture", "currency": "eur", "parent": map[string]any{"type": "subscription_details", "subscription_details": map[string]any{"subscription": "sub_fixture"}}}
			payment := map[string]any{"id": "inpay_fixture", "object": "invoice_payment", "livemode": false, "status": "paid", "amount_paid": 1000, "currency": "eur", "invoice": invoice, "payment": map[string]any{"type": "payment_intent", "payment_intent": "pi_fixture"}}
			if tc.change != nil {
				tc.change(charge, payment)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method != "GET" || r.Header.Get("Stripe-Account") != "" {
					t.Error("unexpected charge request")
				}
				switch r.URL.Path {
				case "/v1/charges/ch_fixture":
					json.NewEncoder(w).Encode(charge)
				case "/v1/disputes":
					if r.URL.Query().Get("charge") != "ch_fixture" || r.URL.Query().Get("limit") != "100" {
						t.Error("wrong dispute lookup")
					}
					data := []any{}
					if tc.name == "disputed" {
						data = append(data, map[string]any{"id": "dp_fixture", "object": "dispute", "livemode": false, "charge": "ch_fixture", "payment_intent": "pi_fixture", "currency": "eur", "amount": 1000, "status": "won"})
					}
					json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data, "has_more": false})
				case "/v1/invoice_payments":
					q := r.URL.Query()
					if q.Get("payment[type]") != "payment_intent" || q.Get("payment[payment_intent]") != "pi_fixture" || q.Get("limit") != "2" || q.Get("expand[0]") != "data.invoice" {
						t.Error("wrong payment lookup", r.URL.String())
					}
					data := []any{payment}
					if tc.name == "multiple mappings" {
						data = append(data, payment)
					}
					json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data, "has_more": tc.name == "truncated mappings"})
				default:
					t.Error("unexpected endpoint", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			client, err := newTestClient("sk_test_synthetic_fixture", "https://portal.example.test/success", "https://portal.example.test/cancel", map[string]string{"starter": "price_fixture"}, server.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			observation, err := client.RetrieveChargeObservation(t.Context(), "ch_fixture")
			if tc.valid {
				if err != nil || observation.SubscriptionID != "sub_fixture" || observation.CustomerID != "cus_fixture" || observation.ObservedAt <= 0 {
					t.Fatal(observation, err)
				}
				if tc.name == "partial refund" && observation.AmountRefunded != 400 {
					t.Fatal(observation)
				}
				if tc.name == "disputed" && (!observation.Disputed || !observation.DisputesChecked || len(observation.Disputes) != 1 || observation.Disputes[0].Status != "won") {
					t.Fatal(observation)
				}
			} else if err == nil {
				t.Fatal("unsafe charge mapped", observation)
			}
		})
	}
}
