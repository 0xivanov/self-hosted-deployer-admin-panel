package portal

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/hostingbilling"
)

// BillingProvider must be an authenticated test provider. No browser-supplied
// customer IDs, prices or redirect URLs enter these operations.
type BillingProvider interface {
	SubscriptionReader
	CreateCustomer(context.Context, string, string) (string, error)
	CreatePinnedCheckout(context.Context, string, string, string, string) (hostingbilling.Checkout, error)
}

// BillingWorkOnce discovers saved requests and processes one due task. Leases
// survive worker restarts; provider creates reuse the saved idempotency key.
// Failed/uncertain work backs off without discarding its identity.
func (s *Store) BillingWorkOnce(ctx context.Context, provider BillingProvider) (bool, error) {
	if provider == nil {
		return false, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	for _, query := range []string{
		`INSERT OR IGNORE INTO billing_work(kind,reference) SELECT 'customer',request_id FROM billing_customers WHERE customer_id IS NULL`,
		`INSERT OR IGNORE INTO billing_work(kind,reference) SELECT 'checkout',id FROM billing_checkouts WHERE state='pending'`,
		`INSERT OR IGNORE INTO billing_work(kind,reference) SELECT 'event',id FROM billing_events WHERE state='pending' AND event_type IN ('checkout.session.completed','checkout.session.expired')`,
		`INSERT OR IGNORE INTO billing_work(kind,reference) SELECT 'subscription',id FROM billing_subscriptions`,
	} {
		if _, err = tx.ExecContext(ctx, query); err != nil {
			return false, err
		}
	}
	now := s.now().Unix()
	var kind, reference string
	var attempts int
	err = tx.QueryRowContext(ctx, `SELECT kind,reference,attempts FROM billing_work WHERE done=0 AND next_attempt<=? AND lease_until<=? ORDER BY next_attempt,kind,reference LIMIT 1`, now, now).Scan(&kind, &reference, &attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return false, tx.Commit()
	}
	if err != nil {
		return false, err
	}
	lease := randomToken()
	if _, err = tx.ExecContext(ctx, `UPDATE billing_work SET lease_hash=?,lease_until=?,attempts=attempts+1 WHERE kind=? AND reference=?`, digest(lease), now+60, kind, reference); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	workErr := s.performBillingWork(ctx, provider, kind, reference)
	delay := int64(300)
	done := 0
	if workErr == nil && kind != "subscription" {
		done = 1
	}
	if workErr != nil {
		delay = 30
		for i := 0; i < attempts && delay < 3600; i++ {
			delay *= 2
		}
		if delay > 3600 {
			delay = 3600
		}
	}
	// A canceled operation leaves the lease for recovery. Never acknowledge a
	// result after another worker has acquired this task.
	result, err := s.db.ExecContext(ctx, `UPDATE billing_work SET done=?,next_attempt=?,lease_hash='',lease_until=0,attempts=CASE WHEN ? THEN 0 ELSE attempts END WHERE kind=? AND reference=? AND lease_hash=? AND lease_until>?`, done, s.now().Unix()+delay, workErr == nil, kind, reference, digest(lease), s.now().Unix())
	if err != nil {
		return true, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return true, err
	}
	if n != 1 {
		return true, ErrBillingConflict
	}
	return true, workErr
}
func (s *Store) performBillingWork(ctx context.Context, p BillingProvider, kind, reference string) error {
	switch kind {
	case "customer":
		c, err := s.BillingCustomerWork(ctx, reference)
		if err != nil {
			return err
		}
		if c.CustomerID != "" {
			return nil
		}
		id, err := p.CreateCustomer(ctx, c.Email, c.RequestID)
		if err != nil {
			return err
		}
		return s.BindBillingCustomer(ctx, c.RequestID, id)
	case "checkout":
		c, err := s.BillingCheckoutWork(ctx, reference)
		if err != nil {
			return err
		}
		if c.State != "pending" {
			return nil
		}
		checkout, err := p.CreatePinnedCheckout(ctx, c.CustomerID, c.PlanID, c.PriceID, c.ID)
		if err != nil {
			return err
		}
		return s.BindBillingCheckout(ctx, c.ID, checkout.ID, checkout.URL)
	case "event":
		done, err := s.ProcessBillingCheckoutEvent(ctx, reference)
		if err != nil {
			return err
		}
		if !done {
			return ErrBillingConflict
		}
		return nil
	case "subscription":
		return s.ReconcileBillingSubscription(ctx, reference, p)
	default:
		return ErrInvalid
	}
}

// RunBillingWorker drains available work, then polls quietly. Callback errors
// should be summarized by the caller without logging provider secrets or PII.
func (s *Store) RunBillingWorker(ctx context.Context, p interface {
	BillingProvider
	BillingPriceReader
}, onError func(error)) {
	for ctx.Err() == nil {
		refreshed, refreshErr := s.RefreshBillingPriceOnce(ctx, p)
		if refreshErr != nil && onError != nil && ctx.Err() == nil {
			onError(refreshErr)
		}
		worked, err := s.BillingWorkOnce(ctx, p)
		if err != nil && onError != nil && ctx.Err() == nil {
			onError(err)
		}
		if (worked && err == nil) || (refreshed && refreshErr == nil) {
			continue
		}
		timer := time.NewTimer(5 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
