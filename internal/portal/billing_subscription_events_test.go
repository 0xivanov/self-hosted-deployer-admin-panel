//go:build integration

package portal

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/hostingbilling"
	stripe "github.com/stripe/stripe-go/v86"
)

func subscriptionEvent(t *testing.T, s *Store, id, kind, customer string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"id": id, "object": "event", "api_version": stripe.APIVersion, "type": kind, "created": 1700000000, "livemode": false, "data": map[string]any{"object": map[string]any{"id": "sub_signal", "object": "subscription", "livemode": false, "customer": customer, "status": "active"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcceptBillingWebhook(t.Context(), body, billingSignature(body), billingSecret); err != nil {
		t.Fatal(err)
	}
}
func TestSubscriptionEventsScheduleCurrentStateAndFenceOldReads(t *testing.T) {
	t.Parallel()
	s, _, a, session := billingCheckoutFixture(t)
	ctx := t.Context()
	subscriptionEvent(t, s, "evt_early_sub", "customer.subscription.created", "cus_owner")
	if _, err := s.ProcessBillingSubscriptionEvent(ctx, "evt_early_sub"); !errors.Is(err, ErrBillingUnmatched) {
		t.Fatal("unbound subscription accepted", err)
	}
	checkout, err := s.RequestBillingCheckout(ctx, session.Token, a.WorkspaceID, "starter")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BindBillingCheckout(ctx, checkout.ID, "cs_test_signal", "https://checkout.stripe.com/c/pay/cs_test_signal"); err != nil {
		t.Fatal(err)
	}
	checkoutEvent(t, s, "evt_bind_signal", "checkout.session.completed", "cs_test_signal", checkout.CustomerID, checkout.ID, "sub_signal")
	if _, err = s.ProcessBillingCheckoutEvent(ctx, "evt_bind_signal"); err != nil {
		t.Fatal(err)
	}
	if done, err := s.ProcessBillingSubscriptionEvent(ctx, "evt_early_sub"); !done || err != nil {
		t.Fatal(done, err)
	}
	p := workerProvider{subscription: func(_ context.Context, id, customer, price string) (hostingbilling.SubscriptionSnapshot, error) {
		return hostingbilling.SubscriptionSnapshot{ID: id, CustomerID: customer, PriceID: price, Status: "canceled", ObservedAt: time.Now().Unix()}, nil
	}}
	if worked, err := s.BillingWorkOnce(ctx, p); !worked || err != nil {
		t.Fatal(worked, err)
	}
	snapshot, err := s.BillingSubscriptionSnapshot(ctx, session.Token, a.WorkspaceID, "sub_signal")
	if err != nil || snapshot == nil || snapshot.Status != "canceled" {
		t.Fatal(snapshot, err)
	}
	subscriptionEvent(t, s, "evt_wrong_sub", "customer.subscription.updated", "cus_foreign")
	if _, err = s.ProcessBillingSubscriptionEvent(ctx, "evt_wrong_sub"); !errors.Is(err, ErrBillingConflict) {
		t.Fatal("wrong customer accepted", err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- s.ReconcileBillingSubscription(ctx, "sub_signal", subscriptionReaderFunc(func(ctx context.Context, id, customer, price string) (hostingbilling.SubscriptionSnapshot, error) {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return hostingbilling.SubscriptionSnapshot{}, ctx.Err()
			}
			return hostingbilling.SubscriptionSnapshot{ID: id, CustomerID: customer, PriceID: price, Status: "active", ObservedAt: time.Now().Unix()}, nil
		}))
	}()
	<-started
	subscriptionEvent(t, s, "evt_deleted_sub", "customer.subscription.deleted", "cus_owner")
	done, eventErr := s.ProcessBillingSubscriptionEvent(ctx, "evt_deleted_sub")
	close(release)
	oldErr := <-result
	if !done || eventErr != nil || !errors.Is(oldErr, ErrBillingConflict) {
		t.Fatal(done, eventErr, oldErr)
	}
	snapshot, err = s.BillingSubscriptionSnapshot(ctx, session.Token, a.WorkspaceID, "sub_signal")
	if err != nil || snapshot != nil {
		t.Fatal("old observation retained", snapshot, err)
	}
	if err = s.ReconcileBillingSubscription(ctx, "sub_signal", p); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ProcessBillingSubscriptionEvent(ctx, "evt_deleted_sub"); err != nil {
		t.Fatal(err)
	}
	snapshot, err = s.BillingSubscriptionSnapshot(ctx, session.Token, a.WorkspaceID, "sub_signal")
	if err != nil || snapshot == nil || snapshot.Status != "canceled" {
		t.Fatal("duplicate event invalidated new observation", snapshot, err)
	}
}
