//go:build integration

package merchantbilling

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const requestFixture = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func accountFixture() map[string]any {
	return map[string]any{"id": "acct_fixture", "object": "account", "country": "BG", "metadata": map[string]string{"merchant_request": requestFixture}, "details_submitted": false, "charges_enabled": false, "payouts_enabled": false, "capabilities": map[string]string{"card_payments": "pending"}, "controller": map[string]any{"type": "application", "is_controller": true, "fees": map[string]string{"payer": "account"}, "losses": map[string]string{"payments": "stripe"}, "requirement_collection": "stripe", "stripe_dashboard": map[string]string{"type": "full"}}}
}

func TestMerchantAccountAndOnboardingContract(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Stripe-Account") != "" {
			t.Error("connected scope on platform request")
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/accounts":
			if r.Method != "POST" || r.Header.Get("Idempotency-Key") != "merchant-account-"+requestFixture {
				t.Error("missing durable account identity")
			}
			expected := map[string]string{"country": "BG", "metadata[merchant_request]": requestFixture, "controller[fees][payer]": "account", "controller[losses][payments]": "stripe", "controller[requirement_collection]": "stripe", "controller[stripe_dashboard][type]": "full", "capabilities[card_payments][requested]": "true"}
			for field, value := range expected {
				if r.Form.Get(field) != value {
					t.Error("incorrect account field", field)
				}
			}
			if r.Form.Has("type") || r.Form.Has("tos_acceptance[date]") || r.Form.Has("email") {
				t.Error("unexpected account type or personal input")
			}
			json.NewEncoder(w).Encode(accountFixture())
		case "/v1/accounts/acct_fixture":
			if r.Method != "GET" {
				t.Error("wrong account retrieval method")
			}
			json.NewEncoder(w).Encode(accountFixture())
		case "/v1/account_links":
			if r.Method != "POST" || r.Form.Get("account") != "acct_fixture" || r.Form.Get("type") != "account_onboarding" || r.Form.Get("return_url") != "https://portal.example.test/merchant/return" || r.Form.Get("refresh_url") != "https://portal.example.test/merchant/refresh" || r.Header.Get("Idempotency-Key") != "merchant-link-"+requestFixture {
				t.Error("unsafe onboarding link parameters")
			}
			json.NewEncoder(w).Encode(map[string]any{"object": "account_link", "created": time.Now().Unix(), "expires_at": time.Now().Unix() + 300, "url": "https://connect.stripe.com/setup/c/acct_fixture/token"})
		default:
			t.Error("unexpected route", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	c, err := newTestClient("sk_test_synthetic_fixture", "https://portal.example.test/merchant/return", "https://portal.example.test/merchant/refresh", []string{"BG"}, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for range 2 {
		a, err := c.CreateAccount(t.Context(), "BG", requestFixture)
		if err != nil || a.ID != "acct_fixture" || a.Country != "BG" || a.RequestID != requestFixture || a.ChargesEnabled || a.CardPayments != "pending" {
			t.Fatal(a, err)
		}
	}
	if _, err = c.RetrieveAccount(t.Context(), "acct_fixture", "BG", requestFixture); err != nil {
		t.Fatal(err)
	}
	link, err := c.CreateOnboardingLink(t.Context(), "acct_fixture", requestFixture)
	if err != nil || link.URL == "" || link.ExpiresAt <= time.Now().Unix() {
		t.Fatal(link, err)
	}
	if _, err = c.CreateAccount(t.Context(), "US", requestFixture); err == nil {
		t.Fatal("unconfigured country")
	}
	if _, err = c.CreateAccount(t.Context(), "BG", "client-request"); err == nil {
		t.Fatal("unbounded request identity")
	}
	if _, err = c.RetrieveAccount(t.Context(), "acct_fixture/foreign", "BG", requestFixture); err == nil {
		t.Fatal("account path injection")
	}
	if calls.Load() != 4 {
		t.Fatal("invalid input reached provider", calls.Load())
	}
}

func TestMerchantAccountRejectsWrongMappingAndControl(t *testing.T) {
	t.Parallel()
	cases := map[string]func(map[string]any){
		"controller": func(a map[string]any) { a["controller"].(map[string]any)["is_controller"] = false },
		"capability": func(a map[string]any) { a["capabilities"] = map[string]string{"card_payments": "unexpected"} },
		"country":    func(a map[string]any) { a["country"] = "US" },
		"request":    func(a map[string]any) { a["metadata"] = map[string]string{"merchant_request": "wrong"} },
		"object":     func(a map[string]any) { a["object"] = "customer" },
		"id":         func(a map[string]any) { a["id"] = "acct_other" },
		"liability": func(a map[string]any) {
			a["controller"].(map[string]any)["losses"] = map[string]string{"payments": "application"}
		},
		"dashboard": func(a map[string]any) {
			a["controller"].(map[string]any)["stripe_dashboard"] = map[string]string{"type": "express"}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				a := accountFixture()
				change(a)
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(a)
			}))
			defer server.Close()
			c, err := newTestClient("sk_test_synthetic_fixture", "https://portal.example.test/return", "https://portal.example.test/refresh", []string{"BG"}, server.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if _, err = c.RetrieveAccount(t.Context(), "acct_fixture", "BG", requestFixture); err == nil {
				t.Fatal("accepted altered account")
			}
		})
	}
}

func TestMerchantLinkAndErrorsFailClosed(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"foreign", "expired", "error", "redirect"} {
		t.Run(kind, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if kind == "error" {
					w.WriteHeader(500)
					w.Write([]byte(`{"error":{"message":"private-provider-detail","type":"api_error"}}`))
					return
				}
				if kind == "redirect" {
					w.Header().Set("Location", "https://foreign.example.test")
					w.WriteHeader(302)
					return
				}
				link := "https://connect.stripe.com/setup/test"
				expiry := time.Now().Unix() + 300
				if kind == "foreign" {
					link = "https://connect.stripe.com.evil.test/setup"
				}
				if kind == "expired" {
					expiry = time.Now().Unix() - 1
				}
				json.NewEncoder(w).Encode(map[string]any{"object": "account_link", "url": link, "expires_at": expiry})
			}))
			defer server.Close()
			c, err := newTestClient("sk_test_synthetic_fixture", "https://portal.example.test/return", "https://portal.example.test/refresh", []string{"BG"}, server.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if _, err = c.CreateOnboardingLink(t.Context(), "acct_fixture", requestFixture); err == nil || strings.Contains(err.Error(), "private-provider-detail") {
				t.Fatal("unsafe link/error", err)
			}
		})
	}
	if _, err := NewTestClient("sk_live_never_allowed", "https://portal.example.test/return", "https://portal.example.test/refresh", []string{"BG"}); err == nil {
		t.Fatal("live key accepted")
	}
	for _, bad := range []string{"http://portal.example.test/refresh", "https://foreign.example.test/refresh", "https://portal.example.test/refresh?next=evil"} {
		if _, err := NewTestClient("sk_test_synthetic_fixture", "https://portal.example.test/return", bad, []string{"BG"}); err == nil {
			t.Fatal("unsafe fixed URL", bad)
		}
	}
}
