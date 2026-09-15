package portal

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/merchantbilling"
)

type MerchantCheckoutProvider interface {
	CreateCheckout(context.Context, string, merchantbilling.CheckoutOrder) (merchantbilling.Checkout, error)
	RetrieveCheckout(context.Context, string, string, merchantbilling.CheckoutOrder) (merchantbilling.Checkout, error)
}

type MerchantOrder struct {
	FulfilledAt     int64  `json:"fulfilled_at"`
	FulfilledBy     string `json:"-"`
	ID              string `json:"id"`
	WorkspaceID     string `json:"workspace_id"`
	ProductID       string `json:"product_id"`
	ProductRevision int64  `json:"product_revision"`
	Name            string `json:"name"`
	Currency        string `json:"currency"`
	AmountMinor     int64  `json:"amount_minor"`
	State           string `json:"state"`
	PaymentStatus   string `json:"payment_status"`
	CreatedAt       int64  `json:"created_at"`
	ObservedAt      int64  `json:"observed_at"`
	BuyerHash       string `json:"-"`
	RequestKey      string `json:"-"`
	AccountID       string `json:"-"`
	SessionID       string `json:"-"`
	CheckoutURL     string `json:"-"`
	PaymentIntentID string `json:"-"`
}

const merchantOrderColumns = "id,workspace_id,product_id,product_revision,buyer_hash,request_key,account_id,name,currency,amount_minor,state,payment_status,COALESCE(session_id,''),COALESCE(checkout_url,''),COALESCE(payment_intent_id,''),created_at,submitted_at,observation_generation,observed_at,fulfilled_at,fulfilled_by"

func scanMerchantOrder(row interface{ Scan(...any) error }) (MerchantOrder, int64, int64, int64, error) {
	var o MerchantOrder
	var submitted, generation int64
	err := row.Scan(&o.ID, &o.WorkspaceID, &o.ProductID, &o.ProductRevision, &o.BuyerHash, &o.RequestKey, &o.AccountID, &o.Name, &o.Currency, &o.AmountMinor, &o.State, &o.PaymentStatus, &o.SessionID, &o.CheckoutURL, &o.PaymentIntentID, &o.CreatedAt, &submitted, &generation, &o.ObservedAt, &o.FulfilledAt, &o.FulfilledBy)
	return o, submitted, generation, o.ObservedAt, err
}

func validMerchantOrderToken(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func freshMerchantAccount(account MerchantAccount, now int64) bool {
	return account.State == "bound" && account.Snapshot != nil && account.Snapshot.ID == account.AccountID && account.Snapshot.Country == account.Country && account.Snapshot.RequestID == account.RequestID && account.Snapshot.DetailsSubmitted && account.Snapshot.ChargesEnabled && account.Snapshot.PayoutsEnabled && account.Snapshot.CardPayments == "active" && account.Snapshot.ObservedAt > 0 && now-account.Snapshot.ObservedAt >= 0 && now-account.Snapshot.ObservedAt < 300
}

func (s *Store) loadFreshMerchantAccount(ctx context.Context, tx *sql.Tx, workspace string, now int64) (MerchantAccount, error) {
	account, err := scanMerchantAccount(tx.QueryRowContext(ctx, "SELECT "+merchantAccountColumns+" FROM merchant_accounts WHERE workspace_id=?", workspace))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return MerchantAccount{}, ErrBillingConflict
		}
		return MerchantAccount{}, err
	}
	if !freshMerchantAccount(account, now) {
		return MerchantAccount{}, ErrBillingConflict
	}
	var owner int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM users u JOIN memberships m ON m.user_id=u.id AND m.workspace_id=? WHERE u.id=? AND u.verified=1 AND u.disabled=0 AND m.role='owner'", workspace, account.ActorID).Scan(&owner); err != nil {
		return MerchantAccount{}, err
	}
	if owner != 1 {
		return MerchantAccount{}, ErrDenied
	}
	return account, nil
}

func (s *Store) RequestMerchantOrder(ctx context.Context, buyerToken, productID string, revision int64, key string) (MerchantOrder, error) {
	if !validMerchantOrderToken(buyerToken) || !validMerchantOrderToken(key) || productID == "" || revision < 1 {
		return MerchantOrder{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MerchantOrder{}, err
	}
	defer tx.Rollback()
	buyerHash := digest(buyerToken)
	var existing MerchantOrder
	existing, _, _, _, err = scanMerchantOrder(tx.QueryRowContext(ctx, "SELECT "+merchantOrderColumns+" FROM merchant_orders WHERE buyer_hash=? AND request_key=?", buyerHash, key))
	if err == nil {
		if existing.ProductID != productID || existing.ProductRevision != revision {
			return MerchantOrder{}, ErrBillingConflict
		}
		return existing, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return MerchantOrder{}, err
	}
	product, err := scanMerchantProduct(tx.QueryRowContext(ctx, "SELECT "+merchantProductColumns+" FROM merchant_products WHERE id=?", productID))
	if errors.Is(err, sql.ErrNoRows) {
		return MerchantOrder{}, ErrDenied
	}
	if err != nil {
		return MerchantOrder{}, err
	}
	if !product.Active || product.Revision != revision {
		return MerchantOrder{}, ErrBillingConflict
	}
	now := s.now().Unix()
	account, err := s.loadFreshMerchantAccount(ctx, tx, product.WorkspaceID, now)
	if err != nil {
		return MerchantOrder{}, err
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM merchant_orders WHERE workspace_id=?", product.WorkspaceID).Scan(&count); err != nil {
		return MerchantOrder{}, err
	}
	if count >= 10000 {
		return MerchantOrder{}, ErrBillingConflict
	}
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM merchant_orders WHERE buyer_hash=? AND created_at>?", buyerHash, now-60).Scan(&count); err != nil {
		return MerchantOrder{}, err
	}
	if count >= 20 {
		return MerchantOrder{}, ErrBillingConflict
	}
	o := MerchantOrder{ID: randomToken(), WorkspaceID: product.WorkspaceID, ProductID: product.ID, ProductRevision: product.Revision, BuyerHash: buyerHash, RequestKey: key, AccountID: account.AccountID, Name: product.Name, Currency: product.Currency, AmountMinor: product.AmountMinor, State: "requested", PaymentStatus: "unpaid", CreatedAt: now}
	_, err = tx.ExecContext(ctx, "INSERT INTO merchant_orders(id,workspace_id,product_id,product_revision,buyer_hash,request_key,account_id,name,currency,amount_minor,state,payment_status,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?, ?,?)", o.ID, o.WorkspaceID, o.ProductID, o.ProductRevision, o.BuyerHash, o.RequestKey, o.AccountID, o.Name, o.Currency, o.AmountMinor, o.State, o.PaymentStatus, o.CreatedAt)
	if err != nil {
		return MerchantOrder{}, err
	}
	if err = audit(ctx, tx, account.ActorID, o.WorkspaceID, "merchant.order_requested:"+o.ID, now); err != nil {
		return MerchantOrder{}, err
	}
	return o, tx.Commit()
}

func (s *Store) BuyerMerchantOrder(ctx context.Context, buyerToken, orderID string) (MerchantOrder, error) {
	if !validMerchantOrderToken(buyerToken) || orderID == "" {
		return MerchantOrder{}, ErrInvalid
	}
	o, _, _, _, err := scanMerchantOrder(s.db.QueryRowContext(ctx, "SELECT "+merchantOrderColumns+" FROM merchant_orders WHERE id=? AND buyer_hash=?", orderID, digest(buyerToken)))
	if errors.Is(err, sql.ErrNoRows) {
		return MerchantOrder{}, ErrDenied
	}
	if err != nil {
		return MerchantOrder{}, err
	}
	return o, nil
}

func (s *Store) MerchantOrders(ctx context.Context, ownerToken, workspace string) ([]MerchantOrder, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = s.authorizeOwner(ctx, tx, ownerToken, workspace); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT "+merchantOrderColumns+" FROM merchant_orders WHERE workspace_id=? ORDER BY created_at DESC,id DESC LIMIT 100", workspace)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	orders := []MerchantOrder{}
	for rows.Next() {
		o, _, _, _, scanErr := scanMerchantOrder(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		orders = append(orders, o)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return orders, tx.Commit()
}

func checkoutForOrder(o MerchantOrder) merchantbilling.CheckoutOrder {
	return merchantbilling.CheckoutOrder{RequestID: o.ID, Name: o.Name, Currency: o.Currency, AmountMinor: o.AmountMinor}
}

func validMerchantProviderID(value, prefix string) bool {
	if !strings.HasPrefix(value, prefix) || len(value) <= len(prefix) || len(value) > 255 {
		return false
	}
	for _, r := range value[len(prefix):] {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_') {
			return false
		}
	}
	return true
}

func validMerchantCheckoutResult(result merchantbilling.Checkout, now, floor int64) bool {
	if !validMerchantProviderID(result.ID, "cs_test_") || (result.State != "open" && result.State != "complete" && result.State != "expired") || (result.PaymentStatus != "unpaid" && result.PaymentStatus != "paid") || result.ObservedAt < floor || result.ObservedAt > now {
		return false
	}
	if result.PaymentStatus == "paid" && (result.State != "complete" || !validMerchantProviderID(result.PaymentIntentID, "pi_")) {
		return false
	}
	if result.State == "open" && result.URL == "" {
		return false
	}
	if result.URL != "" {
		u, err := url.Parse(result.URL)
		if err != nil || u.Scheme != "https" || u.Host != "checkout.stripe.com" || u.User != nil {
			return false
		}
	}
	if result.PaymentIntentID != "" && !validMerchantProviderID(result.PaymentIntentID, "pi_") {
		return false
	}
	return true
}

func (s *Store) DispatchMerchantOrder(ctx context.Context, id string, provider MerchantCheckoutProvider) (MerchantOrder, error) {
	if provider == nil || id == "" {
		return MerchantOrder{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MerchantOrder{}, err
	}
	defer tx.Rollback()
	o, _, _, _, err := scanMerchantOrder(tx.QueryRowContext(ctx, "SELECT "+merchantOrderColumns+" FROM merchant_orders WHERE id=?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return MerchantOrder{}, ErrDenied
	}
	if err != nil {
		return MerchantOrder{}, err
	}
	if o.State != "requested" {
		if o.State == "submitted" {
			return MerchantOrder{}, ErrBillingConflict
		}
		return o, tx.Commit()
	}
	product, err := scanMerchantProduct(tx.QueryRowContext(ctx, "SELECT "+merchantProductColumns+" FROM merchant_products WHERE id=? AND workspace_id=?", o.ProductID, o.WorkspaceID))
	if err != nil {
		return MerchantOrder{}, ErrBillingConflict
	}
	if !product.Active {
		return MerchantOrder{}, ErrBillingConflict
	}
	account, err := s.loadFreshMerchantAccount(ctx, tx, o.WorkspaceID, s.now().Unix())
	if err != nil || account.AccountID != o.AccountID {
		if err == nil {
			err = ErrBillingConflict
		}
		return MerchantOrder{}, err
	}
	submittedAt := s.now().Unix()
	if _, err = tx.ExecContext(ctx, "UPDATE merchant_orders SET state='submitted',submitted_at=? WHERE id=? AND state='requested'", submittedAt, id); err != nil {
		return MerchantOrder{}, err
	}
	if err = audit(ctx, tx, account.ActorID, o.WorkspaceID, "merchant.order_submitted:"+id, submittedAt); err != nil {
		return MerchantOrder{}, err
	}
	if err = tx.Commit(); err != nil {
		return MerchantOrder{}, err
	}
	call, cancel := context.WithTimeout(ctx, 30*time.Second)
	result, providerErr := provider.CreateCheckout(call, o.AccountID, checkoutForOrder(o))
	cancel()
	if providerErr != nil {
		o.State = "submitted"
		return o, providerErr
	}
	return s.bindMerchantCheckout(ctx, id, result, submittedAt, 0)
}

func (s *Store) bindMerchantCheckout(ctx context.Context, id string, result merchantbilling.Checkout, floor, generation int64) (MerchantOrder, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MerchantOrder{}, err
	}
	defer tx.Rollback()
	o, submitted, currentGeneration, _, err := scanMerchantOrder(tx.QueryRowContext(ctx, "SELECT "+merchantOrderColumns+" FROM merchant_orders WHERE id=?", id))
	if err != nil {
		return MerchantOrder{}, err
	}
	if currentGeneration != generation {
		return MerchantOrder{}, ErrBillingConflict
	}
	now := s.now().Unix()
	if floor < submitted {
		floor = submitted
	}
	if !validMerchantCheckoutResult(result, now, floor) {
		return MerchantOrder{}, ErrBillingConflict
	}
	if o.SessionID != "" && o.SessionID != result.ID {
		return MerchantOrder{}, ErrBillingConflict
	}
	if o.PaymentStatus == "paid" && result.PaymentStatus != "paid" {
		return MerchantOrder{}, ErrBillingConflict
	}
	if (o.State == "complete" || o.State == "expired") && o.State != result.State {
		return MerchantOrder{}, ErrBillingConflict
	}
	if o.PaymentIntentID != "" && o.PaymentIntentID != result.PaymentIntentID {
		return MerchantOrder{}, ErrBillingConflict
	}
	var used int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM merchant_orders WHERE account_id=? AND session_id=? AND id!=?", o.AccountID, result.ID, id).Scan(&used); err != nil {
		return MerchantOrder{}, err
	}
	if used != 0 {
		return MerchantOrder{}, ErrBillingConflict
	}
	state := result.State
	_, err = tx.ExecContext(ctx, "UPDATE merchant_orders SET state=?,payment_status=?,session_id=?,checkout_url=?,payment_intent_id=?,observation_generation=observation_generation+1,observed_at=? WHERE id=? AND observation_generation=?", state, result.PaymentStatus, result.ID, result.URL, result.PaymentIntentID, result.ObservedAt, id, currentGeneration)
	if err != nil {
		return MerchantOrder{}, err
	}
	var actor string
	if err = tx.QueryRowContext(ctx, "SELECT actor_id FROM merchant_accounts WHERE workspace_id=?", o.WorkspaceID).Scan(&actor); err != nil {
		return MerchantOrder{}, err
	}
	if err = audit(ctx, tx, actor, o.WorkspaceID, "merchant.order_provider_update:"+id, result.ObservedAt); err != nil {
		return MerchantOrder{}, err
	}
	o.State, o.PaymentStatus, o.SessionID, o.CheckoutURL, o.PaymentIntentID, o.ObservedAt = state, result.PaymentStatus, result.ID, result.URL, result.PaymentIntentID, result.ObservedAt
	return o, tx.Commit()
}

func (s *Store) ReconcileMerchantOrder(ctx context.Context, id, sessionID string, provider MerchantCheckoutProvider) (MerchantOrder, error) {
	if provider == nil || id == "" || !validMerchantProviderID(sessionID, "cs_test_") {
		return MerchantOrder{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MerchantOrder{}, err
	}
	defer tx.Rollback()
	o, _, generation, _, err := scanMerchantOrder(tx.QueryRowContext(ctx, "SELECT "+merchantOrderColumns+" FROM merchant_orders WHERE id=?", id))
	if err != nil {
		return MerchantOrder{}, err
	}
	if o.State == "requested" || (o.SessionID != "" && o.SessionID != sessionID) {
		return MerchantOrder{}, ErrBillingConflict
	}
	if _, err = tx.ExecContext(ctx, "UPDATE merchant_orders SET observation_generation=observation_generation+1 WHERE id=?", id); err != nil {
		tx.Rollback()
		return MerchantOrder{}, err
	}
	if err = tx.Commit(); err != nil {
		return MerchantOrder{}, err
	}
	generation++
	start := s.now().Unix()
	call, cancel := context.WithTimeout(ctx, 30*time.Second)
	result, providerErr := provider.RetrieveCheckout(call, o.AccountID, sessionID, checkoutForOrder(o))
	cancel()
	if providerErr != nil {
		return o, providerErr
	}
	if result.ID != sessionID || result.ObservedAt < start {
		return MerchantOrder{}, ErrBillingConflict
	}
	return s.bindMerchantCheckout(ctx, id, result, start, generation)
}
