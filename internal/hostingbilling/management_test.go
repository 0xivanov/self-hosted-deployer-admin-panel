//go:build integration

package hostingbilling

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestManagementSessionIdentity(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
		valid  bool
	}{
		{"matching", nil, true},
		{"live", func(v map[string]any) { v["livemode"] = true }, false},
		{"foreign customer", func(v map[string]any) { v["customer"] = "cus_foreign" }, false},
		{"foreign configuration", func(v map[string]any) { v["configuration"] = "bpc_foreign" }, false},
		{"foreign return", func(v map[string]any) { v["return_url"] = "https://foreign.test" }, false},
		{"untrusted link", func(v map[string]any) { v["url"] = "https://billing.stripe.com.evil.test/session" }, false},
		{"connected scope", func(v map[string]any) { v["on_behalf_of"] = "acct_foreign" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := map[string]any{"id": "bps_fixture", "object": "billing_portal.session", "livemode": false, "customer": "cus_fixture", "configuration": "bpc_fixture", "return_url": "https://portal.example.test/billing/success", "url": "https://billing.stripe.com/p/session/test_fixture"}
			if tc.change != nil {
				tc.change(v)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				r.ParseForm()
				if r.Method != "POST" || r.URL.Path != "/v1/billing_portal/sessions" || r.Form.Get("customer") != "cus_fixture" || r.Form.Get("configuration") != "bpc_fixture" || r.Form.Get("return_url") != "https://portal.example.test/billing/success" || r.Header.Get("Stripe-Account") != "" {
					t.Error("wrong management request")
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(v)
			}))
			defer server.Close()
			client, err := newTestClient("sk_test_synthetic_fixture", "https://portal.example.test/billing/success", "https://portal.example.test/billing/cancel", map[string]string{"starter": "price_fixture"}, server.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			management, err := NewManagement(client, "bpc_fixture")
			if err != nil {
				t.Fatal(err)
			}
			link, err := management.CreateManagementSession(t.Context(), "cus_fixture")
			if tc.valid && (err != nil || link == "") {
				t.Fatal(link, err)
			}
			if !tc.valid && err == nil {
				t.Fatal("unsafe session returned")
			}
		})
	}
}
