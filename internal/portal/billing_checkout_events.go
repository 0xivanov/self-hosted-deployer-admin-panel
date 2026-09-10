package portal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	stripe "github.com/stripe/stripe-go/v86"
)

var ErrBillingUnmatched = errors.New("billing event has no acknowledged checkout yet")

// ProcessBillingCheckoutEvent consumes only an already verified durable inbox
// event. Checkout completion establishes subscription identity, never paid
// hosting access. Unknown sessions stay pending for delayed acknowledgement or
// reconciliation; browser redirects and event metadata cannot create ownership.
func (s *Store) ProcessBillingCheckoutEvent(ctx context.Context, eventID string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var kind, state string
	var payload []byte
	err = tx.QueryRowContext(ctx, "SELECT event_type,state,payload FROM billing_events WHERE id=?", eventID).Scan(&kind, &state, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrDenied
	}
	if err != nil {
		return false, err
	}
	if state != "pending" {
		return true, tx.Commit()
	}
	if kind != "checkout.session.completed" && kind != "checkout.session.expired" {
		return false, nil
	}
	var session stripe.CheckoutSession
	var scope struct {
		Live   *bool  `json:"livemode"`
		Object string `json:"object"`
	}
	if json.Unmarshal(payload, &session) != nil || json.Unmarshal(payload, &scope) != nil || scope.Live == nil || *scope.Live || scope.Object != "checkout.session" || session.Mode != "subscription" || session.Customer == nil {
		return false, ErrBillingConflict
	}
	c, err := scanCheckout(tx.QueryRowContext(ctx, "SELECT "+checkoutColumns+" FROM billing_checkouts WHERE session_id=?", session.ID))
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrBillingUnmatched
	}
	if err != nil {
		return false, err
	}
	if session.Customer.ID != c.CustomerID || session.ClientReferenceID != c.ID {
		return false, ErrBillingConflict
	}
	target := "completed"
	if kind == "checkout.session.expired" {
		if session.Status != "expired" {
			return false, ErrBillingConflict
		}
		// Never let a delayed expiry overwrite a recorded completed checkout.
		if c.State == "completed" {
			if _, err = tx.ExecContext(ctx, "UPDATE billing_events SET state='ignored' WHERE id=?", eventID); err != nil {
				return false, err
			}
			return true, tx.Commit()
		}
		if c.State != "open" && c.State != "expired" {
			return false, ErrBillingConflict
		}
		target = "expired"
	} else {
		if session.Status != "complete" || session.Subscription == nil || !strings.HasPrefix(session.Subscription.ID, "sub_") || len(session.Subscription.ID) <= 4 || len(session.Subscription.ID) > 255 || strings.ContainsAny(session.Subscription.ID, " /\\\r\n") {
			return false, ErrBillingConflict
		}
		if c.State != "open" && c.State != "completed" {
			return false, ErrBillingConflict
		}
		var boundID, boundCheckout string
		err = tx.QueryRowContext(ctx, "SELECT id,checkout_id FROM billing_subscriptions WHERE id=? OR checkout_id=?", session.Subscription.ID, c.ID).Scan(&boundID, &boundCheckout)
		if err == nil {
			if boundID != session.Subscription.ID || boundCheckout != c.ID {
				return false, ErrBillingConflict
			}
		} else if errors.Is(err, sql.ErrNoRows) {
			if _, err = tx.ExecContext(ctx, "INSERT INTO billing_subscriptions(id,checkout_id,workspace_id,customer_id,plan_id,price_id,first_event) VALUES(?,?,?,?,?,?,?)", session.Subscription.ID, c.ID, c.WorkspaceID, c.CustomerID, c.PlanID, c.PriceID, eventID); err != nil {
				return false, err
			}
		} else {
			return false, err
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE billing_checkouts SET state=? WHERE id=?", target, c.ID); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE billing_events SET state='processed' WHERE id=?", eventID); err != nil {
		return false, err
	}
	if err = audit(ctx, tx, "stripe-test", c.WorkspaceID, "billing.checkout_"+target+":"+c.ID+":event:"+eventID, s.now().Unix()); err != nil {
		return false, err
	}
	return true, tx.Commit()
}
