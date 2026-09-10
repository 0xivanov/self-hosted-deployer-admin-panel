//go:build integration

package portal

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/hostingbilling"
)

type chargeReaderFunc func(context.Context, string) (hostingbilling.ChargeObservation, error)

func (f chargeReaderFunc) RetrieveChargeObservation(ctx context.Context, id string) (hostingbilling.ChargeObservation, error) {
	return f(ctx, id)
}

func TestDurableChargeMappingAndFencing(t *testing.T) {
	t.Parallel()
	s, path, a, session := billingCheckoutFixture(t)
	ctx := t.Context()
	checkout, err := s.RequestBillingCheckout(ctx, session.Token, a.WorkspaceID, "starter")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BindBillingCheckout(ctx, checkout.ID, "cs_test_charge", "https://checkout.stripe.com/c/pay/cs_test_charge"); err != nil {
		t.Fatal(err)
	}
	checkoutEvent(t, s, "evt_charge_bind", "checkout.session.completed", "cs_test_charge", checkout.CustomerID, checkout.ID, "sub_charge")
	if _, err = s.ProcessBillingCheckoutEvent(ctx, "evt_charge_bind"); err != nil {
		t.Fatal(err)
	}
	fixture := func(id string) hostingbilling.ChargeObservation {
		return hostingbilling.ChargeObservation{ChargeID: id, CustomerID: checkout.CustomerID, PaymentIntentID: "pi_saved", InvoiceID: "in_saved", SubscriptionID: "sub_charge", Currency: "eur", AmountCaptured: 1000, AmountRefunded: 250, ObservedAt: time.Now().Unix()}
	}
	reader := chargeReaderFunc(func(_ context.Context, id string) (hostingbilling.ChargeObservation, error) { return fixture(id), nil })
	if err = s.ReconcileBillingCharge(ctx, reader, "ch_saved"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*hostingbilling.ChargeObservation)
	}{
		{"foreign customer", func(v *hostingbilling.ChargeObservation) { v.CustomerID = "cus_foreign" }},
		{"unknown subscription", func(v *hostingbilling.ChargeObservation) { v.SubscriptionID = "sub_foreign" }},
		{"changed invoice", func(v *hostingbilling.ChargeObservation) { v.InvoiceID = "in_replaced" }},
		{"changed payment", func(v *hostingbilling.ChargeObservation) { v.PaymentIntentID = "pi_replaced" }},
		{"stale response", func(v *hostingbilling.ChargeObservation) { v.ObservedAt -= 3600 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := s.ReconcileBillingCharge(ctx, chargeReaderFunc(func(_ context.Context, id string) (hostingbilling.ChargeObservation, error) {
				v := fixture(id)
				tc.change(&v)
				return v, nil
			}), "ch_saved")
			if err == nil {
				t.Fatal("unsafe mapping accepted")
			}
		})
	}
	started, release := make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- s.ReconcileBillingCharge(ctx, chargeReaderFunc(func(ctx context.Context, id string) (hostingbilling.ChargeObservation, error) {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return hostingbilling.ChargeObservation{}, ctx.Err()
			}
			return fixture(id), nil
		}), "ch_saved")
	}()
	<-started
	err = s.ReconcileBillingCharge(ctx, chargeReaderFunc(func(_ context.Context, id string) (hostingbilling.ChargeObservation, error) {
		v := fixture(id)
		v.Disputed = true
		return v, nil
	}), "ch_saved")
	close(release)
	oldErr := <-result
	if err != nil || !errors.Is(oldErr, ErrBillingConflict) {
		t.Fatal(err, oldErr)
	}
	b, other := verifiedAccount(t, s, "charge-other@example.test")
	if _, err = s.BillingChargeObservation(ctx, other.Token, a.WorkspaceID, "ch_saved"); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("INSERT INTO memberships VALUES(?,?,'developer')", b.ID, a.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.BillingChargeObservation(ctx, other.Token, a.WorkspaceID, "ch_saved"); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	observation, err := s.BillingChargeObservation(ctx, session.Token, a.WorkspaceID, "ch_saved")
	if err != nil || !observation.Disputed || observation.AmountRefunded != 250 || observation.InvoiceID != "in_saved" {
		t.Fatal(observation, err)
	}
}
