//go:build integration

package portal

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/merchantbilling"
)

type shopProvider struct {
	onboardingProvider
	checkoutProviderFixture
}

func TestMerchantShopBuyerCheckout(t *testing.T) {
	s, _, _, _, product := orderFixture(t)
	calls := 0
	p := shopProvider{checkoutProviderFixture: checkoutProviderFixture{
		create: func(_ context.Context, account string, input merchantbilling.CheckoutOrder) (merchantbilling.Checkout, error) {
			calls++
			if account != "acct_orders" || input.AmountMinor != 1250 {
				t.Fatal("wrong charge", account, input)
			}
			return openCheckout("cs_test_shop"), nil
		},
		read: func(_ context.Context, account, id string, input merchantbilling.CheckoutOrder) (merchantbilling.Checkout, error) {
			v := openCheckout(id)
			v.State = "complete"
			v.PaymentStatus = "paid"
			v.PaymentIntentID = "pi_shop"
			v.URL = ""
			return v, nil
		},
	}}
	h, err := NewHTTP(s, HTTPOptions{Origin: "https://portal.example.test", Merchant: p, MerchantCountries: []string{"BG"}})
	if err != nil {
		t.Fatal(err)
	}
	w := portalRequest(h, "GET", "/api/shop/session", "", "", "", nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal(cookies)
	}
	cookie := cookies[0]
	if !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Domain != "" || cookie.Path != "/" {
		t.Fatal("buyer cookie", cookie)
	}
	var auth struct {
		CSRF string `json:"csrf"`
	}
	json.Unmarshal(w.Body.Bytes(), &auth)
	if auth.CSRF == "" || strings.Contains(w.Body.String(), cookie.Value) {
		t.Fatal("buyer token exposed")
	}
	w = portalRequest(h, "GET", "/api/shop/product?product="+product.ID, "", "", "", nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), "workspace") {
		t.Fatal(w.Code, w.Body.String())
	}
	input := `{"product":"` + product.ID + `","revision":1,"key":"` + randomToken() + `"}`
	if w = portalRequest(h, "POST", "/api/shop/orders", input, h.origin, "", cookie); w.Code != 403 {
		t.Fatal("csrf", w.Code)
	}
	if w = portalRequest(h, "POST", "/api/shop/orders", input, "https://foreign.test", auth.CSRF, cookie); w.Code != 403 {
		t.Fatal("origin", w.Code)
	}
	w = portalRequest(h, "POST", "/api/shop/orders", input, h.origin, auth.CSRF, cookie)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var order struct{ ID, State, URL string }
	if err = json.Unmarshal(w.Body.Bytes(), &order); err != nil || order.State != "open" || order.URL == "" {
		t.Fatal(order, err)
	}
	for _, hidden := range []string{"account_id", "session_id", "payment_intent_id", "buyer_hash", "workspace_id", cookie.Value} {
		if strings.Contains(w.Body.String(), hidden) {
			t.Fatal("private field", hidden)
		}
	}
	if w = portalRequest(h, "POST", "/api/shop/orders", input, h.origin, auth.CSRF, cookie); w.Code != 200 || calls != 1 {
		t.Fatal("duplicate", w.Code, calls)
	}
	foreign := &http.Cookie{Name: cookie.Name, Value: randomToken()}
	if w = portalRequest(h, "GET", "/api/shop/order?order="+order.ID, "", "", "", foreign); w.Code == 200 {
		t.Fatal("foreign order")
	}
	if w = portalRequest(h, "POST", "/api/shop/refresh", `{"order":"`+order.ID+`"}`, h.origin, csrfFor(foreign.Value), foreign); w.Code == 200 {
		t.Fatal("foreign refresh")
	}
	w = portalRequest(h, "POST", "/api/shop/refresh", `{"order":"`+order.ID+`"}`, h.origin, auth.CSRF, cookie)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"payment_status":"paid"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, path := range []string{"/shop?product=" + product.ID, "/merchant/sales/success", "/merchant/sales/cancel", "/shop.js"} {
		if w = portalRequest(h, "GET", path, "", "", "", nil); w.Code != 200 {
			t.Fatal(path, w.Code)
		}
	}
}

func TestMerchantShopUnknownCreationAndRateLimit(t *testing.T) {
	s, _, _, _, product := orderFixture(t)
	calls := 0
	p := shopProvider{checkoutProviderFixture: checkoutProviderFixture{create: func(context.Context, string, merchantbilling.CheckoutOrder) (merchantbilling.Checkout, error) {
		calls++
		return merchantbilling.Checkout{}, errors.New("private provider response")
	}}}
	h, err := NewHTTP(s, HTTPOptions{Origin: "https://portal.example.test", Merchant: p, MerchantCountries: []string{"BG"}})
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: "__Host-merchant-buyer", Value: newBuyerToken(t, s)}
	input := `{"product":"` + product.ID + `","revision":1,"key":"` + randomToken() + `"}`
	for range 2 {
		w := portalRequest(h, "POST", "/api/shop/orders", input, h.origin, csrfFor(cookie.Value), cookie)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"submitted"`) || strings.Contains(w.Body.String(), "private provider") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if calls != 1 {
		t.Fatal(calls)
	}
	limited := false
	for range 40 {
		w := portalRequest(h, "GET", "/api/shop/session", "", "", "", cookie)
		if w.Code == 429 {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("missing public limit")
	}
	h.merchant = nil
	if w := portalRequest(h, "GET", "/api/shop/product?product="+product.ID, "", "", "", nil); w.Code != 404 {
		t.Fatal("disabled", w.Code)
	}
}

func TestMerchantShopRetryBeforeSubmission(t *testing.T) {
	s, _, _, _, product := orderFixture(t)
	buyer := newBuyerToken(t, s)
	order, err := s.RequestMerchantOrder(t.Context(), buyer, product.ID, product.Revision, randomToken())
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	p := shopProvider{checkoutProviderFixture: checkoutProviderFixture{create: func(context.Context, string, merchantbilling.CheckoutOrder) (merchantbilling.Checkout, error) {
		calls++
		return openCheckout("cs_test_retry"), nil
	}}}
	h, err := NewHTTP(s, HTTPOptions{Origin: "https://portal.example.test", Merchant: p, MerchantCountries: []string{"BG"}})
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: merchantBuyerCookie, Value: buyer}
	w := portalRequest(h, "POST", "/api/shop/refresh", `{"order":"`+order.ID+`"}`, h.origin, csrfFor(buyer), cookie)
	if w.Code != 200 || calls != 1 || !strings.Contains(w.Body.String(), `"state":"open"`) {
		t.Fatal(w.Code, calls, w.Body.String())
	}
	if _, err = s.db.Exec("UPDATE merchant_products SET active=0 WHERE id=?", product.ID); err != nil {
		t.Fatal(err)
	}
	w = portalRequest(h, "GET", "/api/shop/product?product="+product.ID, "", "", "", nil)
	if w.Code != 404 {
		t.Fatal("disabled product public", w.Code)
	}
}
