// Package domainbilling contains the sandbox Stripe checkout adapter for domain purchases.
package domainbilling

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/domains"
	stripe "github.com/stripe/stripe-go/v86"
)

const fixedStripeEndpoint = "https://api.stripe.com"

type Order struct {
	ID          string
	Domain      string
	Currency    string
	AmountMinor int64
}

type Checkout struct {
	ID   string
	URL  string
	Paid bool
}

type Client struct {
	stripe  *stripe.Client
	http    *http.Client
	success string
	cancel  string
}

func NewTestClient(key, success, cancel string) (*Client, error) {
	return newClient(key, success, cancel, fixedStripeEndpoint)
}

func newClient(key, success, cancel, endpoint string) (*Client, error) {
	if !strings.HasPrefix(key, "sk_test_") || len(key) < 16 || strings.ContainsAny(key, " \r\n\t") {
		return nil, errors.New("a Stripe sandbox secret key is required")
	}
	successURL, cancelURL := parseReturnURL(success), parseReturnURL(cancel)
	if successURL == nil || cancelURL == nil || successURL.Host != cancelURL.Host {
		return nil, errors.New("fixed HTTPS billing return URLs are required")
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	hc := &http.Client{
		Transport: transport,
		Timeout:   20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	cfg := &stripe.BackendConfig{
		HTTPClient:        hc,
		MaxNetworkRetries: stripe.Int64(0),
		LeveledLogger:     &stripe.LeveledLogger{Level: stripe.LevelNull},
		EnableTelemetry:   stripe.Bool(false),
	}
	if endpoint != "" {
		cfg.URL = stripe.String(endpoint)
	}
	return &Client{
		stripe:  stripe.NewClient(key, stripe.WithBackends(stripe.NewBackendsWithConfig(cfg))),
		http:    hc,
		success: successURL.String(),
		cancel:  cancelURL.String(),
	}, nil
}

func (c *Client) Close() {
	if c != nil && c.http != nil {
		c.http.CloseIdleConnections()
	}
}

func (c *Client) CreateCheckout(ctx context.Context, order Order) (Checkout, error) {
	if !validOrder(order) {
		return Checkout{}, errors.New("invalid domain checkout order")
	}
	params := &stripe.CheckoutSessionCreateParams{
		Mode:                stripe.String("payment"),
		PaymentMethodTypes:  []*string{stripe.String("card")},
		AdaptivePricing:     &stripe.CheckoutSessionCreateAdaptivePricingParams{Enabled: stripe.Bool(false)},
		AutomaticTax:        &stripe.CheckoutSessionCreateAutomaticTaxParams{Enabled: stripe.Bool(false)},
		AllowPromotionCodes: stripe.Bool(false),
		SuccessURL:          stripe.String(c.success),
		CancelURL:           stripe.String(c.cancel),
		ClientReferenceID:   stripe.String(order.ID),
		Metadata:            map[string]string{"domain_order": order.ID, "domain": order.Domain},
		LineItems: []*stripe.CheckoutSessionCreateLineItemParams{{
			Quantity: stripe.Int64(1),
			PriceData: &stripe.CheckoutSessionCreateLineItemPriceDataParams{
				Currency:    stripe.String(order.Currency),
				UnitAmount:  stripe.Int64(order.AmountMinor),
				TaxBehavior: stripe.String("inclusive"),
				ProductData: &stripe.CheckoutSessionCreateLineItemPriceDataProductDataParams{Name: stripe.String(order.Domain)},
			},
		}},
		PaymentIntentData: &stripe.CheckoutSessionCreatePaymentIntentDataParams{
			Metadata: map[string]string{"domain_order": order.ID, "domain": order.Domain},
		},
	}
	params.SetIdempotencyKey("domain-checkout-" + order.ID)
	session, err := c.stripe.V1CheckoutSessions.Create(ctx, params)
	if err != nil {
		return Checkout{}, errors.New("domain checkout outcome unavailable; retain order for reconciliation")
	}
	return c.normalize(session, order, "")
}

func (c *Client) ReadCheckout(ctx context.Context, sessionID string, order Order) (Checkout, error) {
	if !validOrder(order) || !validSessionID(sessionID) {
		return Checkout{}, errors.New("invalid domain checkout request")
	}
	session, err := c.stripe.V1CheckoutSessions.Retrieve(ctx, sessionID, &stripe.CheckoutSessionRetrieveParams{})
	if err != nil {
		return Checkout{}, errors.New("domain checkout retrieval unavailable; retain order for reconciliation")
	}
	return c.normalize(session, order, sessionID)
}

func (c *Client) normalize(session *stripe.CheckoutSession, order Order, expectedID string) (Checkout, error) {
	if session == nil || session.Object != "checkout.session" || !validSessionID(session.ID) || (expectedID != "" && session.ID != expectedID) || session.Livemode || session.Mode != stripe.CheckoutSessionModePayment || session.ClientReferenceID != order.ID || session.Metadata["domain_order"] != order.ID || session.Metadata["domain"] != order.Domain || session.Currency != stripe.Currency(order.Currency) || session.AmountSubtotal != order.AmountMinor || session.AmountTotal != order.AmountMinor || session.AllowPromotionCodes || session.Customer != nil || session.Subscription != nil || session.Invoice != nil || session.PaymentLink != nil {
		return Checkout{}, errors.New("invalid domain checkout response")
	}
	if session.AdaptivePricing != nil && session.AdaptivePricing.Enabled || session.AutomaticTax != nil && session.AutomaticTax.Enabled {
		return Checkout{}, errors.New("invalid domain checkout response")
	}
	if len(session.PaymentMethodTypes) != 1 || session.PaymentMethodTypes[0] != "card" {
		return Checkout{}, errors.New("invalid domain checkout response")
	}
	if session.TotalDetails != nil && (session.TotalDetails.AmountDiscount != 0 || session.TotalDetails.AmountShipping != 0 || session.TotalDetails.AmountTax != 0) {
		return Checkout{}, errors.New("invalid domain checkout response")
	}
	state := string(session.Status)
	if state != "open" && state != "complete" && state != "expired" {
		return Checkout{}, errors.New("invalid domain checkout response")
	}
	paymentStatus := string(session.PaymentStatus)
	if paymentStatus != "unpaid" && paymentStatus != "paid" {
		return Checkout{}, errors.New("invalid domain checkout response")
	}
	if paymentStatus == "paid" && state != "complete" {
		return Checkout{}, errors.New("invalid domain checkout response")
	}
	if session.URL != "" && !validCheckoutURL(session.URL) {
		return Checkout{}, errors.New("invalid domain checkout response")
	}
	if state == "open" && session.URL == "" {
		return Checkout{}, errors.New("invalid domain checkout response")
	}
	return Checkout{ID: session.ID, URL: session.URL, Paid: paymentStatus == "paid" && state == "complete"}, nil
}

func validOrder(order Order) bool {
	if !validOpaqueID(order.ID) || order.Currency != "usd" && order.Currency != "eur" || order.AmountMinor < 50 || order.AmountMinor > 99999999 {
		return false
	}
	name, err := domains.PurchaseName(order.Domain)
	return err == nil && name == order.Domain
}

func validOpaqueID(id string) bool {
	if len(id) == 0 || len(id) > 255 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func validSessionID(id string) bool {
	return strings.HasPrefix(id, "cs_test_") && validOpaqueID(id[len("cs_test_"):])
}

func parseReturnURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || (u.RawQuery != "" && u.RawQuery != "domain_checkout=return") || u.ForceQuery || u.Fragment != "" {
		return nil
	}
	return u
}

func validCheckoutURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host == "checkout.stripe.com" && u.User == nil && u.Hostname() == "checkout.stripe.com" && raw != ""
}
