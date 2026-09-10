package portal

import (
	"context"
	"database/sql"
	"errors"
)

type BillingStatus struct {
	CustomerState string           `json:"customer_state"`
	Checkout      *BillingCheckout `json:"checkout"`
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
	status := BillingStatus{CustomerState: "not_started"}
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
	return status, tx.Commit()
}
