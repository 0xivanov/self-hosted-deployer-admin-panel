package hostingbilling

import (
	"context"
	"errors"
	"strings"
	"time"

	stripe "github.com/stripe/stripe-go/v86"
)

type SubscriptionSnapshot struct {
	ID                string
	CustomerID        string
	PriceID           string
	Status            string
	PeriodStart       int64
	PeriodEnd         int64
	CancelAtPeriodEnd bool
	CollectionPaused  bool
	InvoiceID         string
	InvoiceStatus     string
	InvoiceRemaining  int64
	ObservedAt        int64
}

func providerID(id, prefix string) bool {
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

// RetrieveSubscription reads current provider state in the configured mode with the latest invoice
// expanded. It checks the persisted customer/price mapping and supported single
// quantity-one hosting item before returning a snapshot. This is not a hosting
// entitlement decision: refunds, disputes, grace periods and freshness policy
// must be reconciled separately before applying access changes.
func (c *Client) RetrieveSubscription(ctx context.Context, id, customer, price string) (SubscriptionSnapshot, error) {
	if !providerID(id, "sub_") || !providerID(customer, "cus_") || !providerID(price, "price_") {
		return SubscriptionSnapshot{}, errors.New("invalid subscription lookup")
	}
	params := &stripe.SubscriptionRetrieveParams{}
	params.AddExpand("latest_invoice")
	observed := time.Now().Unix()
	subscription, err := c.stripe.V1Subscriptions.Retrieve(ctx, id, params)
	if err != nil {
		return SubscriptionSnapshot{}, errors.New("subscription state unavailable; retry reconciliation")
	}
	return normalizeSubscriptionForMode(subscription, id, customer, price, observed, c.live)
}
func normalizeSubscription(s *stripe.Subscription, id, customer, price string, observed int64) (SubscriptionSnapshot, error) {
	return normalizeSubscriptionForMode(s, id, customer, price, observed, false)
}
func normalizeSubscriptionForMode(s *stripe.Subscription, id, customer, price string, observed int64, live bool) (SubscriptionSnapshot, error) {
	invalid := errors.New("subscription state does not match hosting billing identity")
	if s == nil || s.Livemode != live || s.ID != id || s.Customer == nil || s.Customer.ID != customer || s.CustomerAccount != "" || s.Items == nil || s.Items.HasMore || len(s.Items.Data) != 1 {
		return SubscriptionSnapshot{}, invalid
	}
	item := s.Items.Data[0]
	if item == nil || item.Price == nil || item.Price.ID != price || item.Price.Livemode != live || item.Quantity != 1 || item.CurrentPeriodStart <= 0 || item.CurrentPeriodEnd <= item.CurrentPeriodStart {
		return SubscriptionSnapshot{}, invalid
	}
	switch s.Status {
	case "active", "trialing", "past_due", "unpaid", "canceled", "incomplete", "incomplete_expired", "paused":
	default:
		return SubscriptionSnapshot{}, invalid
	}
	result := SubscriptionSnapshot{ID: id, CustomerID: customer, PriceID: price, Status: string(s.Status), PeriodStart: item.CurrentPeriodStart, PeriodEnd: item.CurrentPeriodEnd, CancelAtPeriodEnd: s.CancelAtPeriodEnd, CollectionPaused: s.PauseCollection != nil, ObservedAt: observed}
	if invoice := s.LatestInvoice; invoice != nil {
		if !providerID(invoice.ID, "in_") || invoice.Livemode != live || invoice.Customer == nil || invoice.Customer.ID != customer || invoice.Parent == nil || invoice.Parent.Type != "subscription_details" || invoice.Parent.SubscriptionDetails == nil || invoice.Parent.SubscriptionDetails.Subscription == nil || invoice.Parent.SubscriptionDetails.Subscription.ID != id {
			return SubscriptionSnapshot{}, invalid
		}
		switch invoice.Status {
		case "draft", "open", "paid", "uncollectible", "void":
		default:
			return SubscriptionSnapshot{}, invalid
		}
		result.InvoiceID = invoice.ID
		result.InvoiceStatus = string(invoice.Status)
		result.InvoiceRemaining = invoice.AmountRemaining
	}
	return result, nil
}
