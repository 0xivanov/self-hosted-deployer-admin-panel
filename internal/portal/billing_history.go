package portal

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/hostingbilling"
)

// BillingPaymentView presents reconciled test-payment evidence, not an account
// balance or a promise of refund settlement. ObservedAt is not the payment date.
type BillingPaymentView struct {
	ID              string   `json:"id"`
	Plan            string   `json:"plan"`
	Currency        string   `json:"currency"`
	AmountCaptured  int64    `json:"amount_captured"`
	AmountRefunded  int64    `json:"amount_refunded"`
	Disputed        bool     `json:"disputed"`
	DisputesChecked bool     `json:"disputes_checked"`
	DisputeStatuses []string `json:"dispute_statuses"`
	ObservedAt      int64    `json:"observed_at"`
	Stale           bool     `json:"stale"`
	Pending         bool     `json:"pending"`
}

type BillingPaymentPage struct {
	Payments   []BillingPaymentView `json:"payments"`
	NextCursor string               `json:"next_cursor"`
}

// WorkspaceBillingPayments uses a stable ID cursor rather than the observation
// time, which changes whenever a payment is refreshed. Every page authorizes the
// owner and scopes its join to that workspace in the same transaction.
func (s *Store) WorkspaceBillingPayments(ctx context.Context, token, workspace, before string) (BillingPaymentPage, error) {
	page := BillingPaymentPage{Payments: []BillingPaymentView{}}
	if before != "" && (!strings.HasPrefix(before, "ch_") || len(before) < 4 || len(before) > 255 || strings.IndexFunc(before, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_')
	}) >= 0) {
		return page, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	mode := s.billingModeValue()
	if _, err = s.authorizeOwner(ctx, tx, token, workspace); err != nil {
		return page, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT c.id,s.plan_id,c.customer_id,c.subscription_id,c.invoice_id,c.payment_intent_id,c.snapshot FROM billing_charges c JOIN billing_subscriptions s ON s.mode=c.mode AND s.id=c.subscription_id AND s.customer_id=c.customer_id WHERE c.mode=? AND s.workspace_id=? AND (?='' OR c.id<?) ORDER BY c.id DESC LIMIT 21`, mode, workspace, before, before)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	now := s.now().Unix()
	for rows.Next() {
		if len(page.Payments) == 20 {
			page.NextCursor = page.Payments[19].ID
			break
		}
		view := BillingPaymentView{Pending: true, Stale: true, DisputeStatuses: []string{}}
		var customer, subscription, invoice, payment string
		var data []byte
		if err = rows.Scan(&view.ID, &view.Plan, &customer, &subscription, &invoice, &payment, &data); err != nil {
			return page, err
		}
		if data != nil {
			var observation hostingbilling.ChargeObservation
			if err = json.Unmarshal(data, &observation); err != nil {
				return page, err
			}
			if observation.ChargeID != view.ID || observation.CustomerID != customer || observation.SubscriptionID != subscription || observation.InvoiceID != invoice || observation.PaymentIntentID != payment {
				return page, ErrBillingConflict
			}
			view.Pending = false
			view.Currency = observation.Currency
			view.AmountCaptured = observation.AmountCaptured
			view.AmountRefunded = observation.AmountRefunded
			view.Disputed = observation.Disputed
			view.DisputesChecked = observation.DisputesChecked
			for _, dispute := range observation.Disputes {
				view.DisputeStatuses = append(view.DisputeStatuses, dispute.Status)
			}
			view.ObservedAt = observation.ObservedAt
			view.Stale = observation.ObservedAt <= 0 || observation.ObservedAt > now || now-observation.ObservedAt >= 300
		}
		page.Payments = append(page.Payments, view)
	}
	if err = rows.Err(); err != nil {
		return page, err
	}
	if err = rows.Close(); err != nil {
		return page, err
	}
	return page, tx.Commit()
}
