package merchantbilling

import (
	"context"
	"errors"
	"strings"
	"time"

	stripe "github.com/stripe/stripe-go/v86"
)

type RefundRequest struct {
	RequestID       string
	OrderID         string
	PaymentIntentID string
	Currency        string
	AmountMinor     int64
}

type Refund struct {
	ID         string
	State      string
	ObservedAt int64
}

func validRefundID(id string) bool {
	if !strings.HasPrefix(id, "re_") || len(id) <= len("re_") || len(id) > 255 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_') {
			return false
		}
	}
	return true
}

func validRefundRequest(request RefundRequest) bool {
	return validRequestID(request.RequestID) && validRequestID(request.OrderID) && validPaymentIntentID(request.PaymentIntentID) &&
		(request.Currency == "eur" || request.Currency == "usd" || request.Currency == "gbp") && request.AmountMinor >= 50 && request.AmountMinor <= 99999999
}

func (c *Client) normalizeRefund(refund *stripe.Refund, request RefundRequest, expectedID string) (Refund, error) {
	if refund == nil || refund.Object != "refund" || !validRefundID(refund.ID) || (expectedID != "" && refund.ID != expectedID) || refund.Amount != request.AmountMinor || string(refund.Currency) != request.Currency || refund.PaymentIntent == nil || refund.PaymentIntent.ID != request.PaymentIntentID || refund.Metadata["merchant_refund"] != request.RequestID || refund.Metadata["merchant_order"] != request.OrderID || refund.SourceTransferReversal != nil || refund.TransferReversal != nil {
		return Refund{}, errors.New("invalid test refund response")
	}
	state := string(refund.Status)
	switch state {
	case "pending", "requires_action", "succeeded", "failed", "canceled":
	default:
		return Refund{}, errors.New("invalid test refund response")
	}
	return Refund{ID: refund.ID, State: state, ObservedAt: time.Now().Unix()}, nil
}

func (c *Client) CreateRefund(ctx context.Context, accountID string, request RefundRequest) (Refund, error) {
	if !validAccountID(accountID) || !validRefundRequest(request) {
		return Refund{}, errors.New("invalid merchant refund request")
	}
	params := &stripe.RefundCreateParams{
		Amount:        stripe.Int64(request.AmountMinor),
		PaymentIntent: stripe.String(request.PaymentIntentID),
		Metadata: map[string]string{
			"merchant_refund": request.RequestID,
			"merchant_order":  request.OrderID,
		},
	}
	params.SetStripeAccount(accountID)
	params.SetIdempotencyKey("merchant-refund-" + request.RequestID)
	refund, err := c.stripe.V1Refunds.Create(ctx, params)
	if err != nil {
		return Refund{}, errors.New("merchant refund outcome unavailable; retain refund for reconciliation")
	}
	return c.normalizeRefund(refund, request, "")
}

func (c *Client) RetrieveRefund(ctx context.Context, accountID, refundID string, request RefundRequest) (Refund, error) {
	if !validAccountID(accountID) || !validRefundID(refundID) || !validRefundRequest(request) {
		return Refund{}, errors.New("invalid merchant refund request")
	}
	params := &stripe.RefundRetrieveParams{}
	params.SetStripeAccount(accountID)
	refund, err := c.stripe.V1Refunds.Retrieve(ctx, refundID, params)
	if err != nil {
		return Refund{}, errors.New("merchant refund retrieval unavailable; retain refund for reconciliation")
	}
	return c.normalizeRefund(refund, request, refundID)
}
