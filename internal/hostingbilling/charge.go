package hostingbilling

import (
	"context"
	"errors"
	"time"

	stripe "github.com/stripe/stripe-go/v86"
)

// ChargeObservation links payment risk to one verified hosting invoice. It does
// not decide entitlement or issue a refund. Split invoice allocations require a
// separate accounting policy and are rejected rather than guessed.
type ChargeDispute struct {
	ID     string
	Status string
	Amount int64
}

type ChargeObservation struct {
	Disputes                                                         []ChargeDispute
	DisputesChecked                                                  bool
	ChargeID, CustomerID, PaymentIntentID, InvoiceID, SubscriptionID string
	Currency                                                         string
	AmountCaptured, AmountRefunded                                   int64
	Disputed                                                         bool
	ObservedAt                                                       int64
}

func (c *Client) RetrieveChargeObservation(ctx context.Context, id string) (ChargeObservation, error) {
	if !providerID(id, "ch_") {
		return ChargeObservation{}, errors.New("invalid charge lookup")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	observed := time.Now().Unix()
	charge, err := c.stripe.V1Charges.Retrieve(ctx, id, &stripe.ChargeRetrieveParams{})
	if err != nil {
		return ChargeObservation{}, errors.New("charge state unavailable")
	}
	invalid := errors.New("unsupported hosting charge identity")
	if charge == nil || charge.ID != id || charge.Object != "charge" || charge.Livemode || charge.Customer == nil || !providerID(charge.Customer.ID, "cus_") || charge.PaymentIntent == nil || !providerID(charge.PaymentIntent.ID, "pi_") || charge.Application != nil || charge.ApplicationFee != nil || charge.OnBehalfOf != nil || charge.SourceTransfer != nil || charge.TransferData != nil || !charge.Paid || !charge.Captured || charge.Status != "succeeded" || charge.AmountCaptured <= 0 || charge.AmountCaptured > charge.Amount || charge.AmountRefunded < 0 || charge.AmountRefunded > charge.AmountCaptured {
		return ChargeObservation{}, invalid
	}
	params := &stripe.InvoicePaymentListParams{Payment: &stripe.InvoicePaymentListPaymentParams{Type: stripe.String("payment_intent"), PaymentIntent: stripe.String(charge.PaymentIntent.ID)}}
	params.Limit = stripe.Int64(2)
	params.AddExpand("data.invoice")
	payments := c.stripe.V1InvoicePayments.List(ctx, params)
	if payments.Err() != nil {
		return ChargeObservation{}, errors.New("invoice payment mapping unavailable")
	}
	if payments.Meta().HasMore || len(payments.Data()) != 1 {
		return ChargeObservation{}, invalid
	}
	match := payments.Data()[0]
	if match == nil || match.Livemode || match.Object != "invoice_payment" || match.Status != "paid" || match.AmountPaid != charge.AmountCaptured || match.Currency != charge.Currency || match.Payment == nil || match.Payment.Type != "payment_intent" || match.Payment.PaymentIntent == nil || match.Payment.PaymentIntent.ID != charge.PaymentIntent.ID || match.Invoice == nil {
		return ChargeObservation{}, invalid
	}
	invoice := match.Invoice
	if !providerID(invoice.ID, "in_") || invoice.Object != "invoice" || invoice.Livemode || invoice.Customer == nil || invoice.Customer.ID != charge.Customer.ID || invoice.CustomerAccount != "" || invoice.Currency != charge.Currency || invoice.Parent == nil || invoice.Parent.Type != "subscription_details" || invoice.Parent.SubscriptionDetails == nil || invoice.Parent.SubscriptionDetails.Subscription == nil || !providerID(invoice.Parent.SubscriptionDetails.Subscription.ID, "sub_") {
		return ChargeObservation{}, invalid
	}
	disputes, err := c.retrieveChargeDisputes(ctx, charge)
	if err != nil {
		return ChargeObservation{}, err
	}
	return ChargeObservation{Disputes: disputes, DisputesChecked: true, ChargeID: id, CustomerID: charge.Customer.ID, PaymentIntentID: charge.PaymentIntent.ID, InvoiceID: invoice.ID, SubscriptionID: invoice.Parent.SubscriptionDetails.Subscription.ID, Currency: string(charge.Currency), AmountCaptured: charge.AmountCaptured, AmountRefunded: charge.AmountRefunded, Disputed: charge.Disputed, ObservedAt: observed}, nil
}

func (c *Client) retrieveChargeDisputes(ctx context.Context, charge *stripe.Charge) ([]ChargeDispute, error) {
	params := &stripe.DisputeListParams{Charge: stripe.String(charge.ID)}
	params.Limit = stripe.Int64(100)
	list := c.stripe.V1Disputes.List(ctx, params)
	if list.Err() != nil {
		return nil, errors.New("dispute state unavailable")
	}
	invalid := errors.New("incomplete or mismatched dispute state")
	if list.Meta().HasMore || (charge.Disputed && len(list.Data()) == 0) {
		return nil, invalid
	}
	result := []ChargeDispute{}
	seen := map[string]bool{}
	for _, d := range list.Data() {
		if d == nil || !providerID(d.ID, "dp_") || seen[d.ID] || d.Object != "dispute" || d.Livemode || d.Charge == nil || d.Charge.ID != charge.ID || d.PaymentIntent == nil || d.PaymentIntent.ID != charge.PaymentIntent.ID || d.Currency != charge.Currency || d.Amount <= 0 {
			return nil, invalid
		}
		switch d.Status {
		case "warning_needs_response", "warning_under_review", "warning_closed", "needs_response", "under_review", "won", "lost", "prevented":
		default:
			return nil, invalid
		}
		seen[d.ID] = true
		result = append(result, ChargeDispute{ID: d.ID, Status: string(d.Status), Amount: d.Amount})
	}
	return result, nil
}
