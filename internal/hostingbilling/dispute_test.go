//go:build integration

package hostingbilling

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	stripe "github.com/stripe/stripe-go/v86"
)

func TestCurrentDisputeOutcomesAndIdentity(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
		valid  bool
	}{
		{"needs response", func(v map[string]any) { v["status"] = "needs_response" }, true},
		{"under review", func(v map[string]any) { v["status"] = "under_review" }, true},
		{"won", nil, true},
		{"lost", func(v map[string]any) { v["status"] = "lost" }, true},
		{"prevented", func(v map[string]any) { v["status"] = "prevented" }, true},
		{"warning", func(v map[string]any) { v["status"] = "warning_needs_response" }, true},
		{"foreign charge", func(v map[string]any) { v["charge"] = "ch_foreign" }, false},
		{"foreign payment", func(v map[string]any) { v["payment_intent"] = "pi_foreign" }, false},
		{"foreign currency", func(v map[string]any) { v["currency"] = "usd" }, false},
		{"live", func(v map[string]any) { v["livemode"] = true }, false},
		{"unknown status", func(v map[string]any) { v["status"] = "new_status" }, false},
		{"truncated", nil, false}, {"missing", nil, false}, {"duplicate", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := map[string]any{"id": "dp_fixture", "object": "dispute", "livemode": false, "charge": "ch_fixture", "payment_intent": "pi_fixture", "currency": "eur", "amount": 1000, "status": "won"}
			if tc.change != nil {
				tc.change(d)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/v1/disputes" || r.URL.Query().Get("charge") != "ch_fixture" || r.Header.Get("Stripe-Account") != "" {
					t.Error("incorrect dispute request")
				}
				data := []any{d}
				if tc.name == "missing" {
					data = nil
				}
				if tc.name == "duplicate" {
					data = append(data, d)
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data, "has_more": tc.name == "truncated"})
			}))
			defer server.Close()
			client, err := newTestClient("sk_test_synthetic_fixture", "https://portal.example.test/success", "https://portal.example.test/cancel", map[string]string{"starter": "price_fixture"}, server.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			disputes, err := client.retrieveChargeDisputes(t.Context(), &stripe.Charge{ID: "ch_fixture", PaymentIntent: &stripe.PaymentIntent{ID: "pi_fixture"}, Currency: "eur", Disputed: true})
			if tc.valid {
				if err != nil || len(disputes) != 1 || disputes[0].Status != d["status"] {
					t.Fatal(disputes, err)
				}
			} else if err == nil {
				t.Fatal("unsafe dispute state accepted", disputes)
			}
		})
	}
}
