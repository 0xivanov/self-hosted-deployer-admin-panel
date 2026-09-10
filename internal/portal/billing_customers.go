package portal

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

type BillingCustomer struct {
	WorkspaceID string `json:"workspace_id"`
	RequestID   string `json:"-"`
	ActorID     string `json:"-"`
	Email       string `json:"email"`
	CustomerID  string `json:"customer_id,omitempty"`
	CreatedAt   int64  `json:"created_at"`
}

const billingCustomerColumns = "workspace_id,request_id,actor_id,email,COALESCE(customer_id,''),created_at"

func scanBillingCustomer(row interface{ Scan(...any) error }) (BillingCustomer, error) {
	var c BillingCustomer
	err := row.Scan(&c.WorkspaceID, &c.RequestID, &c.ActorID, &c.Email, &c.CustomerID, &c.CreatedAt)
	return c, err
}

// RequestBillingCustomer authorizes a workspace owner and persists the exact
// provider request identity and email snapshot before contacting Stripe.
func (s *Store) RequestBillingCustomer(ctx context.Context, token, workspace string) (BillingCustomer, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BillingCustomer{}, err
	}
	defer tx.Rollback()
	actor, err := s.authorizeOwner(ctx, tx, token, workspace)
	if err != nil {
		return BillingCustomer{}, err
	}
	existing, err := scanBillingCustomer(tx.QueryRowContext(ctx, "SELECT "+billingCustomerColumns+" FROM billing_customers WHERE workspace_id=?", workspace))
	if err == nil {
		return existing, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return BillingCustomer{}, err
	}
	c := BillingCustomer{WorkspaceID: workspace, RequestID: randomToken(), ActorID: actor, CreatedAt: s.now().Unix()}
	if err = tx.QueryRowContext(ctx, "SELECT email FROM users WHERE id=?", actor).Scan(&c.Email); err != nil {
		return BillingCustomer{}, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO billing_customers(workspace_id,request_id,actor_id,email,created_at) VALUES(?,?,?,?,?)", workspace, c.RequestID, actor, c.Email, c.CreatedAt); err != nil {
		return BillingCustomer{}, err
	}
	if err = audit(ctx, tx, actor, workspace, "billing.customer_requested:"+c.RequestID, c.CreatedAt); err != nil {
		return BillingCustomer{}, err
	}
	return c, tx.Commit()
}
func (s *Store) BillingCustomer(ctx context.Context, token, workspace string) (BillingCustomer, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BillingCustomer{}, err
	}
	defer tx.Rollback()
	if _, err = s.authorizeOwner(ctx, tx, token, workspace); err != nil {
		return BillingCustomer{}, err
	}
	c, err := scanBillingCustomer(tx.QueryRowContext(ctx, "SELECT "+billingCustomerColumns+" FROM billing_customers WHERE workspace_id=?", workspace))
	if errors.Is(err, sql.ErrNoRows) {
		return BillingCustomer{}, ErrDenied
	}
	if err != nil {
		return BillingCustomer{}, err
	}
	return c, tx.Commit()
}

// BillingCustomerWork is trusted worker access. Do not blindly retry Stripe
// creates beyond the provider's idempotency retention window. Old uncertain
// requests require provider reconciliation with their retained request identity.
func (s *Store) BillingCustomerWork(ctx context.Context, request string) (BillingCustomer, error) {
	c, err := scanBillingCustomer(s.db.QueryRowContext(ctx, "SELECT "+billingCustomerColumns+" FROM billing_customers WHERE request_id=?", request))
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrDenied
	}
	if err != nil {
		return c, err
	}
	if c.CustomerID == "" {
		var allowed int
		err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM users u JOIN memberships m ON m.user_id=u.id WHERE u.id=? AND u.verified=1 AND u.disabled=0 AND m.workspace_id=? AND m.role='owner'", c.ActorID, c.WorkspaceID).Scan(&allowed)
		if err != nil {
			return c, err
		}
		if allowed != 1 {
			return c, ErrDenied
		}
	}
	if c.CustomerID == "" && s.now().Sub(time.Unix(c.CreatedAt, 0)) >= 23*time.Hour {
		return c, ErrBillingConflict
	}
	return c, nil
}

// BindBillingCustomer records a test-provider acknowledgement to its persisted
// request. It cannot move a customer between workspaces or replace a mapping.
func (s *Store) BindBillingCustomer(ctx context.Context, request, customer string) error {
	if !strings.HasPrefix(customer, "cus_") || len(customer) > 255 || len(customer) <= 4 || strings.ContainsAny(customer, " /\\\r\n") {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	c, err := scanBillingCustomer(tx.QueryRowContext(ctx, "SELECT "+billingCustomerColumns+" FROM billing_customers WHERE request_id=?", request))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDenied
	}
	if err != nil {
		return err
	}
	if c.CustomerID != "" {
		if c.CustomerID != customer {
			return ErrBillingConflict
		}
		return tx.Commit()
	}
	var used int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM billing_customers WHERE customer_id=?", customer).Scan(&used); err != nil {
		return err
	}
	if used != 0 {
		return ErrBillingConflict
	}
	if _, err = tx.ExecContext(ctx, "UPDATE billing_customers SET customer_id=? WHERE request_id=?", customer, request); err != nil {
		return err
	}
	if err = audit(ctx, tx, c.ActorID, c.WorkspaceID, "billing.customer_bound:"+request, s.now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}
