package portal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/hostingbilling"
)

type BillingPriceReader interface {
	RetrievePlanPrice(context.Context, string, string) (hostingbilling.PlanPrice, error)
}

// RefreshBillingPriceOnce reserves one due enabled plan before fetching its
// configured provider price. Durable generations fence both competing fetches
// and plan edits. Failed reads retain evidence, but the catalog hides stale data.
func (s *Store) RefreshBillingPriceOnce(ctx context.Context, p BillingPriceReader) (bool, error) {
	if p == nil {
		return false, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	now := s.now().Unix()
	var plan, price string
	var generation int64
	err := s.db.QueryRowContext(ctx, `UPDATE billing_plans SET next_refresh=?,price_generation=price_generation+1 WHERE id=(SELECT id FROM billing_plans WHERE enabled=1 AND next_refresh<=? ORDER BY next_refresh,id LIMIT 1) RETURNING id,price_id,price_generation`, now+60, now).Scan(&plan, &price, &generation)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	snapshot, err := p.RetrievePlanPrice(ctx, plan, price)
	if err != nil {
		return true, err
	}
	if snapshot.Plan != plan || snapshot.PriceID != price || snapshot.ObservedAt < now || snapshot.ObservedAt > s.now().Unix() {
		return true, ErrBillingConflict
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return true, err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE billing_plans SET price_snapshot=?,price_observed=?,next_refresh=? WHERE id=? AND price_id=? AND enabled=1 AND price_generation=?`, data, snapshot.ObservedAt, s.now().Unix()+300, plan, price, generation)
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
	return true, nil
}

type BillingPlanOffer struct {
	Plan  string                    `json:"plan"`
	Price *hostingbilling.PlanPrice `json:"price"`
}

// BillingPlanOffers returns only fresh price observations, alongside enabled
// plan IDs. A nil price means not yet verified or temporarily unavailable.
func (s *Store) BillingPlanOffers(ctx context.Context, token, workspace string) ([]BillingPlanOffer, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = s.authorizeOwner(ctx, tx, token, workspace); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,price_snapshot,price_observed FROM billing_plans WHERE enabled=1 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	offers := []BillingPlanOffer{}
	now := s.now().Unix()
	for rows.Next() {
		var offer BillingPlanOffer
		var data []byte
		var observed int64
		if err = rows.Scan(&offer.Plan, &data, &observed); err != nil {
			rows.Close()
			return nil, err
		}
		if len(data) > 0 && observed <= now && observed > now-900 {
			var price hostingbilling.PlanPrice
			if err = json.Unmarshal(data, &price); err != nil {
				rows.Close()
				return nil, err
			}
			offer.Price = &price
		}
		offers = append(offers, offer)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return offers, tx.Commit()
}
