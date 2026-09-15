package portal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/hostingbilling"
)

// ErrHostingPayment means that a workspace configured for test-subscription
// hosting has no currently valid payment evidence.
var ErrHostingPayment = errors.New("hosting payment evidence unavailable")

type HostingAccess struct {
	Mode    string `json:"mode"`
	Allowed bool   `json:"allowed"`
}

// ConfigureHostingPolicy is trusted operator configuration. It deliberately
// has no customer token argument and is intended for the private operator CLI.
func (s *Store) ConfigureHostingPolicy(ctx context.Context, workspace string, required bool) error {
	if workspace == "" {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists int
	if err = tx.QueryRowContext(ctx, "SELECT 1 FROM workspaces WHERE id=?", workspace).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return ErrDenied
	} else if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO hosting_workspace_policies(workspace_id,require_test_subscription) VALUES(?,?) ON CONFLICT(workspace_id) DO UPDATE SET require_test_subscription=excluded.require_test_subscription`, workspace, required); err != nil {
		return err
	}
	if err = audit(ctx, tx, "operator", workspace, "hosting.policy.configured", s.now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

// requireHostingAccess evaluates the durable evidence for one workspace. A
// legacy workspace remains available when no policy exists or the policy is off.
func (s *Store) requireHostingAccess(ctx context.Context, tx *sql.Tx, workspace string) error {
	var required int
	err := tx.QueryRowContext(ctx, "SELECT require_test_subscription FROM hosting_workspace_policies WHERE workspace_id=?", workspace).Scan(&required)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if required == 0 {
		return nil
	}
	now := s.now().Unix()
	rows, err := tx.QueryContext(ctx, `SELECT s.id,s.customer_id,s.price_id,s.snapshot,c.customer_id,c.price_id
		FROM billing_subscriptions s
		JOIN billing_checkouts c ON c.id=s.checkout_id AND c.workspace_id=s.workspace_id AND c.customer_id=s.customer_id AND c.plan_id=s.plan_id AND c.price_id=s.price_id AND c.state='completed'
		JOIN billing_customers bc ON bc.workspace_id=s.workspace_id AND bc.customer_id=s.customer_id
		WHERE s.workspace_id=?`, workspace)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, customer, price, checkoutCustomer, checkoutPrice string
		var subData []byte
		if err = rows.Scan(&id, &customer, &price, &subData, &checkoutCustomer, &checkoutPrice); err != nil {
			return err
		}
		if subData == nil || checkoutCustomer == "" || checkoutCustomer != customer || checkoutPrice == "" {
			continue
		}
		var snapshot hostingbilling.SubscriptionSnapshot
		if err = json.Unmarshal(subData, &snapshot); err != nil {
			return fmt.Errorf("invalid subscription snapshot: %w", err)
		}
		if snapshot.ID != id || snapshot.CustomerID != customer || snapshot.PriceID != price || snapshot.PriceID != checkoutPrice {
			return fmt.Errorf("subscription snapshot identity mismatch")
		}
		if snapshot.Status != "active" || snapshot.CollectionPaused || snapshot.PeriodStart <= 0 || snapshot.PeriodEnd <= snapshot.PeriodStart || now < snapshot.PeriodStart || now >= snapshot.PeriodEnd || snapshot.InvoiceID == "" || snapshot.InvoiceStatus != "paid" || snapshot.InvoiceRemaining != 0 || !freshObservation(snapshot.ObservedAt, now) {
			continue
		}
		var count int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM billing_charges WHERE subscription_id=? AND customer_id=? AND invoice_id=?", id, customer, snapshot.InvoiceID).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			if count > 1 {
				return fmt.Errorf("ambiguous billing charges for invoice")
			}
			continue
		}
		var chargeID, chargeSub, chargeCustomer, invoiceID, paymentID string
		var chargeData []byte
		if err = tx.QueryRowContext(ctx, "SELECT id,subscription_id,customer_id,invoice_id,payment_intent_id,snapshot FROM billing_charges WHERE subscription_id=? AND customer_id=? AND invoice_id=?", id, customer, snapshot.InvoiceID).Scan(&chargeID, &chargeSub, &chargeCustomer, &invoiceID, &paymentID, &chargeData); err != nil {
			return err
		}
		if chargeData == nil || chargeSub != id || chargeCustomer != customer || invoiceID != snapshot.InvoiceID || paymentID == "" {
			continue
		}
		var observation hostingbilling.ChargeObservation
		if err = json.Unmarshal(chargeData, &observation); err != nil {
			return fmt.Errorf("invalid charge snapshot: %w", err)
		}
		if observation.ChargeID != chargeID || observation.SubscriptionID != id || observation.CustomerID != customer || observation.InvoiceID != snapshot.InvoiceID || observation.PaymentIntentID != paymentID || observation.AmountCaptured <= 0 || observation.AmountRefunded < 0 || observation.AmountRefunded > observation.AmountCaptured {
			return fmt.Errorf("charge snapshot identity mismatch")
		}
		if !freshObservation(observation.ObservedAt, now) || observation.Disputed || !observation.DisputesChecked || observation.AmountRefunded != 0 {
			continue
		}
		validDisputes := true
		for _, dispute := range observation.Disputes {
			if dispute.ID == "" || (dispute.Status != "won" && dispute.Status != "warning_closed") {
				validDisputes = false
				break
			}
		}
		if validDisputes {
			return nil
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	return ErrHostingPayment
}

func freshObservation(observed, now int64) bool {
	return observed > 0 && observed <= now && now-observed < 900
}

func (s *Store) requireProjectHostingAccess(ctx context.Context, tx *sql.Tx, project string) error {
	var workspace string
	if err := tx.QueryRowContext(ctx, "SELECT workspace_id FROM projects WHERE id=?", project).Scan(&workspace); errors.Is(err, sql.ErrNoRows) {
		return ErrDenied
	} else if err != nil {
		return err
	}
	return s.requireHostingAccess(ctx, tx, workspace)
}

// WorkspaceHostingAccess permits any authenticated workspace role to inspect
// access state while keeping the payment decision in the same transaction.
func (s *Store) WorkspaceHostingAccess(ctx context.Context, token, workspace string) (HostingAccess, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return HostingAccess{}, err
	}
	defer tx.Rollback()
	if _, err = s.authorize(ctx, tx, token, workspace, false); err != nil {
		return HostingAccess{}, err
	}
	var required int
	err = tx.QueryRowContext(ctx, "SELECT require_test_subscription FROM hosting_workspace_policies WHERE workspace_id=?", workspace).Scan(&required)
	if errors.Is(err, sql.ErrNoRows) {
		required = 0
		err = nil
	} else if err != nil {
		return HostingAccess{}, err
	}
	access := HostingAccess{Mode: "legacy", Allowed: true}
	if required != 0 {
		access.Mode = "test_subscription"
		err = s.requireHostingAccess(ctx, tx, workspace)
		if errors.Is(err, ErrHostingPayment) {
			access.Allowed = false
			err = nil
		}
	}
	if err != nil {
		return HostingAccess{}, err
	}
	return access, tx.Commit()
}
