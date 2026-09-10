//go:build integration

package hostingbilling

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func subscriptionFixture() map[string]any {
	return map[string]any{"id": "sub_fixture", "object": "subscription", "livemode": false, "customer": "cus_fixture", "status": "active", "cancel_at_period_end": true, "items": map[string]any{"object": "list", "has_more": false, "data": []any{map[string]any{"id": "si_fixture", "quantity": 1, "current_period_start": 1700000000, "current_period_end": 1702592000, "price": map[string]any{"id": "price_fixture", "livemode": false}}}}, "latest_invoice": map[string]any{"id": "in_fixture", "livemode": false, "customer": "cus_fixture", "status": "paid", "amount_remaining": 0, "parent": map[string]any{"type": "subscription_details", "subscription_details": map[string]any{"subscription": "sub_fixture"}}}}
}
func TestRetrieveSubscriptionIdentityChecks(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
		valid  bool
	}{
		{"matching", nil, true}, {"live", func(v map[string]any) { v["livemode"] = true }, false}, {"foreign customer", func(v map[string]any) { v["customer"] = "cus_foreign" }, false},
		{"foreign price", func(v map[string]any) {
			v["items"].(map[string]any)["data"].([]any)[0].(map[string]any)["price"] = map[string]any{"id": "price_foreign"}
		}, false},
		{"multiple quantities", func(v map[string]any) {
			v["items"].(map[string]any)["data"].([]any)[0].(map[string]any)["quantity"] = 2
		}, false},
		{"truncated items", func(v map[string]any) { v["items"].(map[string]any)["has_more"] = true }, false},
		{"foreign invoice customer", func(v map[string]any) { v["latest_invoice"].(map[string]any)["customer"] = "cus_foreign" }, false},
		{"foreign invoice subscription", func(v map[string]any) {
			v["latest_invoice"].(map[string]any)["parent"].(map[string]any)["subscription_details"].(map[string]any)["subscription"] = "sub_foreign"
		}, false},
		{"unexpanded invoice", func(v map[string]any) { v["latest_invoice"] = "in_fixture" }, false},
		{"trial without invoice", func(v map[string]any) { v["status"] = "trialing"; v["latest_invoice"] = nil }, true},
		{"past due", func(v map[string]any) {
			v["status"] = "past_due"
			v["latest_invoice"].(map[string]any)["status"] = "open"
		}, true},
		{"unknown status", func(v map[string]any) { v["status"] = "unknown" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := subscriptionFixture()
			if tc.change != nil {
				tc.change(fixture)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/v1/subscriptions/sub_fixture" || r.URL.Query().Get("expand[0]") != "latest_invoice" || r.Header.Get("Stripe-Account") != "" {
					t.Error("unexpected subscription lookup", r.URL.String())
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(fixture)
			}))
			defer server.Close()
			c, err := newTestClient("sk_test_synthetic_fixture", "https://portal.example.test/success", "https://portal.example.test/cancel", map[string]string{"starter": "price_fixture"}, server.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			snapshot, err := c.RetrieveSubscription(t.Context(), "sub_fixture", "cus_fixture", "price_fixture")
			if tc.valid {
				if err != nil || snapshot.ID != "sub_fixture" || snapshot.ObservedAt <= 0 || !snapshot.CancelAtPeriodEnd {
					t.Fatal(snapshot, err)
				}
			} else if err == nil {
				t.Fatal("unsafe provider state accepted", snapshot)
			}
		})
	}
}
