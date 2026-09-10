package portal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	stripe "github.com/stripe/stripe-go/v86"
)

// ProcessBillingSubscriptionEvent uses verified subscription and invoice events
// only as refresh signals.
// Event status and timestamps cannot overwrite current provider observations.
func (s *Store) ProcessBillingSubscriptionEvent(ctx context.Context, id string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var kind, state string
	var payload []byte
	err = tx.QueryRowContext(ctx, "SELECT event_type,state,payload FROM billing_events WHERE id=?", id).Scan(&kind, &state, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrDenied
	}
	if err != nil {
		return false, err
	}
	if state != "pending" {
		return true, tx.Commit()
	}
	subscriptionID, customerID, supported, err := billingRefreshIdentity(kind, payload)
	if err != nil {
		return false, err
	}
	if !supported {
		return false, nil
	}
	var customer, workspace string
	err = tx.QueryRowContext(ctx, "SELECT customer_id,workspace_id FROM billing_subscriptions WHERE id=?", subscriptionID).Scan(&customer, &workspace)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrBillingUnmatched
	}
	if err != nil {
		return false, err
	}
	if customer != customerID {
		return false, ErrBillingConflict
	}
	// Clear the previous observation and fence reads that began before the signal.
	if _, err = tx.ExecContext(ctx, "UPDATE billing_subscriptions SET reconciliation_generation=reconciliation_generation+1,snapshot=NULL WHERE id=?", subscriptionID); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO billing_work(kind,reference) VALUES('subscription',?) ON CONFLICT(kind,reference) DO UPDATE SET next_attempt=0,attempts=0,done=0,lease_hash='',lease_until=0`, subscriptionID); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE billing_events SET state='processed' WHERE id=?", id); err != nil {
		return false, err
	}
	if err = audit(ctx, tx, "stripe-test", workspace, "billing.subscription_refresh:"+subscriptionID+":event:"+id, s.now().Unix()); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// Identity is established from the subscription relation and customer together,
// never invoice metadata or the latest invoice amount/status.
func billingRefreshIdentity(kind string, payload []byte) (string, string, bool, error) {
	var scope struct {
		Live   *bool  `json:"livemode"`
		Object string `json:"object"`
	}
	switch kind {
	case "customer.subscription.created", "customer.subscription.updated", "customer.subscription.deleted":
		var sub stripe.Subscription
		if json.Unmarshal(payload, &sub) != nil || json.Unmarshal(payload, &scope) != nil || scope.Live == nil || *scope.Live || scope.Object != "subscription" || sub.Customer == nil || sub.CustomerAccount != "" {
			return "", "", true, ErrBillingConflict
		}
		return sub.ID, sub.Customer.ID, true, nil
	case "invoice.paid", "invoice.payment_failed", "invoice.payment_action_required", "invoice.voided", "invoice.marked_uncollectible", "invoice.finalized":
		var invoice stripe.Invoice
		if json.Unmarshal(payload, &invoice) != nil || json.Unmarshal(payload, &scope) != nil || scope.Live == nil || *scope.Live || scope.Object != "invoice" || invoice.Customer == nil || invoice.CustomerAccount != "" || invoice.Parent == nil || invoice.Parent.Type != "subscription_details" || invoice.Parent.SubscriptionDetails == nil || invoice.Parent.SubscriptionDetails.Subscription == nil {
			return "", "", true, ErrBillingConflict
		}
		return invoice.Parent.SubscriptionDetails.Subscription.ID, invoice.Customer.ID, true, nil
	default:
		return "", "", false, nil
	}
}
