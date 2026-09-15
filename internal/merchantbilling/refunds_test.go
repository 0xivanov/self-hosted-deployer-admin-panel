//go:build integration

package merchantbilling

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func refundRequestFixture() RefundRequest {
	return RefundRequest{RequestID: requestFixture, OrderID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", PaymentIntentID: "pi_fixture", Currency: "eur", AmountMinor: 1250}
}

func refundFixture(request RefundRequest) map[string]any {
	return map[string]any{
		"object":         "refund",
		"id":             "re_fixture",
		"amount":         request.AmountMinor,
		"currency":       request.Currency,
		"payment_intent": map[string]string{"id": request.PaymentIntentID},
		"metadata":       map[string]string{"merchant_refund": request.RequestID, "merchant_order": request.OrderID, "unrelated": "allowed"},
		"status":         "pending",
	}
}

func TestMerchantRefundScopeAndIdentity(t *testing.T) {
	t.Parallel()
	request := refundRequestFixture()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Stripe-Account") != "acct_fixture" {
			t.Error("merchant scope missing")
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.Method == "POST" {
			if r.URL.Path != "/v1/refunds" || r.Header.Get("Idempotency-Key") != "merchant-refund-"+request.RequestID {
				t.Error("refund request identity")
			}
			for field, want := range map[string]string{"payment_intent": request.PaymentIntentID, "amount": "1250", "metadata[merchant_refund]": request.RequestID, "metadata[merchant_order]": request.OrderID} {
				if r.Form.Get(field) != want {
					t.Error("unexpected refund field", field, r.Form.Get(field))
				}
			}
			for _, field := range []string{"currency", "charge", "reason", "refund_application_fee", "reverse_transfer", "instructions_email", "customer"} {
				if r.Form.Has(field) {
					t.Error("unexpected refund field", field)
				}
			}
		} else if r.Method != "GET" || r.URL.Path != "/v1/refunds/re_fixture" {
			t.Error("wrong refund route")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(refundFixture(request))
	}))
	defer server.Close()
	c, err := newTestClient("sk_test_synthetic_fixture", "https://portal.example.test/return", "https://portal.example.test/refresh", []string{"BG"}, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for range 2 {
		got, err := c.CreateRefund(t.Context(), "acct_fixture", request)
		if err != nil || got.ID != "re_fixture" || got.State != "pending" || got.ObservedAt == 0 {
			t.Fatal(got, err)
		}
	}
	if _, err = c.RetrieveRefund(t.Context(), "acct_fixture", "re_fixture", request); err != nil {
		t.Fatal(err)
	}
	bad := request
	bad.AmountMinor = 0
	if _, err = c.CreateRefund(t.Context(), "acct_fixture", bad); err == nil {
		t.Fatal("accepted invalid amount")
	}
	if _, err = c.CreateRefund(t.Context(), "acct_fixture/foreign", request); err == nil {
		t.Fatal("accepted invalid account")
	}
	if calls != 3 {
		t.Fatal("invalid input reached provider", calls)
	}
}

func TestMerchantRefundRejectsMismatchedProviderEvidence(t *testing.T) {
	t.Parallel()
	request := refundRequestFixture()
	cases := map[string]func(map[string]any){
		"object":   func(v map[string]any) { v["object"] = "charge" },
		"id":       func(v map[string]any) { v["id"] = "re_other" },
		"amount":   func(v map[string]any) { v["amount"] = 1251 },
		"currency": func(v map[string]any) { v["currency"] = "usd" },
		"intent":   func(v map[string]any) { v["payment_intent"] = map[string]string{"id": "pi_other"} },
		"metadata": func(v map[string]any) {
			v["metadata"] = map[string]string{"merchant_refund": "wrong", "merchant_order": request.OrderID}
		},
		"reversal": func(v map[string]any) { v["transfer_reversal"] = map[string]string{"id": "tr_fixture"} },
		"state":    func(v map[string]any) { v["status"] = "unknown" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				v := refundFixture(request)
				change(v)
				_ = json.NewEncoder(w).Encode(v)
			}))
			defer server.Close()
			c, err := newTestClient("sk_test_synthetic_fixture", "https://portal.example.test/return", "https://portal.example.test/refresh", []string{"BG"}, server.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if _, err = c.RetrieveRefund(t.Context(), "acct_fixture", "re_fixture", request); err == nil {
				t.Fatal("accepted altered refund")
			}
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"private-provider-detail"}}`))
	}))
	defer server.Close()
	c, err := newTestClient("sk_test_synthetic_fixture", "https://portal.example.test/return", "https://portal.example.test/refresh", []string{"BG"}, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err = c.CreateRefund(t.Context(), "acct_fixture", request); err == nil || strings.Contains(err.Error(), "private-provider-detail") {
		t.Fatal("unsafe provider error", err)
	}
}
