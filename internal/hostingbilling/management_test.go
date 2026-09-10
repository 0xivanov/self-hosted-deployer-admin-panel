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
		{"settings changed during creation", func(v map[string]any) {
			config := managementConfigFixture()
			config["features"].(map[string]any)["subscription_update"] = map[string]any{"enabled": true}
			v["configuration"] = config
		}, false},
		{"live", func(v map[string]any) { v["livemode"] = true }, false},
		{"foreign customer", func(v map[string]any) { v["customer"] = "cus_foreign" }, false},
		{"foreign configuration", func(v map[string]any) { v["configuration"] = "bpc_foreign" }, false},
		{"foreign return", func(v map[string]any) { v["return_url"] = "https://foreign.test" }, false},
		{"untrusted link", func(v map[string]any) { v["url"] = "https://billing.stripe.com.evil.test/session" }, false},
		{"connected scope", func(v map[string]any) { v["on_behalf_of"] = "acct_foreign" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := map[string]any{"id": "bps_fixture", "object": "billing_portal.session", "livemode": false, "customer": "cus_fixture", "configuration": managementConfigFixture(), "return_url": "https://portal.example.test/billing/success", "url": "https://billing.stripe.com/p/session/test_fixture"}
			if tc.change != nil {
				tc.change(v)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" && r.URL.Path == "/v1/billing_portal/configurations/bpc_fixture" {
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(managementConfigFixture())
					return
				}
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

func managementConfigFixture() map[string]any {
	return map[string]any{"id": "bpc_fixture", "object": "billing_portal.configuration", "active": true, "livemode": false, "login_page": map[string]any{"enabled": false}, "features": map[string]any{"subscription_update": map[string]any{"enabled": false}, "subscription_cancel": map[string]any{"enabled": true, "mode": "at_period_end", "proration_behavior": "none"}, "payment_method_update": map[string]any{"enabled": true}, "invoice_history": map[string]any{"enabled": true}}}
}

func TestManagementRejectsUnsupportedConfigurationBeforeSession(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
	}{
		{"live", func(v map[string]any) { v["livemode"] = true }},
		{"inactive", func(v map[string]any) { v["active"] = false }},
		{"public login", func(v map[string]any) { v["login_page"] = map[string]any{"enabled": true} }},
		{"plan changes", func(v map[string]any) {
			v["features"].(map[string]any)["subscription_update"] = map[string]any{"enabled": true}
		}},
		{"immediate cancellation", func(v map[string]any) {
			v["features"].(map[string]any)["subscription_cancel"] = map[string]any{"enabled": true, "mode": "immediately", "proration_behavior": "none"}
		}},
		{"missing settings", func(v map[string]any) { delete(v, "features") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := managementConfigFixture()
			tc.change(config)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					t.Error("session created with unsupported configuration")
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(config)
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
			if link, err := management.CreateManagementSession(t.Context(), "cus_fixture"); err == nil || link != "" {
				t.Fatal("unsafe configuration returned link", link, err)
			}
		})
	}
}
