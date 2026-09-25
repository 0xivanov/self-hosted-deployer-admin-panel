package portal

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/merchantbilling"
)

type MerchantRefundProvider interface {
	CreateRefund(context.Context, string, merchantbilling.RefundRequest) (merchantbilling.Refund, error)
	RetrieveRefund(context.Context, string, string, merchantbilling.RefundRequest) (merchantbilling.Refund, error)
}
type MerchantRefund struct {
	ID              string `json:"id"`
	OrderID         string `json:"order_id"`
	Currency        string `json:"currency"`
	AmountMinor     int64  `json:"amount_minor"`
	State           string `json:"state"`
	CreatedAt       int64  `json:"created_at"`
	ObservedAt      int64  `json:"observed_at"`
	WorkspaceID     string `json:"-"`
	ActorID         string `json:"-"`
	AccountID       string `json:"-"`
	PaymentIntentID string `json:"-"`
	ProviderID      string `json:"-"`
	SubmittedAt     int64  `json:"-"`
	Generation      int64  `json:"-"`
}

const merchantRefundColumns = "id,order_id,workspace_id,actor_id,account_id,payment_intent_id,currency,amount_minor,state,COALESCE(provider_id,''),created_at,submitted_at,observed_at,generation"

func scanMerchantRefund(row interface{ Scan(...any) error }) (MerchantRefund, error) {
	var r MerchantRefund
	err := row.Scan(&r.ID, &r.OrderID, &r.WorkspaceID, &r.ActorID, &r.AccountID, &r.PaymentIntentID, &r.Currency, &r.AmountMinor, &r.State, &r.ProviderID, &r.CreatedAt, &r.SubmittedAt, &r.ObservedAt, &r.Generation)
	return r, err
}
func refundRequest(r MerchantRefund) merchantbilling.RefundRequest {
	return merchantbilling.RefundRequest{RequestID: r.ID, OrderID: r.OrderID, PaymentIntentID: r.PaymentIntentID, Currency: r.Currency, AmountMinor: r.AmountMinor}
}

func (s *Store) RequestMerchantRefund(ctx context.Context, token, workspace, orderID string) (MerchantRefund, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MerchantRefund{}, err
	}
	defer tx.Rollback()
	actor, err := s.authorizeOwner(ctx, tx, token, workspace)
	if err != nil {
		return MerchantRefund{}, err
	}
	prior, err := scanMerchantRefund(tx.QueryRowContext(ctx, "SELECT "+merchantRefundColumns+" FROM merchant_refunds WHERE order_id=? AND workspace_id=?", orderID, workspace))
	if err == nil {
		return prior, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return MerchantRefund{}, err
	}
	order, _, _, _, err := scanMerchantOrder(tx.QueryRowContext(ctx, "SELECT "+merchantOrderColumns+" FROM merchant_orders WHERE id=? AND workspace_id=?", orderID, workspace))
	if errors.Is(err, sql.ErrNoRows) {
		return MerchantRefund{}, ErrDenied
	}
	if err != nil {
		return MerchantRefund{}, err
	}
	if order.State != "complete" || order.PaymentStatus != "paid" || !validMerchantProviderID(order.PaymentIntentID, "pi_") {
		return MerchantRefund{}, ErrBillingConflict
	}
	r := MerchantRefund{ID: randomToken(), OrderID: order.ID, WorkspaceID: workspace, ActorID: actor, AccountID: order.AccountID, PaymentIntentID: order.PaymentIntentID, Currency: order.Currency, AmountMinor: order.AmountMinor, State: "requested", CreatedAt: s.now().Unix()}
	_, err = tx.ExecContext(ctx, "INSERT INTO merchant_refunds(id,order_id,workspace_id,actor_id,account_id,payment_intent_id,currency,amount_minor,state,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)", r.ID, r.OrderID, r.WorkspaceID, r.ActorID, r.AccountID, r.PaymentIntentID, r.Currency, r.AmountMinor, r.State, r.CreatedAt)
	if err != nil {
		return MerchantRefund{}, err
	}
	if err = audit(ctx, tx, actor, workspace, "merchant.refund_requested:"+r.ID, r.CreatedAt); err != nil {
		return MerchantRefund{}, err
	}
	return r, tx.Commit()
}
func (s *Store) MerchantRefunds(ctx context.Context, token, workspace string) ([]MerchantRefund, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = s.authorizeOwner(ctx, tx, token, workspace); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT "+merchantRefundColumns+" FROM merchant_refunds WHERE workspace_id=? ORDER BY created_at DESC,id DESC LIMIT 100", workspace)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MerchantRefund{}
	for rows.Next() {
		r, e := scanMerchantRefund(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	return out, tx.Commit()
}
func (s *Store) DispatchMerchantRefund(ctx context.Context, id string, p MerchantRefundProvider) (MerchantRefund, error) {
	if err := validateMerchantProviderMode(p); err != nil {
		return MerchantRefund{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MerchantRefund{}, err
	}
	defer tx.Rollback()
	r, err := scanMerchantRefund(tx.QueryRowContext(ctx, "SELECT "+merchantRefundColumns+" FROM merchant_refunds WHERE id=?", id))
	if err != nil {
		return MerchantRefund{}, err
	}
	if r.State != "requested" {
		if r.State == "submitted" {
			return r, ErrBillingConflict
		}
		return r, tx.Commit()
	}
	var authorized int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM users u JOIN memberships m ON m.user_id=u.id WHERE u.id=? AND u.verified=1 AND u.disabled=0 AND m.workspace_id=? AND m.role='owner'", r.ActorID, r.WorkspaceID).Scan(&authorized); err != nil {
		return r, err
	}
	if authorized != 1 {
		return r, ErrDenied
	}
	r.SubmittedAt = s.now().Unix()
	if _, err = tx.ExecContext(ctx, "UPDATE merchant_refunds SET state='submitted',submitted_at=? WHERE id=?", r.SubmittedAt, id); err != nil {
		return r, err
	}
	if err = audit(ctx, tx, r.ActorID, r.WorkspaceID, "merchant.refund_submitted:"+r.ID, r.SubmittedAt); err != nil {
		return r, err
	}
	if err = tx.Commit(); err != nil {
		return r, err
	}
	call, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	observed, err := p.CreateRefund(call, r.AccountID, refundRequest(r))
	if err != nil {
		r.State = "submitted"
		return r, errors.New("refund outcome unknown; reconcile before further action")
	}
	return s.bindMerchantRefund(ctx, id, observed, r.SubmittedAt, r.Generation)
}
func (s *Store) bindMerchantRefund(ctx context.Context, id string, observed merchantbilling.Refund, floor, generation int64) (MerchantRefund, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MerchantRefund{}, err
	}
	defer tx.Rollback()
	r, err := scanMerchantRefund(tx.QueryRowContext(ctx, "SELECT "+merchantRefundColumns+" FROM merchant_refunds WHERE id=?", id))
	if err != nil {
		return r, err
	}
	if r.Generation != generation || !validMerchantProviderID(observed.ID, "re_") || observed.ObservedAt < floor || observed.ObservedAt < r.SubmittedAt || observed.ObservedAt > s.now().Unix() || r.State == "requested" {
		return r, ErrBillingConflict
	}
	switch observed.State {
	case "pending", "requires_action", "succeeded", "failed", "canceled":
	default:
		return r, ErrBillingConflict
	}
	if r.ProviderID != "" && r.ProviderID != observed.ID {
		return r, ErrBillingConflict
	}
	// A bank may return funds after apparent success. Accept fresh canonical
	// status changes; generation fencing rejects older in-flight observations.

	if _, err = tx.ExecContext(ctx, "UPDATE merchant_refunds SET state=?,provider_id=?,observed_at=?,generation=generation+1 WHERE id=?", observed.State, observed.ID, observed.ObservedAt, id); err != nil {
		return r, err
	}
	if r.State != observed.State {
		if err = audit(ctx, tx, r.ActorID, r.WorkspaceID, "merchant.refund_updated:"+r.ID, observed.ObservedAt); err != nil {
			return r, err
		}
	}
	r.State, r.ProviderID, r.ObservedAt = observed.State, observed.ID, observed.ObservedAt
	return r, tx.Commit()
}
func (s *Store) ReconcileMerchantRefund(ctx context.Context, id, providerID string, p MerchantRefundProvider) (MerchantRefund, error) {
	if err := validateMerchantProviderMode(p); err != nil {
		return MerchantRefund{}, err
	}
	if !validMerchantProviderID(providerID, "re_") {
		return MerchantRefund{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MerchantRefund{}, err
	}
	defer tx.Rollback()
	r, err := scanMerchantRefund(tx.QueryRowContext(ctx, "SELECT "+merchantRefundColumns+" FROM merchant_refunds WHERE id=?", id))
	if err != nil {
		return r, err
	}
	if r.State == "requested" || (r.ProviderID != "" && r.ProviderID != providerID) {
		return r, ErrBillingConflict
	}
	if _, err = tx.ExecContext(ctx, "UPDATE merchant_refunds SET generation=generation+1 WHERE id=?", id); err != nil {
		return r, err
	}
	if err = tx.Commit(); err != nil {
		return r, err
	}
	started := s.now().Unix()
	call, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	observed, err := p.RetrieveRefund(call, r.AccountID, providerID, refundRequest(r))
	if err != nil {
		return r, errors.New("refund observation unavailable")
	}
	if observed.ID != providerID {
		return r, ErrBillingConflict
	}
	return s.bindMerchantRefund(ctx, id, observed, started, r.Generation+1)
}

func (s *Store) OwnerMerchantRefund(ctx context.Context, token, workspace, id string) (MerchantRefund, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MerchantRefund{}, err
	}
	defer tx.Rollback()
	if _, err = s.authorizeOwner(ctx, tx, token, workspace); err != nil {
		return MerchantRefund{}, err
	}
	r, err := scanMerchantRefund(tx.QueryRowContext(ctx, "SELECT "+merchantRefundColumns+" FROM merchant_refunds WHERE id=? AND workspace_id=?", id, workspace))
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrDenied
	}
	if err != nil {
		return r, err
	}
	return r, tx.Commit()
}
