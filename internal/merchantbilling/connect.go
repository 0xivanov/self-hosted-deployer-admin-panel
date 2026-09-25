// Package merchantbilling contains mode-bound merchant payment adapters, separate
// from the hosting subscription provider.
package merchantbilling

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	stripe "github.com/stripe/stripe-go/v86"
)

type Client struct {
	live                  bool
	stripe                *stripe.Client
	http                  *http.Client
	returnURL, refreshURL string
	countries             map[string]struct{}
}

type Account struct {
	ID               string
	Country          string
	RequestID        string
	DetailsSubmitted bool
	ChargesEnabled   bool
	PayoutsEnabled   bool
	CardPayments     string
	ObservedAt       int64
}

type OnboardingLink struct {
	URL       string
	ExpiresAt int64
}

func NewTestClient(key, returnURL, refreshURL string, countries []string) (*Client, error) {
	return newTestClient(key, returnURL, refreshURL, countries, "")
}

const fixedStripeEndpoint = "https://api.stripe.com"

// NewLiveClient explicitly selects the live Stripe account. Callers must keep
// live merchant records separate from test records.
func NewLiveClient(key, returnURL, refreshURL string, countries []string) (*Client, error) {
	return newClient(key, returnURL, refreshURL, countries, fixedStripeEndpoint, true)
}

func newTestClient(key, returnURL, refreshURL string, countries []string, endpoint string) (*Client, error) {
	return newClient(key, returnURL, refreshURL, countries, endpoint, false)
}

func newClient(key, returnURL, refreshURL string, countries []string, endpoint string, live bool) (*Client, error) {
	prefix := "sk_test_"
	if live {
		prefix = "sk_live_"
	}
	if !strings.HasPrefix(key, prefix) || len(key) < 16 || strings.ContainsAny(key, " \r\n\t") {
		return nil, errors.New("a Stripe secret key matching the selected mode is required")
	}
	parseURL := func(raw string) *url.URL {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
			return nil
		}
		return u
	}
	returnParsed, refreshParsed := parseURL(returnURL), parseURL(refreshURL)
	if returnParsed == nil || refreshParsed == nil || returnParsed.Host != refreshParsed.Host {
		return nil, errors.New("fixed HTTPS Connect return URLs on one origin are required")
	}
	allowed := make(map[string]struct{}, len(countries))
	for _, country := range countries {
		country = strings.ToUpper(country)
		if len(country) != 2 || strings.TrimSpace(country) != country || country[0] < 'A' || country[0] > 'Z' || country[1] < 'A' || country[1] > 'Z' {
			return nil, errors.New("invalid Connect country allowlist")
		}
		allowed[country] = struct{}{}
	}
	if len(allowed) == 0 {
		return nil, errors.New("Connect country allowlist is required")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	hc := &http.Client{Transport: transport, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	cfg := &stripe.BackendConfig{HTTPClient: hc, MaxNetworkRetries: stripe.Int64(0), LeveledLogger: &stripe.LeveledLogger{Level: stripe.LevelNull}, EnableTelemetry: stripe.Bool(false)}
	if endpoint != "" {
		cfg.URL = stripe.String(endpoint)
	}
	sdk := stripe.NewClient(key, stripe.WithBackends(stripe.NewBackendsWithConfig(cfg)))
	return &Client{live: live, stripe: sdk, http: hc, returnURL: returnURL, refreshURL: refreshURL, countries: allowed}, nil
}

func (c *Client) Close() { c.http.CloseIdleConnections() }

func (c *Client) BillingMode() string {
	if c == nil {
		return ""
	}
	if c.live {
		return "live"
	}
	return "test"
}

func validRequestID(requestID string) bool {
	if len(requestID) != 64 {
		return false
	}
	for _, ch := range requestID {
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
			return false
		}
	}
	return true
}

func validAccountID(id string) bool {
	if !strings.HasPrefix(id, "acct_") || len(id) <= 5 || len(id) > 255 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_') {
			return false
		}
	}
	return true
}

func (c *Client) validCountry(country string) bool {
	_, ok := c.countries[strings.ToUpper(country)]
	return len(country) == 2 && country == strings.ToUpper(country) && ok
}

func (c *Client) accountResult(account *stripe.Account, country, requestID string) (Account, error) {
	if account == nil || account.Object != "account" || !validAccountID(account.ID) || !c.validCountry(country) || account.Country != country || account.Metadata["merchant_request"] != requestID || account.Controller == nil || account.Controller.Type != "application" || !account.Controller.IsController || account.Controller.Fees == nil || account.Controller.Fees.Payer != "account" || account.Controller.Losses == nil || account.Controller.Losses.Payments != "stripe" || account.Controller.RequirementCollection != "stripe" || account.Controller.StripeDashboard == nil || account.Controller.StripeDashboard.Type != "full" {
		return Account{}, errors.New("invalid Connect account response")
	}
	cardPayments := ""
	if account.Capabilities != nil {
		cardPayments = string(account.Capabilities.CardPayments)
	}
	switch cardPayments {
	case "", "inactive", "pending", "active":
	default:
		return Account{}, errors.New("unsupported card payment capability")
	}
	return Account{ID: account.ID, Country: account.Country, RequestID: requestID, DetailsSubmitted: account.DetailsSubmitted, ChargesEnabled: account.ChargesEnabled, PayoutsEnabled: account.PayoutsEnabled, CardPayments: cardPayments, ObservedAt: time.Now().Unix()}, nil
}

// CreateAccount requires an owner-authorized, persisted account intent.
// The caller must reconcile unknown outcomes before creating a replacement.
func (c *Client) CreateAccount(ctx context.Context, country, requestID string) (Account, error) {
	if !c.validCountry(country) || !validRequestID(requestID) {
		return Account{}, errors.New("invalid Connect account request")
	}
	params := &stripe.AccountCreateParams{
		Country: stripe.String(country),
		Controller: &stripe.AccountCreateControllerParams{
			Fees:                  &stripe.AccountCreateControllerFeesParams{Payer: stripe.String("account")},
			Losses:                &stripe.AccountCreateControllerLossesParams{Payments: stripe.String("stripe")},
			RequirementCollection: stripe.String("stripe"),
			StripeDashboard:       &stripe.AccountCreateControllerStripeDashboardParams{Type: stripe.String("full")},
		},
		Capabilities: &stripe.AccountCreateCapabilitiesParams{CardPayments: &stripe.AccountCreateCapabilitiesCardPaymentsParams{Requested: stripe.Bool(true)}},
		Metadata:     map[string]string{"merchant_request": requestID},
	}
	params.SetIdempotencyKey("merchant-account-" + requestID)
	account, err := c.stripe.V1Accounts.Create(ctx, params)
	if err != nil {
		return Account{}, errors.New("Connect account outcome unavailable; retain request for reconciliation")
	}
	return c.accountResult(account, country, requestID)
}

func (c *Client) RetrieveAccount(ctx context.Context, id, country, requestID string) (Account, error) {
	if !validAccountID(id) || !c.validCountry(country) || !validRequestID(requestID) {
		return Account{}, errors.New("invalid Connect account request")
	}
	params := &stripe.AccountRetrieveParams{}
	account, err := c.stripe.V1Accounts.GetByID(ctx, id, params)
	if err != nil {
		return Account{}, errors.New("Connect account retrieval unavailable; retain request for reconciliation")
	}
	result, err := c.accountResult(account, country, requestID)
	if err != nil || result.ID != id {
		return Account{}, errors.New("invalid Connect account response")
	}
	return result, nil
}

// CreateOnboardingLink accepts a previously bound merchant account and a separate
// persisted link intent. The caller must reauthorize the owner before returning
// this single-use credential, and must never log it or treat return as readiness.
func (c *Client) CreateOnboardingLink(ctx context.Context, accountID, requestID string) (OnboardingLink, error) {
	if !validAccountID(accountID) || !validRequestID(requestID) {
		return OnboardingLink{}, errors.New("invalid Connect onboarding request")
	}
	params := &stripe.AccountLinkCreateParams{Account: stripe.String(accountID), RefreshURL: stripe.String(c.refreshURL), ReturnURL: stripe.String(c.returnURL), Type: stripe.String(string(stripe.AccountLinkTypeAccountOnboarding))}
	params.SetIdempotencyKey("merchant-link-" + requestID)
	link, err := c.stripe.V1AccountLinks.Create(ctx, params)
	if err != nil {
		return OnboardingLink{}, errors.New("Connect onboarding link outcome unavailable; retain request for reconciliation")
	}
	now := time.Now().Unix()
	if link == nil {
		return OnboardingLink{}, errors.New("invalid Connect onboarding link response")
	}
	u, parseErr := url.Parse(link.URL)
	if link.Object != "account_link" || parseErr != nil || u.Scheme != "https" || u.Host != "connect.stripe.com" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || link.ExpiresAt <= now || link.ExpiresAt > now+3600 {
		return OnboardingLink{}, errors.New("invalid Connect onboarding link response")
	}
	return OnboardingLink{URL: link.URL, ExpiresAt: link.ExpiresAt}, nil
}
