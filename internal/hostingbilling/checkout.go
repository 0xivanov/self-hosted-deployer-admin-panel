package hostingbilling

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
	live            bool
	stripe          *stripe.Client
	http            *http.Client
	success, cancel string
	plans           map[string]string
}

func (c *Client) BillingMode() string {
	if c != nil && c.live {
		return "live"
	}
	return "test"
}

type Checkout struct {
	ID  string
	URL string
}

func NewTestClient(key, success, cancel string, plans map[string]string) (*Client, error) {
	return newTestClient(key, success, cancel, plans, "")
}

// NewLiveClient explicitly selects real-money provider mode. Callers must keep
// live customer, checkout and entitlement records separate from sandbox data.
func NewLiveClient(key, success, cancel string, plans map[string]string) (*Client, error) {
	return newClient(key, success, cancel, plans, "", true)
}
func newTestClient(key, success, cancel string, plans map[string]string, endpoint string) (*Client, error) {
	return newClient(key, success, cancel, plans, endpoint, false)
}
func newClient(key, success, cancel string, plans map[string]string, endpoint string, live bool) (*Client, error) {
	prefix := "sk_test_"
	if live {
		prefix = "sk_live_"
	}
	if !strings.HasPrefix(key, prefix) || len(key) < 16 || strings.ContainsAny(key, " \r\n\t") {
		return nil, errors.New("a Stripe secret key matching the selected mode is required")
	}
	good := func(raw string) *url.URL {
		u, e := url.Parse(raw)
		if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
			return nil
		}
		return u
	}
	a, b := good(success), good(cancel)
	if a == nil || b == nil || a.Host != b.Host {
		return nil, errors.New("fixed HTTPS billing return URLs on one origin are required")
	}
	configured := map[string]string{}
	for plan, price := range plans {
		if plan == "" || len(plan) > 100 || !strings.HasPrefix(price, "price_") || strings.ContainsAny(price, " /\\\r\n") {
			return nil, errors.New("invalid configured hosting plan")
		}
		configured[plan] = price
	}
	if len(configured) == 0 {
		return nil, errors.New("hosting plans are required")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	hc := &http.Client{Transport: transport, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	cfg := &stripe.BackendConfig{HTTPClient: hc, MaxNetworkRetries: stripe.Int64(0), LeveledLogger: &stripe.LeveledLogger{Level: stripe.LevelNull}, EnableTelemetry: stripe.Bool(false)}
	if endpoint != "" {
		cfg.URL = stripe.String(endpoint)
	}
	sdk := stripe.NewClient(key, stripe.WithBackends(stripe.NewBackendsWithConfig(cfg)))
	return &Client{live: live, stripe: sdk, http: hc, success: success, cancel: cancel, plans: configured}, nil
}
func (c *Client) Close() { c.http.CloseIdleConnections() }

// CreateCheckout uses a server-owned customer/plan mapping and a durable opaque
// request ID. Callers must authorize an owner and persist the intent before the
// request, reuse its key after uncertain outcomes, and bind the returned session
// before processing events. Clients never choose amounts, URLs or account scope.
func (c *Client) CreateCheckout(ctx context.Context, customer, plan, requestID string) (Checkout, error) {
	price, ok := c.plans[plan]
	if !ok || !strings.HasPrefix(customer, "cus_") || strings.ContainsAny(customer, " /\\\r\n") || len(requestID) < 16 || len(requestID) > 128 || strings.ContainsAny(requestID, " \r\n") {
		return Checkout{}, errors.New("invalid checkout request")
	}
	params := &stripe.CheckoutSessionCreateParams{Customer: stripe.String(customer), Mode: stripe.String("subscription"), ClientReferenceID: stripe.String(requestID), SuccessURL: stripe.String(c.success), CancelURL: stripe.String(c.cancel), LineItems: []*stripe.CheckoutSessionCreateLineItemParams{{Price: stripe.String(price), Quantity: stripe.Int64(1)}}}
	params.SetIdempotencyKey(requestID)
	session, err := c.stripe.V1CheckoutSessions.Create(ctx, params)
	if err != nil {
		return Checkout{}, errors.New("checkout outcome unavailable; retain request for reconciliation")
	}
	if session == nil {
		return Checkout{}, errors.New("checkout response unavailable")
	}
	u, err := url.Parse(session.URL)
	prefix := "cs_test_"
	if c.live {
		prefix = "cs_live_"
	}
	if session.Livemode != c.live || !strings.HasPrefix(session.ID, prefix) || err != nil || u.Scheme != "https" || u.Host != "checkout.stripe.com" || u.User != nil {
		return Checkout{}, errors.New("invalid checkout mode or identity")
	}
	return Checkout{ID: session.ID, URL: session.URL}, nil
}

// CreateCustomer is called only with a persisted request/email snapshot. It
// creates a Stripe customer in the selected mode, not a subscription or hosting entitlement.
func (c *Client) CreateCustomer(ctx context.Context, email, requestID string) (string, error) {
	if len(email) > 254 || !strings.Contains(email, "@") || strings.ContainsAny(email, "\r\n") || len(requestID) < 16 || len(requestID) > 128 {
		return "", errors.New("invalid customer request")
	}
	params := &stripe.CustomerCreateParams{Email: stripe.String(email), Metadata: map[string]string{"hosting_request": requestID}}
	params.SetIdempotencyKey(requestID)
	customer, err := c.stripe.V1Customers.Create(ctx, params)
	if err != nil {
		return "", errors.New("customer creation outcome unavailable; retain request for reconciliation")
	}
	if customer == nil || customer.Livemode != c.live || !strings.HasPrefix(customer.ID, "cus_") {
		return "", errors.New("invalid customer mode or identity")
	}
	return customer.ID, nil
}

// CreatePinnedCheckout refuses plan changes after the checkout intent was
// persisted, so retries cannot charge a newly configured price by accident.
func (c *Client) CreatePinnedCheckout(ctx context.Context, customer, plan, price, request string) (Checkout, error) {
	if c.plans[plan] != price || price == "" {
		return Checkout{}, errors.New("hosting price changed; checkout requires reconciliation")
	}
	return c.CreateCheckout(ctx, customer, plan, request)
}
