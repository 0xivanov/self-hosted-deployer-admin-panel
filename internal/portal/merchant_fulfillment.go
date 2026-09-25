package portal

import (
	"context"
	"database/sql"
	"errors"
)

// FulfillMerchantOrder records an owner's delivery attestation. It does not ship
// goods, send messages or grant access to an external application.
func (s *Store) FulfillMerchantOrder(ctx context.Context, token, workspace, id string) (MerchantOrder, error) {
	if !validMerchantOrderToken(id) {
		return MerchantOrder{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MerchantOrder{}, err
	}
	defer tx.Rollback()
	mode := s.merchantModeValue()
	actor, err := s.authorizeOwner(ctx, tx, token, workspace)
	if err != nil {
		return MerchantOrder{}, err
	}
	order, _, _, _, err := scanMerchantOrder(tx.QueryRowContext(ctx, "SELECT "+merchantOrderColumns+" FROM merchant_orders WHERE mode=? AND id=? AND workspace_id=?", mode, id, workspace))
	if errors.Is(err, sql.ErrNoRows) {
		return MerchantOrder{}, ErrDenied
	}
	if err != nil {
		return MerchantOrder{}, err
	}
	// Replay preserves the original actor and time, including after a later refund.
	if order.FulfilledAt != 0 {
		return order, tx.Commit()
	}
	if order.State != "complete" || order.PaymentStatus != "paid" {
		return MerchantOrder{}, ErrBillingConflict
	}
	var blocked int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM merchant_refunds WHERE mode=? AND order_id=? AND state NOT IN ('failed','canceled')", mode, id).Scan(&blocked); err != nil {
		return MerchantOrder{}, err
	}
	if blocked != 0 {
		return MerchantOrder{}, ErrBillingConflict
	}
	now := s.now().Unix()
	if now <= 0 {
		return MerchantOrder{}, ErrInvalid
	}
	if _, err = tx.ExecContext(ctx, "UPDATE merchant_orders SET fulfilled_at=?,fulfilled_by=? WHERE mode=? AND id=?", now, actor, mode, id); err != nil {
		return MerchantOrder{}, err
	}
	if err = audit(ctx, tx, actor, workspace, "merchant.order_fulfilled:"+id, now); err != nil {
		return MerchantOrder{}, err
	}
	order.FulfilledAt, order.FulfilledBy = now, actor
	return order, tx.Commit()
}
