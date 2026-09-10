package portal

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"strings"
	"time"
)

type BillingCheckout struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	ActorID     string `json:"-"`
	CustomerID  string `json:"-"`
	PlanID      string `json:"plan"`
	PriceID     string `json:"-"`
	SessionID   string `json:"-"`
	URL         string `json:"url,omitempty"`
	State       string `json:"state"`
	CreatedAt   int64  `json:"created_at"`
}

const checkoutColumns = "id,workspace_id,actor_id,customer_id,plan_id,price_id,COALESCE(session_id,''),checkout_url,state,created_at"

func scanCheckout(row interface{ Scan(...any) error }) (BillingCheckout, error) {
	var c BillingCheckout
	err := row.Scan(&c.ID, &c.WorkspaceID, &c.ActorID, &c.CustomerID, &c.PlanID, &c.PriceID, &c.SessionID, &c.URL, &c.State, &c.CreatedAt)
	return c, err
}

// ConfigureBillingPlan is operator configuration, never a customer route.
func (s *Store) ConfigureBillingPlan(ctx context.Context, plan, price string, enabled bool) error {
	if plan == "" || len(plan) > 100 || !strings.HasPrefix(price, "price_") || len(price) > 255 || len(price) <= 6 || strings.ContainsAny(price, " /\\\r\n") {
		return ErrInvalid
	}
	_, err := s.db.ExecContext(ctx, "INSERT INTO billing_plans VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET price_id=excluded.price_id,enabled=excluded.enabled", plan, price, enabled)
	return err
}
func (s *Store) RequestBillingCheckout(ctx context.Context, token, workspace, plan string) (BillingCheckout, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BillingCheckout{}, err
	}
	defer tx.Rollback()
	actor, err := s.authorizeOwner(ctx, tx, token, workspace)
	if err != nil {
		return BillingCheckout{}, err
	}
	existing, err := scanCheckout(tx.QueryRowContext(ctx, "SELECT "+checkoutColumns+" FROM billing_checkouts WHERE workspace_id=? AND state IN ('pending','open','completed')", workspace))
	if err == nil {
		if existing.PlanID != plan {
			return BillingCheckout{}, ErrBillingConflict
		}
		return existing, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return BillingCheckout{}, err
	}
	c := BillingCheckout{ID: randomToken(), WorkspaceID: workspace, ActorID: actor, PlanID: plan, State: "pending", CreatedAt: s.now().Unix()}
	err = tx.QueryRowContext(ctx, "SELECT price_id FROM billing_plans WHERE id=? AND enabled=1", plan).Scan(&c.PriceID)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrInvalid
	}
	if err != nil {
		return c, err
	}
	err = tx.QueryRowContext(ctx, "SELECT customer_id FROM billing_customers WHERE workspace_id=? AND customer_id IS NOT NULL", workspace).Scan(&c.CustomerID)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrBillingConflict
	}
	if err != nil {
		return c, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO billing_checkouts(id,workspace_id,actor_id,customer_id,plan_id,price_id,state,created_at) VALUES(?,?,?,?,?,?,?,?)", c.ID, workspace, actor, c.CustomerID, plan, c.PriceID, c.State, c.CreatedAt); err != nil {
		return c, err
	}
	if err = audit(ctx, tx, actor, workspace, "billing.checkout_requested:"+c.ID, c.CreatedAt); err != nil {
		return c, err
	}
	return c, tx.Commit()
}
func (s *Store) BillingCheckout(ctx context.Context, token, workspace, id string) (BillingCheckout, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BillingCheckout{}, err
	}
	defer tx.Rollback()
	if _, err = s.authorizeOwner(ctx, tx, token, workspace); err != nil {
		return BillingCheckout{}, err
	}
	c, err := scanCheckout(tx.QueryRowContext(ctx, "SELECT "+checkoutColumns+" FROM billing_checkouts WHERE workspace_id=? AND id=?", workspace, id))
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrDenied
	}
	if err != nil {
		return c, err
	}
	return c, tx.Commit()
}

// BillingCheckoutWork rejects stale requests and changed plan configuration.
// A worker must send this exact snapshot and use ID as its idempotency key.
func (s *Store) BillingCheckoutWork(ctx context.Context, id string) (BillingCheckout, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BillingCheckout{}, err
	}
	defer tx.Rollback()
	c, err := scanCheckout(tx.QueryRowContext(ctx, "SELECT "+checkoutColumns+" FROM billing_checkouts WHERE id=?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrDenied
	}
	if err != nil {
		return c, err
	}
	if c.State != "pending" {
		return c, tx.Commit()
	}
	var valid int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM memberships m JOIN users u ON u.id=m.user_id JOIN billing_plans p ON p.id=? WHERE m.workspace_id=? AND u.id=? AND u.verified=1 AND u.disabled=0 AND m.role='owner' AND p.enabled=1 AND p.price_id=?`, c.PlanID, c.WorkspaceID, c.ActorID, c.PriceID).Scan(&valid)
	if err != nil {
		return c, err
	}
	if valid != 1 {
		return c, ErrDenied
	}
	if s.now().Sub(time.Unix(c.CreatedAt, 0)) >= 23*time.Hour {
		return c, ErrBillingConflict
	}
	return c, tx.Commit()
}

// BindBillingCheckout is a trusted provider acknowledgement, not a browser
// redirect handler. It opens a checkout link; it does not mark anything paid.
func (s *Store) BindBillingCheckout(ctx context.Context, id, session, rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.Host != "checkout.stripe.com" || u.User != nil || len(rawURL) > 8192 || !strings.HasPrefix(session, "cs_test_") || len(session) > 255 || len(session) <= 8 {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	c, err := scanCheckout(tx.QueryRowContext(ctx, "SELECT "+checkoutColumns+" FROM billing_checkouts WHERE id=?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDenied
	}
	if err != nil {
		return err
	}
	if c.SessionID != "" {
		if c.SessionID != session || c.URL != rawURL {
			return ErrBillingConflict
		}
		return tx.Commit()
	}
	if c.State != "pending" {
		return ErrBillingConflict
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM billing_checkouts WHERE session_id=?", session).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return ErrBillingConflict
	}
	if _, err = tx.ExecContext(ctx, "UPDATE billing_checkouts SET session_id=?,checkout_url=?,state='open' WHERE id=?", session, rawURL, id); err != nil {
		return err
	}
	if err = audit(ctx, tx, c.ActorID, c.WorkspaceID, "billing.checkout_opened:"+id, s.now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}
