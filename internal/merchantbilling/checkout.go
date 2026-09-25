package merchantbilling

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	stripe "github.com/stripe/stripe-go/v86"
)

type CheckoutOrder struct {
	RequestID   string
	Name        string
	Currency    string
	AmountMinor int64
}

type Checkout struct {
	ID              string
	URL             string
	State           string
	PaymentStatus   string
	PaymentIntentID string
	ObservedAt      int64
}

func validSessionID(id string) bool {
	return validSessionIDForMode(id, false)
}

func validSessionIDForMode(id string, live bool) bool {
	prefix := "cs_test_"
	if live {
		prefix = "cs_live_"
	}
	if !strings.HasPrefix(id, prefix) || len(id) <= len(prefix) || len(id) > 255 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_') {
			return false
		}
	}
	return true
}

func validPaymentIntentID(id string) bool {
	if !strings.HasPrefix(id, "pi_") || len(id) <= len("pi_") || len(id) > 255 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_') {
			return false
		}
	}
	return true
}

func validCheckoutOrder(order CheckoutOrder) bool {
	name := strings.TrimSpace(order.Name)
	if name != order.Name || !utf8.ValidString(name) || len(name) < 1 || len(name) > 120 {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	if !validRequestID(order.RequestID) || (order.Currency != "eur" && order.Currency != "usd" && order.Currency != "gbp") || order.AmountMinor < 50 || order.AmountMinor > 99999999 {
		return false
	}
	return true
}

func (c *Client) checkoutURLs() (string, string, error) {
	u, err := url.Parse(c.returnURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", "", errors.New("invalid fixed merchant return URL")
	}
	base := &url.URL{Scheme: u.Scheme, Host: u.Host}
	return (&url.URL{Scheme: base.Scheme, Host: base.Host, Path: "/merchant/sales/success"}).String(), (&url.URL{Scheme: base.Scheme, Host: base.Host, Path: "/merchant/sales/cancel"}).String(), nil
}

func (c *Client) normalizeCheckout(session *stripe.CheckoutSession, order CheckoutOrder, expectedID string) (Checkout, error) {
	success, cancel, err := c.checkoutURLs()
	if err != nil {
		return Checkout{}, err
	}
	if session != nil && (session.SuccessURL != success || session.CancelURL != cancel) {
		return Checkout{}, errors.New("invalid checkout return URLs")
	}

	if session == nil || session.Object != "checkout.session" || !validSessionIDForMode(session.ID, c.live) || session.Livemode != c.live || (expectedID != "" && session.ID != expectedID) || session.Mode != stripe.CheckoutSessionModePayment || session.ClientReferenceID != order.RequestID || session.Metadata["merchant_order"] != order.RequestID || session.Currency != stripe.Currency(order.Currency) || session.AmountSubtotal != order.AmountMinor || session.AmountTotal != order.AmountMinor || session.AllowPromotionCodes || session.Customer != nil || session.CustomerAccount != "" || session.Subscription != nil || session.PaymentLink != nil || session.Invoice != nil {
		return Checkout{}, errors.New("invalid checkout response")
	}
	if session.AdaptivePricing != nil && session.AdaptivePricing.Enabled {
		return Checkout{}, errors.New("invalid checkout response")
	}
	if session.AutomaticTax != nil && session.AutomaticTax.Enabled {
		return Checkout{}, errors.New("invalid checkout response")
	}
	if len(session.PaymentMethodTypes) != 1 || session.PaymentMethodTypes[0] != "card" {
		return Checkout{}, errors.New("invalid checkout response")
	}
	if session.TotalDetails != nil && (session.TotalDetails.AmountDiscount != 0 || session.TotalDetails.AmountShipping != 0 || session.TotalDetails.AmountTax != 0) {
		return Checkout{}, errors.New("invalid checkout response")
	}
	state := string(session.Status)
	if state != "open" && state != "complete" && state != "expired" {
		return Checkout{}, errors.New("invalid checkout response")
	}
	paymentStatus := string(session.PaymentStatus)
	if paymentStatus != "unpaid" && paymentStatus != "paid" {
		return Checkout{}, errors.New("invalid checkout response")
	}
	paymentIntentID := ""
	if session.PaymentIntent != nil {
		if session.PaymentIntent.Object != "" && (session.PaymentIntent.Object != "payment_intent" || session.PaymentIntent.Livemode != c.live) {
			return Checkout{}, errors.New("checkout payment intent mode mismatch")
		}
		paymentIntentID = session.PaymentIntent.ID
	}
	if paymentStatus == "paid" {
		if state != "complete" || !validPaymentIntentID(paymentIntentID) {
			return Checkout{}, errors.New("invalid checkout response")
		}
	} else if paymentIntentID != "" && !validPaymentIntentID(paymentIntentID) {
		return Checkout{}, errors.New("invalid checkout response")
	}
	if state == "open" {
		u, err := url.Parse(session.URL)
		if err != nil || u.Scheme != "https" || u.Host != "checkout.stripe.com" || u.User != nil || session.URL == "" {
			return Checkout{}, errors.New("invalid checkout response")
		}
	} else if session.URL != "" {
		u, err := url.Parse(session.URL)
		if err != nil || u.Scheme != "https" || u.Host != "checkout.stripe.com" || u.User != nil {
			return Checkout{}, errors.New("invalid checkout response")
		}
	}
	return Checkout{ID: session.ID, URL: session.URL, State: state, PaymentStatus: paymentStatus, PaymentIntentID: paymentIntentID, ObservedAt: time.Now().Unix()}, nil
}

// CreateCheckout requires a persisted order price and a previously authorized,
// ready merchant account. Browsers must not supply account IDs or final prices.
// Unknown outcomes retain this order identity; callers must reconcile before
// creating another checkout, including after idempotency retention expires.
func (c *Client) CreateCheckout(ctx context.Context, accountID string, order CheckoutOrder) (Checkout, error) {
	if !validAccountID(accountID) || !validCheckoutOrder(order) {
		return Checkout{}, errors.New("invalid merchant checkout request")
	}
	successURL, cancelURL, err := c.checkoutURLs()
	if err != nil {
		return Checkout{}, err
	}
	name := strings.TrimSpace(order.Name)
	params := &stripe.CheckoutSessionCreateParams{
		Mode:                stripe.String("payment"),
		PaymentMethodTypes:  []*string{stripe.String("card")},
		AdaptivePricing:     &stripe.CheckoutSessionCreateAdaptivePricingParams{Enabled: stripe.Bool(false)},
		AutomaticTax:        &stripe.CheckoutSessionCreateAutomaticTaxParams{Enabled: stripe.Bool(false)},
		AllowPromotionCodes: stripe.Bool(false),
		SuccessURL:          stripe.String(successURL),
		CancelURL:           stripe.String(cancelURL),
		ClientReferenceID:   stripe.String(order.RequestID),
		Metadata:            map[string]string{"merchant_order": order.RequestID},
		LineItems:           []*stripe.CheckoutSessionCreateLineItemParams{{Quantity: stripe.Int64(1), PriceData: &stripe.CheckoutSessionCreateLineItemPriceDataParams{Currency: stripe.String(order.Currency), UnitAmount: stripe.Int64(order.AmountMinor), TaxBehavior: stripe.String("inclusive"), ProductData: &stripe.CheckoutSessionCreateLineItemPriceDataProductDataParams{Name: stripe.String(name)}}}},
		PaymentIntentData:   &stripe.CheckoutSessionCreatePaymentIntentDataParams{Metadata: map[string]string{"merchant_order": order.RequestID}},
	}
	params.SetStripeAccount(accountID)
	params.SetIdempotencyKey("merchant-checkout-" + order.RequestID)
	session, err := c.stripe.V1CheckoutSessions.Create(ctx, params)
	if err != nil {
		return Checkout{}, errors.New("merchant checkout outcome unavailable; retain order for reconciliation")
	}
	return c.normalizeCheckout(session, order, "")
}

// RetrieveCheckout always reads in the bound connected-account scope. Its result
// is provider evidence; return URLs never establish payment or fulfillment.
func (c *Client) RetrieveCheckout(ctx context.Context, accountID, sessionID string, order CheckoutOrder) (Checkout, error) {
	if !validAccountID(accountID) || !validSessionIDForMode(sessionID, c.live) || !validCheckoutOrder(order) {
		return Checkout{}, errors.New("invalid merchant checkout request")
	}
	params := &stripe.CheckoutSessionRetrieveParams{}
	params.SetStripeAccount(accountID)
	session, err := c.stripe.V1CheckoutSessions.Retrieve(ctx, sessionID, params)
	if err != nil {
		return Checkout{}, errors.New("merchant checkout retrieval unavailable; retain order for reconciliation")
	}
	return c.normalizeCheckout(session, order, sessionID)
}
