//go:build integration

package portal

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

func newBuyerToken(t *testing.T, s *Store) string {
	t.Helper()
	session, err := s.CreateMerchantBuyerSession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return session.Token
}
func TestMerchantBuyerSessionExpiryRevocationAndRestart(t *testing.T) {
	s, path := newStore(t)
	ctx := t.Context()
	session, err := s.CreateMerchantBuyerSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.AuthenticateMerchantBuyer(ctx, session.Token); err != nil {
		t.Fatal(err)
	}
	if err = s.AuthenticateMerchantBuyer(ctx, randomToken()); !errors.Is(err, ErrDenied) {
		t.Fatal("forged buyer", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.AuthenticateMerchantBuyer(ctx, session.Token); err != nil {
		t.Fatal("restart", err)
	}
	s.now = func() time.Time { return session.ExpiresAt }
	if err = s.AuthenticateMerchantBuyer(ctx, session.Token); !errors.Is(err, ErrDenied) {
		t.Fatal("expired session accepted", err)
	}
	next, err := s.CreateMerchantBuyerSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RevokeMerchantBuyer(ctx, next.Token); err != nil {
		t.Fatal(err)
	}
	if err = s.AuthenticateMerchantBuyer(ctx, next.Token); !errors.Is(err, ErrDenied) {
		t.Fatal("revoked session accepted", err)
	}
}
func TestMerchantBuyerLogoutHTTP(t *testing.T) {
	s, _ := newStore(t)
	h, err := NewHTTP(s, HTTPOptions{Origin: "https://portal.example.test", Merchant: shopProvider{}, MerchantCountries: []string{"BG"}})
	if err != nil {
		t.Fatal(err)
	}
	token := newBuyerToken(t, s)
	cookie := &http.Cookie{Name: merchantBuyerCookie, Value: token}
	if w := portalRequest(h, "POST", "/api/shop/logout", `{}`, h.origin, "", cookie); w.Code != 403 {
		t.Fatal("csrf", w.Code)
	}
	if err = s.AuthenticateMerchantBuyer(t.Context(), token); err != nil {
		t.Fatal("failed logout revoked", err)
	}
	w := portalRequest(h, "POST", "/api/shop/logout", `{}`, h.origin, csrfFor(token), cookie)
	if w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].MaxAge != -1 || !cookies[0].Secure || cookies[0].Path != "/" {
		t.Fatal("clear cookie", cookies)
	}
	if err = s.AuthenticateMerchantBuyer(t.Context(), token); !errors.Is(err, ErrDenied) {
		t.Fatal("server session not revoked", err)
	}
	if w = portalRequest(h, "GET", "/api/shop/order?order="+randomToken(), "", "", "", cookie); w.Code != 401 {
		t.Fatal("revoked HTTP access", w.Code)
	}
}

func TestMerchantBuyerLegacyMigration(t *testing.T) {
	s, path, _, _, product := orderFixture(t)
	ctx := t.Context()
	token := randomToken()
	if _, err := s.RequestMerchantOrder(ctx, token, product.ID, product.Revision, randomToken()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("DROP TABLE hosting_workspace_policies; DROP TABLE merchant_order_recovery_grants; DROP TABLE merchant_order_recovery_codes; DROP TABLE merchant_buyer_sessions; PRAGMA user_version=33"); err != nil {
		t.Fatal(err)
	}
	before := time.Now().Add(30 * 24 * time.Hour).Unix()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.AuthenticateMerchantBuyer(ctx, token); err != nil {
		t.Fatal("legacy access lost", err)
	}
	var expires int64
	if err := s.db.QueryRow("SELECT expires_at FROM merchant_buyer_sessions WHERE token_hash=?", digest(token)).Scan(&expires); err != nil {
		t.Fatal(err)
	}
	if expires < before || expires > time.Now().Add(30*24*time.Hour).Unix() {
		t.Fatal("migration expiry", expires)
	}
	if err := s.RevokeMerchantBuyer(ctx, token); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.AuthenticateMerchantBuyer(ctx, token); !errors.Is(err, ErrDenied) {
		t.Fatal("restart resurrected revoked legacy session", err)
	}
}
