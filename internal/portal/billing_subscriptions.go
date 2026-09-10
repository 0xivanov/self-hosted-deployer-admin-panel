package portal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/hostingbilling"
)

// SubscriptionReader supplies authenticated provider state, never browser input.
type SubscriptionReader interface {
	RetrieveSubscription(context.Context, string, string, string) (hostingbilling.SubscriptionSnapshot, error)
}

type InvoiceChargeFinder interface {
	DiscoverInvoiceCharge(context.Context, string, string, string) (string, error)
}

// ReconcileBillingSubscription fences each provider read with a durable generation.
// A later-started read invalidates earlier work even if the later read fails.
// Stored snapshots are billing evidence, not paid hosting entitlements.
func (s *Store) ReconcileBillingSubscription(ctx context.Context, id string, reader SubscriptionReader) error {
	if reader == nil {
		return ErrBillingConflict
	}
	var customer, price string
	var generation int64
	err := s.db.QueryRowContext(ctx, `UPDATE billing_subscriptions SET reconciliation_generation=reconciliation_generation+1 WHERE id=? RETURNING customer_id,price_id,reconciliation_generation`, id).Scan(&customer, &price, &generation)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDenied
	}
	if err != nil {
		return err
	}
	started := s.now().Unix()
	snapshot, err := reader.RetrieveSubscription(ctx, id, customer, price)
	if err != nil {
		return err
	}
	if snapshot.ID != id || snapshot.CustomerID != customer || snapshot.PriceID != price || snapshot.ObservedAt < started || snapshot.ObservedAt > s.now().Unix() {
		return ErrBillingConflict
	}
	chargeID := ""
	if finder, ok := reader.(InvoiceChargeFinder); ok && snapshot.InvoiceID != "" {
		chargeID, err = finder.DiscoverInvoiceCharge(ctx, snapshot.InvoiceID, customer, id)
		if err != nil {
			return err
		}
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE billing_subscriptions SET snapshot=? WHERE id=? AND reconciliation_generation=?`, data, id, generation)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrBillingConflict
	}
	if chargeID != "" {
		if len(chargeID) < 4 || len(chargeID) > 255 || !strings.HasPrefix(chargeID, "ch_") || strings.ContainsAny(chargeID, " /\\\r\n") {
			return ErrBillingConflict
		}
		if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO billing_charges(id) VALUES(?)", chargeID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// BillingSubscriptionSnapshot is owner-only and may be nil before reconciliation.
// Its observed time must be considered by any future access policy.
func (s *Store) BillingSubscriptionSnapshot(ctx context.Context, token, workspace, id string) (*hostingbilling.SubscriptionSnapshot, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = s.authorizeOwner(ctx, tx, token, workspace); err != nil {
		return nil, err
	}
	var data []byte
	err = tx.QueryRowContext(ctx, `SELECT snapshot FROM billing_subscriptions WHERE id=? AND workspace_id=?`, id, workspace).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrDenied
	}
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, tx.Commit()
	}
	var snapshot hostingbilling.SubscriptionSnapshot
	if err = json.Unmarshal(data, &snapshot); err != nil {
		return nil, err
	}
	return &snapshot, tx.Commit()
}
