//go:build integration

package portal

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/hostingbilling"
)

type workerProvider struct {
	customer     func(context.Context, string, string) (string, error)
	checkout     func(context.Context, string, string, string, string) (hostingbilling.Checkout, error)
	subscription subscriptionReaderFunc
}

func (p workerProvider) CreateCustomer(c context.Context, e, r string) (string, error) {
	return p.customer(c, e, r)
}
func (p workerProvider) CreatePinnedCheckout(c context.Context, u, n, v, r string) (hostingbilling.Checkout, error) {
	return p.checkout(c, u, n, v, r)
}
func (p workerProvider) RetrieveSubscription(c context.Context, i, u, v string) (hostingbilling.SubscriptionSnapshot, error) {
	return p.subscription(c, i, u, v)
}

func TestBillingWorkerLifecycleAndRetry(t *testing.T) {
	t.Parallel()
	s, path := newStore(t)
	ctx := t.Context()
	a, session := verifiedAccount(t, s, "worker-owner@example.test")
	request, err := s.RequestBillingCustomer(ctx, session.Token, a.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	now := s.now()
	s.now = func() time.Time { return now }
	calls := 0
	p := workerProvider{customer: func(_ context.Context, email, key string) (string, error) {
		if email != "worker-owner@example.test" || key != request.RequestID {
			t.Error("customer identity changed")
		}
		calls++
		if calls == 1 {
			return "", errors.New("uncertain provider result")
		}
		return "cus_worker", nil
	}}
	if worked, err := s.BillingWorkOnce(ctx, p); !worked || err == nil {
		t.Fatal(worked, err)
	}
	if worked, err := s.BillingWorkOnce(ctx, p); worked || err != nil {
		t.Fatal("retry ignored backoff", worked, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now = now.Add(31 * time.Second)
	s.now = func() time.Time { return now }
	if worked, err := s.BillingWorkOnce(ctx, p); !worked || err != nil {
		t.Fatal(worked, err)
	}
	customer, err := s.BillingCustomer(ctx, session.Token, a.WorkspaceID)
	if err != nil || customer.CustomerID != "cus_worker" || calls != 2 {
		t.Fatal(customer, calls, err)
	}
	if err = s.ConfigureBillingPlan(ctx, "starter", "price_worker", true); err != nil {
		t.Fatal(err)
	}
	intent, err := s.RequestBillingCheckout(ctx, session.Token, a.WorkspaceID, "starter")
	if err != nil {
		t.Fatal(err)
	}
	p.checkout = func(_ context.Context, customer, plan, price, key string) (hostingbilling.Checkout, error) {
		if customer != "cus_worker" || plan != "starter" || price != "price_worker" || key != intent.ID {
			t.Error("checkout identity changed")
		}
		return hostingbilling.Checkout{ID: "cs_test_worker", URL: "https://checkout.stripe.com/c/pay/cs_test_worker"}, nil
	}
	if worked, err := s.BillingWorkOnce(ctx, p); !worked || err != nil {
		t.Fatal(worked, err)
	}
	checkoutEvent(t, s, "evt_worker", "checkout.session.completed", "cs_test_worker", "cus_worker", intent.ID, "sub_worker")
	if worked, err := s.BillingWorkOnce(ctx, p); !worked || err != nil {
		t.Fatal(worked, err)
	}
	p.subscription = func(_ context.Context, id, customer, price string) (hostingbilling.SubscriptionSnapshot, error) {
		return hostingbilling.SubscriptionSnapshot{ID: id, CustomerID: customer, PriceID: price, Status: "active", ObservedAt: now.Unix()}, nil
	}
	if worked, err := s.BillingWorkOnce(ctx, p); !worked || err != nil {
		t.Fatal(worked, err)
	}
	snapshot, err := s.BillingSubscriptionSnapshot(ctx, session.Token, a.WorkspaceID, "sub_worker")
	if err != nil || snapshot == nil || snapshot.Status != "active" {
		t.Fatal(snapshot, err)
	}
	if worked, err := s.BillingWorkOnce(ctx, p); worked || err != nil {
		t.Fatal("unexpected duplicate work", worked, err)
	}
	now = now.Add(301 * time.Second)
	if worked, err := s.BillingWorkOnce(ctx, p); !worked || err != nil {
		t.Fatal("subscription refresh missing", worked, err)
	}
}

func TestBillingWorkerLeaseExcludesConcurrentProviderCreate(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)
	ctx := t.Context()
	a, session := verifiedAccount(t, s, "leased-owner@example.test")
	if _, err := s.RequestBillingCustomer(ctx, session.Token, a.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	p := workerProvider{customer: func(ctx context.Context, _, _ string) (string, error) {
		close(started)
		select {
		case <-release:
			return "cus_leased", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}}
	result := make(chan error, 1)
	go func() { _, err := s.BillingWorkOnce(ctx, p); result <- err }()
	<-started
	worked, err := s.BillingWorkOnce(ctx, p)
	close(release)
	firstErr := <-result
	if worked || err != nil || firstErr != nil {
		t.Fatal(worked, err, firstErr)
	}
}

func TestBillingWorkerRecoversUnacknowledgedProviderResult(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)
	a, session := verifiedAccount(t, s, "recovery-owner@example.test")
	request, err := s.RequestBillingCustomer(t.Context(), session.Token, a.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	now := s.now()
	s.now = func() time.Time { return now }
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	calls := 0
	p := workerProvider{customer: func(_ context.Context, _, key string) (string, error) {
		calls++
		if key != request.RequestID {
			t.Error("retry created a new request identity")
		}
		if calls == 1 {
			cancel()
		}
		return "cus_recovered", nil
	}}
	if worked, err := s.BillingWorkOnce(ctx, p); !worked || err == nil {
		t.Fatal("canceled acknowledgement succeeded", worked, err)
	}
	if worked, err := s.BillingWorkOnce(t.Context(), p); worked || err != nil {
		t.Fatal("unexpired lease reused", worked, err)
	}
	now = now.Add(61 * time.Second)
	if worked, err := s.BillingWorkOnce(t.Context(), p); !worked || err != nil {
		t.Fatal(worked, err)
	}
	customer, err := s.BillingCustomer(t.Context(), session.Token, a.WorkspaceID)
	if err != nil || customer.CustomerID != "cus_recovered" || calls != 2 {
		t.Fatal(customer, calls, err)
	}
}
