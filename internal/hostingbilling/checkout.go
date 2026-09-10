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
	stripe          *stripe.Client
	http            *http.Client
	success, cancel string
	plans           map[string]string
}
type Checkout struct {
	ID  string
	URL string
}

func NewTestClient(key, success, cancel string, plans map[string]string) (*Client, error) {
	return newTestClient(key, success, cancel, plans, "")
}
func newTestClient(key, success, cancel string, plans map[string]string, endpoint string) (*Client, error) {
	if !strings.HasPrefix(key, "sk_test_") || len(key) < 16 {
		return nil, errors.New("a Stripe test secret key is required")
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
	return &Client{stripe: sdk, http: hc, success: success, cancel: cancel, plans: configured}, nil
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
	u, err := url.Parse(session.URL)
	if session.Livemode || !strings.HasPrefix(session.ID, "cs_test_") || err != nil || u.Scheme != "https" || u.Host != "checkout.stripe.com" || u.User != nil {
		return Checkout{}, errors.New("invalid test checkout response")
	}
	return Checkout{ID: session.ID, URL: session.URL}, nil
}

// CreateCustomer is called only with a persisted request/email snapshot. It
// creates a Stripe test customer, not a subscription or hosting entitlement.
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
	if customer.Livemode || !strings.HasPrefix(customer.ID, "cus_") {
		return "", errors.New("invalid test customer response")
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
