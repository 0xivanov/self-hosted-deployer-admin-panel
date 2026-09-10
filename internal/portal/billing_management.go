package portal

import (
	"context"
	"net/url"
	"time"
)

type BillingManagement interface {
	CreateManagementSession(context.Context, string) (string, error)
}

// BillingManagementURL rechecks owner access after obtaining the ephemeral link.
// The link is never stored in the database or audit trail.
func (s *Store) BillingManagementURL(ctx context.Context, p BillingManagement, token, workspace string) (string, error) {
	if p == nil {
		return "", ErrDenied
	}
	customer, err := s.BillingCustomer(ctx, token, workspace)
	if err != nil {
		return "", err
	}
	if customer.CustomerID == "" {
		return "", ErrBillingConflict
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	link, err := p.CreateManagementSession(ctx, customer.CustomerID)
	if err != nil {
		return "", err
	}
	current, err := s.BillingCustomer(ctx, token, workspace)
	if err != nil {
		return "", err
	}
	if current.CustomerID != customer.CustomerID {
		return "", ErrBillingConflict
	}
	u, err := url.Parse(link)
	if err != nil || u.Scheme != "https" || u.Host != "billing.stripe.com" || u.User != nil || len(link) > 8192 {
		return "", ErrBillingConflict
	}
	return link, nil
}
