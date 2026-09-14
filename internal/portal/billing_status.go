package portal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/hostingbilling"
)

type BillingStatus struct {
	CustomerState string                    `json:"customer_state"`
	Checkout      *BillingCheckout          `json:"checkout"`
	Subscriptions []BillingSubscriptionView `json:"subscriptions"`
}

// BillingSubscriptionView is display evidence, never a hosting entitlement.
// Provider customer, price, invoice and payment identifiers stay private.
type BillingSubscriptionView struct {
	ID                string `json:"id"`
	CheckoutID        string `json:"checkout_id"`
	Plan              string `json:"plan"`
	State             string `json:"state"`
	PeriodEnd         int64  `json:"period_end"`
	CancelAtPeriodEnd bool   `json:"cancel_at_period_end"`
	CollectionPaused  bool   `json:"collection_paused"`
	InvoiceStatus     string `json:"invoice_status"`
	ObservedAt        int64  `json:"observed_at"`
	Stale             bool   `json:"stale"`
}

func (s *Store) WorkspaceBillingStatus(ctx context.Context, token, workspace string) (BillingStatus, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BillingStatus{}, err
	}
	defer tx.Rollback()
	if _, err = s.authorizeOwner(ctx, tx, token, workspace); err != nil {
		return BillingStatus{}, err
	}
	status := BillingStatus{CustomerState: "not_started", Subscriptions: []BillingSubscriptionView{}}
	var customer string
	err = tx.QueryRowContext(ctx, "SELECT COALESCE(customer_id,'') FROM billing_customers WHERE workspace_id=?", workspace).Scan(&customer)
	if err == nil {
		status.CustomerState = "pending"
		if customer != "" {
			status.CustomerState = "ready"
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return status, err
	}
	checkout, err := scanCheckout(tx.QueryRowContext(ctx, "SELECT "+checkoutColumns+" FROM billing_checkouts WHERE workspace_id=? AND state IN ('pending','open','completed')", workspace))
	if err == nil {
		status.Checkout = &checkout
	} else if !errors.Is(err, sql.ErrNoRows) {
		return status, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,checkout_id,plan_id,customer_id,price_id,snapshot FROM billing_subscriptions WHERE workspace_id=? ORDER BY id`, workspace)
	if err != nil {
		return status, err
	}
	defer rows.Close()
	now := s.now().Unix()
	for rows.Next() {
		view := BillingSubscriptionView{State: "pending", Stale: true}
		var customer, price string
		var data []byte
		if err = rows.Scan(&view.ID, &view.CheckoutID, &view.Plan, &customer, &price, &data); err != nil {
			return status, err
		}
		if data != nil {
			var snapshot hostingbilling.SubscriptionSnapshot
			if err = json.Unmarshal(data, &snapshot); err != nil {
				return status, err
			}
			if snapshot.ID != view.ID || snapshot.CustomerID != customer || snapshot.PriceID != price {
				return status, ErrBillingConflict
			}
			view.State = snapshot.Status
			view.PeriodEnd = snapshot.PeriodEnd
			view.CancelAtPeriodEnd = snapshot.CancelAtPeriodEnd
			view.CollectionPaused = snapshot.CollectionPaused
			view.InvoiceStatus = snapshot.InvoiceStatus
			view.ObservedAt = snapshot.ObservedAt
			view.Stale = snapshot.ObservedAt <= 0 || snapshot.ObservedAt > now || now-snapshot.ObservedAt >= 300
		}
		status.Subscriptions = append(status.Subscriptions, view)
	}
	if err = rows.Err(); err != nil {
		return status, err
	}
	if err = rows.Close(); err != nil {
		return status, err
	}
	return status, tx.Commit()
}
