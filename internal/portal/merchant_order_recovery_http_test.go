//go:build integration

package portal

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestMerchantShopRecoveryHTTP(t *testing.T) {
	s, _, owner, session, order := paidOrderFixture(t)
	buyer := newBuyerToken(t, s)
	if _, err := s.db.Exec("UPDATE merchant_orders SET buyer_hash=? WHERE id=?", digest(buyer), order.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RequestMerchantRefund(t.Context(), session.Token, owner.WorkspaceID, order.ID); err != nil {
		t.Fatal(err)
	}
	h, err := NewHTTP(s, HTTPOptions{Origin: "https://portal.example.test", Merchant: shopProvider{}, MerchantCountries: []string{"BG"}})
	if err != nil {
		t.Fatal(err)
	}
	original := &http.Cookie{Name: merchantBuyerCookie, Value: buyer}
	other := &http.Cookie{Name: merchantBuyerCookie, Value: newBuyerToken(t, s)}
	body := `{"order":"` + order.ID + `"}`
	for _, tc := range []struct {
		path, body, csrf string
		cookie           *http.Cookie
		status           int
	}{
		{"/api/shop/recovery-code", body, "", original, 403},
		{"/api/shop/recovery-code", body, csrfFor(other.Value), other, 404},
		{"/api/shop/recovery-code", body, "", nil, 401},
		{"/api/shop/recovery-code?order=x", body, csrfFor(buyer), original, 400},
		{"/api/shop/recovery-code", `{"order":"` + order.ID + `","code":"bad"}`, csrfFor(buyer), original, 400},
	} {
		w := portalRequest(h, "POST", tc.path, tc.body, h.origin, tc.csrf, tc.cookie)
		if w.Code != tc.status {
			t.Fatalf("%s: got %d want %d", tc.path, w.Code, tc.status)
		}
	}
	w := portalRequest(h, "POST", "/api/shop/recovery-code", body, h.origin, csrfFor(buyer), original)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var issued struct {
		Code      string `json:"code"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	if !validMerchantOrderToken(issued.Code) || issued.ExpiresAt <= s.now().Unix() || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("unsafe code response")
	}
	redeem := `{"code":"` + issued.Code + `"}`
	if w = portalRequest(h, "POST", "/api/shop/recover", redeem, h.origin, "", other); w.Code != 403 {
		t.Fatal("missing CSRF", w.Code)
	}
	if w = portalRequest(h, "POST", "/api/shop/recover?code=x", redeem, h.origin, csrfFor(other.Value), other); w.Code != 400 {
		t.Fatal("query allowed", w.Code)
	}
	for i := 0; i < 2; i++ {
		w = portalRequest(h, "POST", "/api/shop/recover", redeem, h.origin, csrfFor(other.Value), other)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var view shopOrder
		if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		if view.ID != order.ID || view.PaymentStatus != "paid" || view.Refund == nil || view.Refund.State != "requested" {
			t.Fatal("lost recovered order/refund", view)
		}
		for _, secret := range []string{buyer, other.Value, issued.Code, digest(issued.Code), "buyer_hash", "session_hash", "payment_intent_id", "acct_orders"} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatal("private data exposed")
			}
		}
	}
	if err := s.RevokeMerchantBuyer(t.Context(), other.Value); err != nil {
		t.Fatal(err)
	}
	if w = portalRequest(h, "GET", "/api/shop/order?order="+order.ID, "", "", "", other); w.Code != 401 {
		t.Fatal("revoked recovered browser", w.Code)
	}
}

func TestMerchantRecoveryMigrationPreservesBuyer(t *testing.T) {
	s, path, _, _, product := orderFixture(t)
	buyer := newBuyerToken(t, s)
	order, err := s.RequestMerchantOrder(t.Context(), buyer, product.ID, product.Revision, randomToken())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("DROP TABLE project_domains; ALTER TABLE projects DROP COLUMN deletion_requested_at; ALTER TABLE projects DROP COLUMN deletion_error; ALTER TABLE billing_checkouts DROP COLUMN hosting_limits; DROP TABLE hosting_plan_limits; DROP TABLE hosting_workspace_policies; DROP TABLE merchant_order_recovery_grants; DROP TABLE merchant_order_recovery_codes; PRAGMA user_version=34"); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.AuthenticateMerchantBuyer(t.Context(), buyer); err != nil {
		t.Fatal("migration lost session", err)
	}
	saved, err := s.BuyerMerchantOrder(t.Context(), buyer, order.ID)
	if err != nil || saved.BuyerHash != order.BuyerHash || saved.RequestKey != order.RequestKey {
		t.Fatal("migration changed order", err)
	}
	if _, err = s.IssueMerchantOrderRecoveryCode(t.Context(), buyer, order.ID); err != nil {
		t.Fatal("migration cannot issue recovery", err)
	}
}
