package portal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	stripe "github.com/stripe/stripe-go/v86"
)

// ProcessBillingChargeEvent treats verified refund/dispute events as lookup
// signals. Financial state and ownership come from the authenticated provider,
// never the event amount, customer metadata or dispute outcome alone.
func (s *Store) ProcessBillingChargeEvent(ctx context.Context, id string, p BillingChargeReader) (bool, error) {
	mode := s.billingModeValue()
	var kind, state string
	var payload []byte
	err := s.db.QueryRowContext(ctx, "SELECT event_type,state,payload FROM billing_events WHERE mode=? AND id=?", mode, id).Scan(&kind, &state, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrDenied
	}
	if err != nil {
		return false, err
	}
	if state != "pending" {
		return true, nil
	}
	var chargeID string
	var scope struct {
		Live   *bool  `json:"livemode"`
		Object string `json:"object"`
	}
	switch kind {
	case "charge.succeeded", "charge.refunded":
		var charge stripe.Charge
		if json.Unmarshal(payload, &charge) != nil || json.Unmarshal(payload, &scope) != nil || scope.Live == nil || *scope.Live != (mode == "live") || scope.Object != "charge" {
			return false, ErrBillingConflict
		}
		chargeID = charge.ID
	case "charge.dispute.created", "charge.dispute.updated", "charge.dispute.closed", "charge.dispute.funds_withdrawn", "charge.dispute.funds_reinstated":
		var dispute stripe.Dispute
		if json.Unmarshal(payload, &dispute) != nil || json.Unmarshal(payload, &scope) != nil || scope.Live == nil || *scope.Live != (mode == "live") || scope.Object != "dispute" || dispute.Charge == nil {
			return false, ErrBillingConflict
		}
		chargeID = dispute.Charge.ID
	default:
		return false, nil
	}
	if err = s.ReconcileBillingCharge(ctx, p, chargeID); err != nil {
		return false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if err = tx.QueryRowContext(ctx, "SELECT state FROM billing_events WHERE mode=? AND id=?", mode, id).Scan(&state); err != nil {
		return false, err
	}
	if state != "pending" {
		return true, tx.Commit()
	}
	var subscription, workspace string
	if err = tx.QueryRowContext(ctx, `SELECT c.subscription_id,s.workspace_id FROM billing_charges c JOIN billing_subscriptions s ON s.mode=c.mode AND s.id=c.subscription_id WHERE c.mode=? AND c.id=?`, mode, chargeID).Scan(&subscription, &workspace); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE billing_subscriptions SET snapshot=NULL,reconciliation_generation=reconciliation_generation+1 WHERE mode=? AND id=?", mode, subscription); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO billing_work(mode,kind,reference) VALUES(?, 'subscription',?) ON CONFLICT(mode,kind,reference) DO UPDATE SET next_attempt=0,attempts=0,done=0,lease_hash='',lease_until=0`, mode, subscription); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE billing_events SET state='processed' WHERE mode=? AND id=?", mode, id); err != nil {
		return false, err
	}
	if err = audit(ctx, tx, "stripe-"+mode, workspace, "billing.charge_refreshed:"+chargeID+":event:"+id, s.now().Unix()); err != nil {
		return false, err
	}
	return true, tx.Commit()
}
