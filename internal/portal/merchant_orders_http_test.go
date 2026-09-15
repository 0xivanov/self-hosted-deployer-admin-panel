//go:build integration

package portal

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestMerchantOrderHistoryHTTPIsolation(t *testing.T) {
	t.Parallel()
	s, _, owner, session, product := orderFixture(t)
	h, err := NewHTTP(s, HTTPOptions{Origin: "https://portal.example.test", Merchant: onboardingProvider{}, MerchantCountries: []string{"BG"}})
	if err != nil {
		t.Fatal(err)
	}
	buyer := randomToken()
	order, err := s.RequestMerchantOrder(t.Context(), buyer, product.ID, product.Revision, randomToken())
	if err != nil {
		t.Fatal(err)
	}
	// Set provider values so omission is checked with populated private fields.
	if _, err = s.db.Exec("UPDATE merchant_orders SET session_id=?,checkout_url=?,payment_intent_id=? WHERE id=?", "cs_test_private", "https://checkout.stripe.com/private", "pi_private", order.ID); err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: h.cookie, Value: session.Token}
	path := "/api/merchant/orders?workspace=" + owner.WorkspaceID
	w := portalRequest(h, "GET", path, "", "", "", cookie)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var body struct {
		Orders []MerchantOrder `json:"orders"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body.Orders) != 1 || body.Orders[0].ID != order.ID {
		t.Fatal(body, err)
	}
	for _, private := range []string{buyer, order.BuyerHash, order.RequestKey, order.AccountID, "cs_test_private", "pi_private", "checkout.stripe.com", "buyer_hash", "request_key", "account_id", "session_id", "checkout_url", "payment_intent_id"} {
		if strings.Contains(w.Body.String(), private) {
			t.Fatalf("private field disclosed: %s", private)
		}
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("history may be cached", w.Header())
	}
	if w = portalRequest(h, "GET", path, "", "", "", nil); w.Code != 401 {
		t.Fatal("anonymous", w.Code)
	}
	foreign, other := verifiedAccount(t, s, "foreign-order-history@example.test")
	foreignCookie := &http.Cookie{Name: h.cookie, Value: other.Token}
	if w = portalRequest(h, "GET", path, "", "", "", foreignCookie); w.Code != 403 {
		t.Fatal("foreign", w.Code)
	}
	if _, err = s.db.Exec("INSERT INTO memberships VALUES(?,?,'developer')", foreign.ID, owner.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if w = portalRequest(h, "GET", path, "", "", "", foreignCookie); w.Code != 403 {
		t.Fatal("developer", w.Code)
	}
	if w = portalRequest(h, "GET", "/api/merchant/orders?workspace="+foreign.WorkspaceID, "", "", "", foreignCookie); w.Code != 200 || !strings.Contains(w.Body.String(), `"orders":[]`) {
		t.Fatal("empty history", w.Code, w.Body.String())
	}
	if w = portalRequest(h, "POST", path, `{}`, h.origin, csrfFor(session.Token), cookie); w.Code != 405 || w.Header().Get("Allow") != "GET" {
		t.Fatal("unsupported method", w.Code)
	}
	h.merchant = nil
	if w = portalRequest(h, "GET", path, "", "", "", cookie); w.Code != 404 {
		t.Fatal("disabled", w.Code)
	}
}
