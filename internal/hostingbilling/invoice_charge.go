package hostingbilling

import (
	"context"
	"errors"

	stripe "github.com/stripe/stripe-go/v86"
)

// DiscoverInvoiceCharge locates a paid invoice's charge without depending on a
// charge webhook. An empty result is not proof of paid access (credit-funded or
// zero-value invoices need separate accounting). Split payments are unsupported.
func (c *Client) DiscoverInvoiceCharge(ctx context.Context, invoice, customer, subscription string) (string, error) {
	if !providerID(invoice, "in_") || !providerID(customer, "cus_") || !providerID(subscription, "sub_") {
		return "", errors.New("invalid invoice discovery identity")
	}
	params := &stripe.InvoicePaymentListParams{Invoice: stripe.String(invoice), Status: stripe.String("paid")}
	params.Limit = stripe.Int64(2)
	params.AddExpand("data.invoice")
	params.AddExpand("data.payment.payment_intent")
	list := c.stripe.V1InvoicePayments.List(ctx, params)
	if list.Err() != nil {
		return "", errors.New("invoice charge discovery unavailable")
	}
	invalid := errors.New("unsupported invoice charge mapping")
	if list.Meta().HasMore || len(list.Data()) > 1 {
		return "", invalid
	}
	if len(list.Data()) == 0 {
		return "", nil
	}
	payment := list.Data()[0]
	if payment == nil || payment.Livemode || payment.Object != "invoice_payment" || payment.Status != "paid" || payment.Invoice == nil || payment.Payment == nil || payment.Payment.Type != "payment_intent" || payment.Payment.PaymentIntent == nil {
		return "", invalid
	}
	inv := payment.Invoice
	intent := payment.Payment.PaymentIntent
	if inv.ID != invoice || inv.Object != "invoice" || inv.Livemode || inv.Customer == nil || inv.Customer.ID != customer || inv.CustomerAccount != "" || inv.Parent == nil || inv.Parent.Type != "subscription_details" || inv.Parent.SubscriptionDetails == nil || inv.Parent.SubscriptionDetails.Subscription == nil || inv.Parent.SubscriptionDetails.Subscription.ID != subscription {
		return "", invalid
	}
	if !providerID(intent.ID, "pi_") || intent.Object != "payment_intent" || intent.Livemode || intent.Customer == nil || intent.Customer.ID != customer || intent.CustomerAccount != "" || intent.Status != "succeeded" || intent.LatestCharge == nil || !providerID(intent.LatestCharge.ID, "ch_") || payment.AmountPaid <= 0 || payment.AmountPaid != intent.AmountReceived || payment.Currency != intent.Currency || payment.Currency != inv.Currency {
		return "", invalid
	}
	return intent.LatestCharge.ID, nil
}
