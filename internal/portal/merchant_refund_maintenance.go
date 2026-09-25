package portal

import (
	"context"
)

type MerchantRefundMaintenanceResult struct {
	Checked  int
	Failures int
	Cursor   string
}

// MaintainMerchantRefunds refreshes only refunds that already have a provider
// identity. It never creates a refund and advances the cursor past provider
// failures so one unavailable refund cannot starve later rows.
func (s *Store) MaintainMerchantRefunds(ctx context.Context, p MerchantRefundProvider, cursor string, limit int) (MerchantRefundMaintenanceResult, error) {
	result := MerchantRefundMaintenanceResult{Cursor: cursor}
	if err := validateMerchantProviderMode(p); err != nil {
		return result, err
	}
	if limit < 1 || limit > 100 {
		return result, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT "+merchantRefundColumns+" FROM merchant_refunds WHERE provider_id IS NOT NULL AND provider_id<>'' AND id>? ORDER BY id LIMIT ?", cursor, limit)
	if err != nil {
		tx.Rollback()
		return result, err
	}
	refunds := make([]MerchantRefund, 0, limit)
	for rows.Next() {
		r, scanErr := scanMerchantRefund(rows)
		if scanErr != nil {
			rows.Close()
			tx.Rollback()
			return result, scanErr
		}
		refunds = append(refunds, r)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		tx.Rollback()
		return result, err
	}
	if err = rows.Close(); err != nil {
		tx.Rollback()
		return result, err
	}
	if err = tx.Commit(); err != nil {
		return result, err
	}
	if len(refunds) < limit {
		result.Cursor = ""
	} else {
		result.Cursor = refunds[len(refunds)-1].ID
	}
	for _, refund := range refunds {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		_, reconcileErr := s.ReconcileMerchantRefund(ctx, refund.ID, refund.ProviderID, p)
		if reconcileErr == nil {
			result.Checked++
			continue
		}
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		result.Failures++
	}
	return result, nil
}
