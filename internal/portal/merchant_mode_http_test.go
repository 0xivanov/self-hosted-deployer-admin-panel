//go:build integration

package portal

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type liveMerchantHTTPProvider struct{ shopProvider }

func (liveMerchantHTTPProvider) BillingMode() string { return "live" }

func TestMerchantModeConfigAndWebhookRoutes(t *testing.T) {
	s, _ := newStore(t)
	defer s.Close()
	h, err := NewHTTP(s, HTTPOptions{Origin: "https://portal.example.test", MerchantMode: "test"})
	if err != nil {
		t.Fatal(err)
	}
	config := httptest.NewRecorder()
	configRequest := httptest.NewRequest(http.MethodGet, h.origin+"/api/config", nil)
	configRequest.Host = h.host
	configRequest.TLS = &tlsState
	h.ServeHTTP(config, configRequest)
	var got map[string]any
	if err = json.Unmarshal(config.Body.Bytes(), &got); err != nil || got["merchant_mode"] != "" {
		t.Fatalf("disabled merchant mode leaked: %v %v", got, err)
	}

	s.merchantMode = "live"
	liveProvider := liveMerchantHTTPProvider{}
	live, err := NewHTTP(s, HTTPOptions{Origin: "https://portal.example.test", MerchantMode: "live", Merchant: liveProvider, MerchantCountries: []string{"BG"}, MerchantWebhookSecret: "whsec_live_fixture"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path string
		code int
	}{
		{path: "/webhooks/stripe-merchant-test", code: http.StatusNotFound},
		{path: "/webhooks/stripe-live", code: http.StatusNotFound},
		{path: "/webhooks/stripe-merchant-%6cive", code: http.StatusNotFound},
		{path: "/webhooks/stripe-merchant-live?", code: http.StatusNotFound},
	} {
		r := httptest.NewRequest(http.MethodPost, live.origin+tc.path, nil)
		r.Host = live.host
		r.TLS = &tlsState
		w := httptest.NewRecorder()
		live.ServeHTTP(w, r)
		if w.Code != tc.code {
			t.Fatalf("route %s status=%d want=%d", tc.path, w.Code, tc.code)
		}
	}
	r := httptest.NewRequest(http.MethodPost, live.origin+"/webhooks/stripe-merchant-live", nil)
	r.Host = live.host
	r.TLS = &tlsState
	w := httptest.NewRecorder()
	live.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("selected merchant route status=%d", w.Code)
	}
}

var tlsState = tls.ConnectionState{}

func TestMerchantModeRejectsUnsafeConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name      string
		storeMode string
		opts      HTTPOptions
	}{
		{"store mismatch", "test", HTTPOptions{MerchantMode: "live", Merchant: liveMerchantHTTPProvider{}, MerchantWebhookSecret: "whsec_fixture"}},
		{"provider mismatch", "live", HTTPOptions{MerchantMode: "live", Merchant: shopProvider{}, MerchantWebhookSecret: "whsec_fixture"}},
		{"missing webhook", "live", HTTPOptions{MerchantMode: "live", Merchant: liveMerchantHTTPProvider{}}},
		{"missing provider", "live", HTTPOptions{MerchantMode: "live", MerchantWebhookSecret: "whsec_fixture"}},
		{"legacy live secret", "live", HTTPOptions{MerchantMode: "live", Merchant: liveMerchantHTTPProvider{}, TestMerchantWebhookSecret: "whsec_fixture"}},
		{"duplicate secrets", "test", HTTPOptions{MerchantMode: "test", Merchant: shopProvider{}, MerchantWebhookSecret: "whsec_one", TestMerchantWebhookSecret: "whsec_two"}},
		{"development live", "live", HTTPOptions{Development: true, MerchantMode: "live", Merchant: liveMerchantHTTPProvider{}, MerchantWebhookSecret: "whsec_fixture"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newStore(t)
			defer s.Close()
			s.merchantMode = tc.storeMode
			tc.opts.Origin = "https://portal.example.test"
			tc.opts.MerchantCountries = []string{"BG"}
			if _, err := NewHTTP(s, tc.opts); err == nil {
				t.Fatal("unsafe configuration accepted")
			}
		})
	}
}
