package portal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/hostingbilling"
)

type BillingChargeReader interface {
	RetrieveChargeObservation(context.Context, string) (hostingbilling.ChargeObservation, error)
}

// ReconcileBillingCharge accepts a trusted provider reader and charge ID, never
// browser-supplied financial data. Mapping changes and superseded reads fail
// closed. Snapshots alone do not grant or suspend hosting.
func (s *Store) ReconcileBillingCharge(ctx context.Context, p BillingChargeReader, id string) error {
	if p == nil || !strings.HasPrefix(id, "ch_") || len(id) <= 3 || len(id) > 255 || strings.ContainsAny(id, " /\\\r\n") {
		return ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var generation int64
	err := s.db.QueryRowContext(ctx, `INSERT INTO billing_charges(id,generation) VALUES(?,1) ON CONFLICT(id) DO UPDATE SET generation=generation+1 RETURNING generation`, id).Scan(&generation)
	if err != nil {
		return err
	}
	started := s.now().Unix()
	observation, err := p.RetrieveChargeObservation(ctx, id)
	if err != nil {
		return err
	}
	if observation.ChargeID != id || observation.ObservedAt < started || observation.ObservedAt > s.now().Unix() || observation.AmountCaptured <= 0 || observation.AmountRefunded < 0 || observation.AmountRefunded > observation.AmountCaptured {
		return ErrBillingConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var customer string
	err = tx.QueryRowContext(ctx, "SELECT customer_id FROM billing_subscriptions WHERE id=?", observation.SubscriptionID).Scan(&customer)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrBillingUnmatched
	}
	if err != nil {
		return err
	}
	if customer != observation.CustomerID {
		return ErrBillingConflict
	}
	var currentGeneration int64
	var sub, priorCustomer, invoice, payment string
	err = tx.QueryRowContext(ctx, "SELECT generation,COALESCE(subscription_id,''),COALESCE(customer_id,''),invoice_id,payment_intent_id FROM billing_charges WHERE id=?", id).Scan(&currentGeneration, &sub, &priorCustomer, &invoice, &payment)
	if err != nil {
		return err
	}
	if currentGeneration != generation {
		return ErrBillingConflict
	}
	if sub != "" && (sub != observation.SubscriptionID || priorCustomer != customer || invoice != observation.InvoiceID || payment != observation.PaymentIntentID) {
		return ErrBillingConflict
	}
	data, err := json.Marshal(observation)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE billing_charges SET subscription_id=?,customer_id=?,invoice_id=?,payment_intent_id=?,snapshot=? WHERE id=?", observation.SubscriptionID, customer, observation.InvoiceID, observation.PaymentIntentID, data, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) BillingChargeObservation(ctx context.Context, token, workspace, id string) (hostingbilling.ChargeObservation, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return hostingbilling.ChargeObservation{}, err
	}
	defer tx.Rollback()
	if _, err = s.authorizeOwner(ctx, tx, token, workspace); err != nil {
		return hostingbilling.ChargeObservation{}, err
	}
	var data []byte
	err = tx.QueryRowContext(ctx, `SELECT c.snapshot FROM billing_charges c JOIN billing_subscriptions s ON s.id=c.subscription_id WHERE c.id=? AND s.workspace_id=?`, id, workspace).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return hostingbilling.ChargeObservation{}, ErrDenied
	}
	if err != nil {
		return hostingbilling.ChargeObservation{}, err
	}
	var observation hostingbilling.ChargeObservation
	if err = json.Unmarshal(data, &observation); err != nil {
		return observation, err
	}
	return observation, tx.Commit()
}
