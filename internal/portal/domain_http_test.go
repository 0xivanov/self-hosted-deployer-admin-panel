//go:build integration

package portal

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/domains"
)

func TestDomainQuoteHTTPProtection(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)
	a, _ := verifiedAccount(t, s, "domain-http@example.test")
	_, other := verifiedAccount(t, s, "domain-http-other@example.test")
	calls := 0
	p := domainQuoteFunc(func(_ context.Context, name string) (domains.RegistrarQuote, error) {
		calls++
		return domains.RegistrarQuote{Domain: name, Available: true, PremiumChecked: true, Currency: "eur", RegistrationMinor: 900, RenewalMinor: 1000, CheckedAt: s.now()}, nil
	})
	h, err := NewHTTP(s, HTTPOptions{Origin: "https://portal.example.test", DomainQuotes: p, DomainMarkupMinor: 200})
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf := httpLogin(t, h, a.Email)
	body := `{"workspace":"` + a.WorkspaceID + `","domain":"example.com"}`
	for _, tc := range []struct {
		name, origin, csrf, body string
		code                     int
	}{
		{"csrf", h.origin, "", body, 403},
		{"origin", "https://foreign.example", csrf, body, 403},
		{"price injection", h.origin, csrf, strings.TrimSuffix(body, "}") + `,"markup":0}`, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := portalRequest(h, "POST", "/api/domains/quote", tc.body, tc.origin, tc.csrf, cookie)
			if w.Code != tc.code {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
	if calls != 0 {
		t.Fatal("invalid request contacted registrar")
	}
	w := portalRequest(h, "POST", "/api/domains/quote", body, h.origin, csrf, cookie)
	var q DomainQuote
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &q) != nil || q.Offer.RegistrationMinor != 1100 {
		t.Fatal(w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "evidence") {
		t.Fatal("provider evidence exposed")
	}
	read := portalRequest(h, "GET", "/api/domains/quote?workspace="+a.WorkspaceID+"&id="+q.ID, "", "", "", cookie)
	if read.Code != 200 {
		t.Fatal(read.Code, read.Body.String())
	}
	otherCookie, otherCSRF := httpLogin(t, h, other.Account.Email)
	denied := portalRequest(h, "POST", "/api/domains/quote", body, h.origin, otherCSRF, otherCookie)
	if denied.Code != 403 || calls != 1 {
		t.Fatal(denied.Code, calls)
	}
	denied = portalRequest(h, "GET", "/api/domains/quote?workspace="+a.WorkspaceID+"&id="+q.ID, "", "", "", otherCookie)
	if denied.Code != 403 {
		t.Fatal(denied.Code)
	}
	for i := 0; i < 4; i++ {
		if w = portalRequest(h, "POST", "/api/domains/quote", body, h.origin, csrf, cookie); w.Code != 200 {
			t.Fatal(w.Code)
		}
	}
	// A new session for the same account cannot reset the lookup allowance.
	cookie2, csrf2 := httpLogin(t, h, a.Email)
	w = portalRequest(h, "POST", "/api/domains/quote", body, h.origin, csrf2, cookie2)
	if w.Code != 429 || calls != 5 || w.Header().Get("Retry-After") != "60" {
		t.Fatal(w.Code, calls)
	}
	now := s.now()
	s.now = func() time.Time { return now.Add(time.Minute) }
	w = portalRequest(h, "POST", "/api/domains/quote", body, h.origin, csrf2, cookie2)
	if w.Code != 200 || calls != 6 {
		t.Fatal(w.Code, calls)
	}
}

func TestDomainQuoteHTTPDisabledAndSanitized(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)
	a, _ := verifiedAccount(t, s, "domain-disabled@example.test")
	h, err := NewHTTP(s, HTTPOptions{Origin: "https://portal.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf := httpLogin(t, h, a.Email)
	body := `{"workspace":"` + a.WorkspaceID + `","domain":"example.com"}`
	w := portalRequest(h, "POST", "/api/domains/quote", body, h.origin, csrf, cookie)
	if w.Code != 404 {
		t.Fatal(w.Code)
	}
	h.domainQuotes = domainQuoteFunc(func(context.Context, string) (domains.RegistrarQuote, error) {
		return domains.RegistrarQuote{}, errors.New("secret-provider-key")
	})
	w = portalRequest(h, "POST", "/api/domains/quote", body, h.origin, csrf, cookie)
	if w.Code != 503 || strings.Contains(w.Body.String(), "secret-provider-key") {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, err = NewHTTP(s, HTTPOptions{Origin: h.origin, DomainMarkupMinor: -1}); err == nil {
		t.Fatal("negative markup accepted")
	}
}
