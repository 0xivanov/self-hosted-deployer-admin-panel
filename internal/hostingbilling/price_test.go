//go:build integration

package hostingbilling

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRetrievePlanPrice(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
		valid  bool
	}{
		{"fixed monthly", nil, true},
		{"expanded default currency", func(p map[string]any) {
			p["currency_options"] = map[string]any{"eur": map[string]any{"unit_amount": 1000, "unit_amount_decimal": "1000", "tax_behavior": "exclusive"}}
		}, true},
		{"live", func(p map[string]any) { p["livemode"] = true }, false},
		{"inactive", func(p map[string]any) { p["active"] = false }, false},
		{"foreign price", func(p map[string]any) { p["id"] = "price_other" }, false},
		{"one time", func(p map[string]any) { p["type"] = "one_time" }, false},
		{"metered", func(p map[string]any) { p["recurring"].(map[string]any)["usage_type"] = "metered" }, false},
		{"fractional amount", func(p map[string]any) { p["unit_amount_decimal"] = "1000.5" }, false},
		{"tiered", func(p map[string]any) { p["billing_scheme"] = "tiered" }, false},
		{"custom amount", func(p map[string]any) { p["custom_unit_amount"] = map[string]any{"minimum": 100} }, false},
		{"transformed quantity", func(p map[string]any) { p["transform_quantity"] = map[string]any{"divide_by": 10} }, false},
		{"multiple currencies", func(p map[string]any) {
			p["currency_options"] = map[string]any{"eur": map[string]any{"unit_amount": 900}}
		}, false},
		{"zero decimal currency", func(p map[string]any) { p["currency"] = "jpy" }, true},
		{"free plan", func(p map[string]any) { p["unit_amount"] = 0; p["unit_amount_decimal"] = "0" }, true},
		{"invalid interval", func(p map[string]any) { p["recurring"].(map[string]any)["interval_count"] = 0 }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := map[string]any{"id": "price_fixture", "object": "price", "active": true, "livemode": false, "type": "recurring", "billing_scheme": "per_unit", "currency": "eur", "unit_amount": 1000, "unit_amount_decimal": "1000", "tax_behavior": "exclusive", "recurring": map[string]any{"interval": "month", "interval_count": 1, "usage_type": "licensed"}}
			if tc.change != nil {
				tc.change(fixture)
			}
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "GET" || r.URL.Path != "/v1/prices/price_fixture" || r.URL.Query().Get("expand[0]") != "currency_options" || r.Header.Get("Stripe-Account") != "" {
					t.Error("unexpected price request", r.URL.String())
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(fixture)
			}))
			defer server.Close()
			client, err := newTestClient("sk_test_synthetic_fixture", "https://portal.example.test/success", "https://portal.example.test/cancel", map[string]string{"starter": "price_fixture"}, server.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			price, err := client.RetrievePlanPrice(t.Context(), "starter", "price_fixture")
			if tc.valid {
				if err != nil || price.PriceID != "price_fixture" || price.Plan != "starter" || price.ObservedAt <= 0 {
					t.Fatal(price, err)
				}
			} else if err == nil {
				t.Fatal("unsafe price accepted", price)
			}
			if _, err = client.RetrievePlanPrice(t.Context(), "starter", "price_changed"); err == nil {
				t.Fatal("configuration drift accepted")
			}
			if calls != 1 {
				t.Fatal("configuration mismatch contacted provider", calls)
			}
		})
	}
}
