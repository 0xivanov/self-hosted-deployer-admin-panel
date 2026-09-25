//go:build integration

package portal

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestBillingModeHTTPConfigurationAndWebhook(t *testing.T) {
	s, path := newStore(t)
	s.Close()
	s, err := OpenWithBillingMode(path, "live")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, opts := range []HTTPOptions{
		{BillingMode: "unknown"},
		{BillingMode: "test"},
		{BillingMode: "live", TestBilling: true},
		{BillingMode: "live", TestWebhookSecret: billingSecret},
		{BillingMode: "live", Development: true},
	} {
		opts.Origin = "https://portal.example.test"
		if _, err := NewHTTP(s, opts); err == nil {
			t.Fatalf("accepted invalid options %+v", opts)
		}
	}
	h, err := NewHTTP(s, HTTPOptions{Origin: "https://portal.example.test", BillingMode: "live", BillingWebhookSecret: billingSecret})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", h.origin+"/api/config", nil))
	var cfg struct {
		Enabled bool   `json:"billing_enabled"`
		Mode    string `json:"billing_mode"`
		Test    bool   `json:"test_billing"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &cfg) != nil || !cfg.Enabled || cfg.Mode != "live" || cfg.Test {
		t.Fatalf("live config mislabeled: %d %s", w.Code, w.Body.String())
	}
	for _, tc := range []struct {
		path string
		live bool
		want int
	}{
		{"/webhooks/stripe-live", true, 204},
		{"/webhooks/stripe-live", false, 400},
		{"/webhooks/stripe-test", false, 404},
	} {
		body := modeBillingPayload(t, "evt_http_mode", tc.live)
		r := httptest.NewRequest("POST", h.origin+tc.path, bytes.NewReader(body))
		r.Header.Set("Stripe-Signature", modeBillingSignature(body))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("path=%s live=%v: got %d want %d", tc.path, tc.live, w.Code, tc.want)
		}
	}
}
