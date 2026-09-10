//go:build integration

package portal

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"

	stripe "github.com/stripe/stripe-go/v86"
)

func TestMountedBillingWebhookDurableCheckoutFlow(t *testing.T) {
	t.Parallel()
	s, _, a, session := billingCheckoutFixture(t)
	c, err := s.RequestBillingCheckout(t.Context(), session.Token, a.WorkspaceID, "starter")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BindBillingCheckout(t.Context(), c.ID, "cs_test_mounted", "https://checkout.stripe.com/c/pay/cs_test_mounted"); err != nil {
		t.Fatal(err)
	}
	h, err := NewHTTP(s, HTTPOptions{Origin: "https://portal.example.test", TestBilling: true, TestWebhookSecret: billingSecret})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{"id": "evt_mounted", "object": "event", "type": "checkout.session.completed", "api_version": stripe.APIVersion, "created": 1700000000, "livemode": false, "data": map[string]any{"object": map[string]any{"id": "cs_test_mounted", "object": "checkout.session", "livemode": false, "mode": "subscription", "status": "complete", "customer": c.CustomerID, "client_reference_id": c.ID, "subscription": "sub_mounted"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, url, method, origin string
		signed                    bool
		code                      int
	}{
		{"missing signature", h.origin + "/webhooks/stripe-test", "POST", "", false, 400},
		{"browser origin", h.origin + "/webhooks/stripe-test", "POST", h.origin, true, 403},
		{"plaintext", "http://portal.example.test/webhooks/stripe-test", "POST", "", true, 403},
		{"foreign host", "https://foreign.test/webhooks/stripe-test", "POST", "", true, 403},
		{"get", h.origin + "/webhooks/stripe-test", "GET", "", true, 405},
		{"query alias", h.origin + "/webhooks/stripe-test?x=1", "POST", "", true, 404},
		{"encoded alias", h.origin + "/webhooks/%73tripe-test", "POST", "", true, 404},
		{"valid without browser session", h.origin + "/webhooks/stripe-test", "POST", "", true, 204},
		{"duplicate", h.origin + "/webhooks/stripe-test", "POST", "", true, 204},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.url, bytes.NewReader(body))
			r.Header.Set("Origin", tc.origin)
			if tc.signed {
				r.Header.Set("Stripe-Signature", billingSignature(body))
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.code {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
	if worked, err := s.BillingWorkOnce(t.Context(), workerProvider{}); !worked || err != nil {
		t.Fatal(worked, err)
	}
	checkout, err := s.BillingCheckout(t.Context(), session.Token, a.WorkspaceID, c.ID)
	if err != nil || checkout.State != "completed" {
		t.Fatal(checkout, err)
	}
	w := portalRequest(h, "GET", "/api/billing/customer?workspace="+a.WorkspaceID, "", "", "", nil)
	if w.Code != 401 {
		t.Fatal("webhook bypassed browser auth", w.Code)
	}
	disabled, err := NewHTTP(s, HTTPOptions{Origin: h.origin, TestBilling: true})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", h.origin+"/webhooks/stripe-test", bytes.NewReader(body))
	r.Header.Set("Stripe-Signature", billingSignature(body))
	w = httptest.NewRecorder()
	disabled.ServeHTTP(w, r)
	if w.Code != 404 {
		t.Fatal("webhook active without configuration", w.Code)
	}
}

func TestWebhookConfigurationRequiresHTTPSAndTestBilling(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)
	for _, tc := range []struct {
		name string
		opts HTTPOptions
	}{
		{"billing disabled", HTTPOptions{Origin: "https://portal.example.test", TestWebhookSecret: billingSecret}},
		{"development", HTTPOptions{Origin: "http://127.0.0.1:8791", Development: true, TestBilling: true, TestWebhookSecret: billingSecret}},
		{"invalid secret", HTTPOptions{Origin: "https://portal.example.test", TestBilling: true, TestWebhookSecret: "invalid"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewHTTP(s, tc.opts); err == nil {
				t.Fatal("unsafe webhook configuration accepted")
			}
		})
	}
}
