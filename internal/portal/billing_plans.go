package portal

import "context"

// AvailableBillingPlans lists enabled plan identifiers for a workspace owner.
// These are not price quotes: the provider checkout presents the actual price.
// Provider Price IDs remain private and are resolved when an intent is saved.
func (s *Store) AvailableBillingPlans(ctx context.Context, token, workspace string) ([]string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = s.authorizeOwner(ctx, tx, token, workspace); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT id FROM billing_plans WHERE enabled=1 ORDER BY id")
	if err != nil {
		return nil, err
	}
	plans := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		plans = append(plans, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return plans, tx.Commit()
}
