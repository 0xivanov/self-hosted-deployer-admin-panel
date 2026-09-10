//go:build integration

package portal

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/hostingbilling"
)

type priceReaderFunc func(context.Context, string, string) (hostingbilling.PlanPrice, error)

func (f priceReaderFunc) RetrievePlanPrice(ctx context.Context, plan, price string) (hostingbilling.PlanPrice, error) {
	return f(ctx, plan, price)
}

func TestPriceCatalogPersistenceFreshnessAndPlanEdits(t *testing.T) {
	t.Parallel()
	s, path, a, session := billingCheckoutFixture(t)
	ctx := t.Context()
	now := s.now()
	s.now = func() time.Time { return now }
	read := priceReaderFunc(func(_ context.Context, plan, price string) (hostingbilling.PlanPrice, error) {
		return hostingbilling.PlanPrice{Plan: plan, PriceID: price, AmountMinor: 1500, Currency: "eur", Interval: "month", IntervalCount: 1, TaxBehavior: "exclusive", ObservedAt: now.Unix()}, nil
	})
	if worked, err := s.RefreshBillingPriceOnce(ctx, read); !worked || err != nil {
		t.Fatal(worked, err)
	}
	if worked, err := s.RefreshBillingPriceOnce(ctx, read); worked || err != nil {
		t.Fatal("refresh ignored interval", worked, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.now = func() time.Time { return now }
	offers, err := s.BillingPlanOffers(ctx, session.Token, a.WorkspaceID)
	if err != nil || len(offers) != 1 || offers[0].Price == nil || offers[0].Price.AmountMinor != 1500 {
		t.Fatal(offers, err)
	}
	now = now.Add(901 * time.Second)
	if worked, err := s.RefreshBillingPriceOnce(ctx, priceReaderFunc(func(context.Context, string, string) (hostingbilling.PlanPrice, error) {
		return hostingbilling.PlanPrice{}, errors.New("offline")
	})); !worked || err == nil {
		t.Fatal(worked, err)
	}
	offers, err = s.BillingPlanOffers(ctx, session.Token, a.WorkspaceID)
	if err != nil || offers[0].Price != nil {
		t.Fatal("stale price exposed", offers, err)
	}
	if worked, err := s.RefreshBillingPriceOnce(ctx, read); worked || err != nil {
		t.Fatal("failed refresh ignored backoff", worked, err)
	}
	now = now.Add(61 * time.Second)
	if worked, err := s.RefreshBillingPriceOnce(ctx, read); !worked || err != nil {
		t.Fatal(worked, err)
	}
	if err = s.ConfigureBillingPlan(ctx, "starter", "price_changed", true); err != nil {
		t.Fatal(err)
	}
	offers, err = s.BillingPlanOffers(ctx, session.Token, a.WorkspaceID)
	if err != nil || offers[0].Price != nil {
		t.Fatal("repriced plan retained old amount", offers, err)
	}
	if err = s.ConfigureBillingPlan(ctx, "starter", "price_changed", false); err != nil {
		t.Fatal(err)
	}
	offers, err = s.BillingPlanOffers(ctx, session.Token, a.WorkspaceID)
	if err != nil || len(offers) != 0 {
		t.Fatal("disabled plan exposed", offers, err)
	}
}

func TestPriceFetchCannotOverwriteChangedPlan(t *testing.T) {
	t.Parallel()
	s, _, a, session := billingCheckoutFixture(t)
	ctx := t.Context()
	started, release := make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	go func() {
		_, err := s.RefreshBillingPriceOnce(ctx, priceReaderFunc(func(ctx context.Context, plan, price string) (hostingbilling.PlanPrice, error) {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return hostingbilling.PlanPrice{}, ctx.Err()
			}
			return hostingbilling.PlanPrice{Plan: plan, PriceID: price, ObservedAt: time.Now().Unix()}, nil
		}))
		result <- err
	}()
	<-started
	err := s.ConfigureBillingPlan(ctx, "starter", "price_replaced", true)
	close(release)
	oldErr := <-result
	if err != nil || !errors.Is(oldErr, ErrBillingConflict) {
		t.Fatal(err, oldErr)
	}
	offers, err := s.BillingPlanOffers(ctx, session.Token, a.WorkspaceID)
	if err != nil || offers[0].Price != nil {
		t.Fatal(offers, err)
	}
	_, other := verifiedAccount(t, s, "price-other@example.test")
	if _, err = s.BillingPlanOffers(ctx, other.Token, a.WorkspaceID); !errors.Is(err, ErrDenied) {
		t.Fatal("foreign owner read", err)
	}
}
