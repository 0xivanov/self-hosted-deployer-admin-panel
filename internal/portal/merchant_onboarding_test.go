//go:build integration

package portal

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/merchantbilling"
)

type onboardingProvider struct {
	merchantProviderFixture
	link func(context.Context, string, string) (merchantbilling.OnboardingLink, error)
}

func (p onboardingProvider) CreateOnboardingLink(ctx context.Context, account, request string) (merchantbilling.OnboardingLink, error) {
	return p.link(ctx, account, request)
}
func onboardingFixture(t *testing.T) (*Store, Account, Session, onboardingProvider) {
	t.Helper()
	s, _ := newStore(t)
	a, session := verifiedAccount(t, s, "onboarding@example.test")
	p := onboardingProvider{merchantProviderFixture: merchantProviderFixture{create: func(_ context.Context, country, request string) (merchantbilling.Account, error) {
		return merchantSnapshot("acct_onboarding", country, request), nil
	}, read: func(_ context.Context, id, country, request string) (merchantbilling.Account, error) {
		v := merchantSnapshot(id, country, request)
		v.ChargesEnabled = true
		v.CardPayments = "active"
		return v, nil
	}}, link: func(_ context.Context, id, request string) (merchantbilling.OnboardingLink, error) {
		if id != "acct_onboarding" || len(request) != 64 {
			t.Error("wrong link binding")
		}
		return merchantbilling.OnboardingLink{URL: "https://connect.stripe.com/setup/secret", ExpiresAt: time.Now().Unix() + 300}, nil
	}}
	return s, a, session, p
}
func TestMerchantOwnerHTTPAndOnboarding(t *testing.T) {
	t.Parallel()
	s, a, session, p := onboardingFixture(t)
	ctx := t.Context()
	h, err := NewHTTP(s, HTTPOptions{Origin: "https://portal.example.test", Merchant: p, MerchantCountries: []string{"BG"}})
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: h.cookie, Value: session.Token}
	csrf := csrfFor(session.Token)
	body := `{"workspace":"` + a.WorkspaceID + `","country":"BG"}`
	if w := portalRequest(h, "POST", "/api/merchant/account", body, h.origin, "", cookie); w.Code != 403 {
		t.Fatal("csrf", w.Code)
	}
	if w := portalRequest(h, "POST", "/api/merchant/account", body, h.origin, csrf, cookie); w.Code != 200 || !strings.Contains(w.Body.String(), "bound") || strings.Contains(w.Body.String(), "acct_onboarding") {
		t.Fatal(w.Code, w.Body.String())
	}
	body = `{"workspace":"` + a.WorkspaceID + `"}`
	if w := portalRequest(h, "POST", "/api/merchant/refresh", body, h.origin, csrf, cookie); w.Code != 200 || !strings.Contains(w.Body.String(), `"charges_enabled":true`) {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := portalRequest(h, "POST", "/api/merchant/onboarding", body, h.origin, csrf, cookie); w.Code != 200 || !strings.Contains(w.Body.String(), "connect.stripe.com") {
		t.Fatal(w.Code, w.Body.String())
	}
	var logged int
	if err = s.db.QueryRow("SELECT count(*) FROM audit_events WHERE action LIKE '%https:%' OR action LIKE '%secret%'").Scan(&logged); err != nil || logged != 0 {
		t.Fatal("link logged", logged, err)
	}
	other, foreign := verifiedAccount(t, s, "onboarding-other@example.test")
	_ = other
	if _, err = s.MerchantOnboardingLink(ctx, foreign.Token, a.WorkspaceID, p); !errors.Is(err, ErrDenied) {
		t.Fatal("foreign link", err)
	}
	for _, path := range []string{"/merchant/return", "/merchant/refresh"} {
		if w := portalRequest(h, "GET", path, "", "", "", nil); w.Code != 200 {
			t.Fatal("return route", w.Code)
		}
	}
	h.merchant = nil
	if w := portalRequest(h, "GET", "/api/merchant/account?workspace="+a.WorkspaceID, "", "", "", cookie); w.Code != 404 {
		t.Fatal("disabled", w.Code)
	}
}
func TestMerchantLinkReauthorizesAfterProvider(t *testing.T) {
	t.Parallel()
	s, a, session, p := onboardingFixture(t)
	ctx := t.Context()
	intent, err := s.RequestMerchantAccount(ctx, session.Token, a.WorkspaceID, "BG")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DispatchMerchantAccount(ctx, intent.RequestID, p); err != nil {
		t.Fatal(err)
	}
	p.link = func(context.Context, string, string) (merchantbilling.OnboardingLink, error) {
		if err := s.Logout(ctx, session.Token); err != nil {
			t.Fatal(err)
		}
		return merchantbilling.OnboardingLink{URL: "https://connect.stripe.com/setup/secret", ExpiresAt: time.Now().Unix() + 300}, nil
	}
	link, err := s.MerchantOnboardingLink(ctx, session.Token, a.WorkspaceID, p)
	if !errors.Is(err, ErrDenied) || link.URL != "" {
		t.Fatal("revoked owner received link", link, err)
	}
}
func TestMerchantRefreshFencesOlderRead(t *testing.T) {
	t.Parallel()
	s, a, session, p := onboardingFixture(t)
	ctx := t.Context()
	intent, err := s.RequestMerchantAccount(ctx, session.Token, a.WorkspaceID, "BG")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DispatchMerchantAccount(ctx, intent.RequestID, p); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	slow := p
	slow.read = func(_ context.Context, id, country, request string) (merchantbilling.Account, error) {
		close(entered)
		<-release
		return merchantSnapshot(id, country, request), nil
	}
	result := make(chan error, 1)
	go func() { result <- s.RefreshMerchantAccount(ctx, session.Token, a.WorkspaceID, slow) }()
	<-entered
	err = s.RefreshMerchantAccount(ctx, session.Token, a.WorkspaceID, p)
	close(release)
	old := <-result
	if err != nil || !errors.Is(old, ErrBillingConflict) {
		t.Fatal(err, old)
	}
	view, err := s.MerchantStatus(ctx, session.Token, a.WorkspaceID)
	if err != nil || !view.ChargesEnabled || view.Stale {
		t.Fatal(view, err)
	}
}

func TestMerchantLinkRequestLimit(t *testing.T) {
	t.Parallel()
	s, a, session, p := onboardingFixture(t)
	ctx := t.Context()
	intent, err := s.RequestMerchantAccount(ctx, session.Token, a.WorkspaceID, "BG")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DispatchMerchantAccount(ctx, intent.RequestID, p); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		if _, err = s.MerchantOnboardingLink(ctx, session.Token, a.WorkspaceID, p); err != nil {
			t.Fatal(err)
		}
	}
	p.link = func(context.Context, string, string) (merchantbilling.OnboardingLink, error) {
		t.Fatal("limited request reached provider")
		return merchantbilling.OnboardingLink{}, nil
	}
	if _, err = s.MerchantOnboardingLink(ctx, session.Token, a.WorkspaceID, p); !errors.Is(err, ErrMerchantRateLimited) {
		t.Fatal(err)
	}
}
