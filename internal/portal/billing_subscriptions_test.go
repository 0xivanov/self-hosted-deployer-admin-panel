//go:build integration

package portal

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/hostingbilling"
)

type subscriptionReaderFunc func(context.Context, string, string, string) (hostingbilling.SubscriptionSnapshot, error)

func (f subscriptionReaderFunc) RetrieveSubscription(ctx context.Context, id, customer, price string) (hostingbilling.SubscriptionSnapshot, error) {
	return f(ctx, id, customer, price)
}

func TestSubscriptionReconciliationFencingAndPersistence(t *testing.T) {
	t.Parallel()
	s, path, a, session := billingCheckoutFixture(t)
	ctx := t.Context()
	c, err := s.RequestBillingCheckout(ctx, session.Token, a.WorkspaceID, "starter")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BindBillingCheckout(ctx, c.ID, "cs_test_saved", "https://checkout.stripe.com/c/pay/cs_test_saved"); err != nil {
		t.Fatal(err)
	}
	checkoutEvent(t, s, "evt_complete", "checkout.session.completed", "cs_test_saved", c.CustomerID, c.ID, "sub_saved")
	if _, err = s.ProcessBillingCheckoutEvent(ctx, "evt_complete"); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.BillingSubscriptionSnapshot(ctx, session.Token, a.WorkspaceID, "sub_saved")
	if err != nil || snapshot != nil {
		t.Fatal(snapshot, err)
	}
	read := func(status string) subscriptionReaderFunc {
		return func(_ context.Context, id, customer, price string) (hostingbilling.SubscriptionSnapshot, error) {
			return hostingbilling.SubscriptionSnapshot{ID: id, CustomerID: customer, PriceID: price, Status: status, ObservedAt: time.Now().Unix()}, nil
		}
	}
	started, release := make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- s.ReconcileBillingSubscription(ctx, "sub_saved", subscriptionReaderFunc(func(ctx context.Context, id, customer, price string) (hostingbilling.SubscriptionSnapshot, error) {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return hostingbilling.SubscriptionSnapshot{}, ctx.Err()
			}
			return read("active")(ctx, id, customer, price)
		}))
	}()
	<-started
	err = s.ReconcileBillingSubscription(ctx, "sub_saved", read("past_due"))
	close(release)
	oldErr := <-result
	if err != nil || !errors.Is(oldErr, ErrBillingConflict) {
		t.Fatal(err, oldErr)
	}
	for _, tc := range []struct {
		name   string
		reader subscriptionReaderFunc
	}{
		{"provider failure", func(context.Context, string, string, string) (hostingbilling.SubscriptionSnapshot, error) {
			return hostingbilling.SubscriptionSnapshot{}, errors.New("offline")
		}},
		{"foreign identity", func(ctx context.Context, id, customer, price string) (hostingbilling.SubscriptionSnapshot, error) {
			v, _ := read("active")(ctx, id, customer, price)
			v.CustomerID = "cus_foreign"
			return v, nil
		}},
		{"stale observation", func(ctx context.Context, id, customer, price string) (hostingbilling.SubscriptionSnapshot, error) {
			v, _ := read("active")(ctx, id, customer, price)
			v.ObservedAt -= 3600
			return v, nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.ReconcileBillingSubscription(ctx, "sub_saved", tc.reader); err == nil {
				t.Fatal("invalid lookup accepted")
			}
		})
	}
	b, other := verifiedAccount(t, s, "subscription-other@example.test")
	if _, err = s.BillingSubscriptionSnapshot(ctx, other.Token, a.WorkspaceID, "sub_saved"); !errors.Is(err, ErrDenied) {
		t.Fatal("foreign read", err)
	}
	if _, err = s.db.Exec("INSERT INTO memberships VALUES(?,?,'developer')", b.ID, a.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.BillingSubscriptionSnapshot(ctx, other.Token, a.WorkspaceID, "sub_saved"); !errors.Is(err, ErrDenied) {
		t.Fatal("developer read", err)
	}
	if _, err = s.BillingSubscriptionSnapshot(ctx, other.Token, b.WorkspaceID, "sub_saved"); !errors.Is(err, ErrDenied) {
		t.Fatal("wrong workspace", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	snapshot, err = reopened.BillingSubscriptionSnapshot(ctx, session.Token, a.WorkspaceID, "sub_saved")
	if err != nil || snapshot == nil || snapshot.Status != "past_due" {
		t.Fatal(snapshot, err)
	}
	discovery := discoveringReader{subscriptionReaderFunc: func(ctx context.Context, id, customer, price string) (hostingbilling.SubscriptionSnapshot, error) {
		v, err := read("active")(ctx, id, customer, price)
		v.InvoiceID = "in_latest"
		return v, err
	}}
	if err = reopened.ReconcileBillingSubscription(ctx, "sub_saved", discovery); err != nil {
		t.Fatal(err)
	}
	var tracked int
	if err = reopened.db.QueryRow("SELECT count(*) FROM billing_charges WHERE id='ch_discovered' AND next_refresh=0 AND snapshot IS NULL").Scan(&tracked); err != nil || tracked != 1 {
		t.Fatal("discovered charge was not queued", tracked, err)
	}
	var state string
	if err = reopened.db.QueryRow("SELECT state FROM billing_subscriptions WHERE id='sub_saved'").Scan(&state); err != nil || state != "awaiting_reconciliation" {
		t.Fatal("snapshot granted entitlement", state, err)
	}
}

type discoveringReader struct{ subscriptionReaderFunc }

func (d discoveringReader) DiscoverInvoiceCharge(context.Context, string, string, string) (string, error) {
	return "ch_discovered", nil
}
