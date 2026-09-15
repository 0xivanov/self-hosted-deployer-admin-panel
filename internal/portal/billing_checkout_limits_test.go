//go:build integration

package portal

import (
	"errors"
	"reflect"
	"testing"
)

func TestBillingCheckoutHostingLimitsSnapshotSurvivesEditsRestartAndReplay(t *testing.T) {
	t.Parallel()
	s, path, account, session := billingCheckoutFixture(t)
	ctx := t.Context()
	initial := HostingPlanLimits{Projects: 3, Uploads: 4, UploadBytes: 5 * 1024 * 1024, Node: true}
	if err := s.ConfigureHostingLimits(ctx, "starter", initial); err != nil {
		t.Fatal(err)
	}

	checkout, err := s.RequestBillingCheckout(ctx, session.Token, account.WorkspaceID, "starter")
	if err != nil {
		t.Fatal(err)
	}
	if checkout.Limits == nil || *checkout.Limits != initial {
		t.Fatalf("checkout limits = %#v, want %#v", checkout.Limits, initial)
	}

	changed := HostingPlanLimits{Projects: 1, Uploads: 1, UploadBytes: 1024 * 1024, Node: false}
	if err = s.ConfigureHostingLimits(ctx, "starter", changed); err != nil {
		t.Fatal(err)
	}
	retry, err := s.RequestBillingCheckout(ctx, session.Token, account.WorkspaceID, "starter")
	if err != nil || retry.Limits == nil || *retry.Limits != initial {
		t.Fatalf("retry limits = %#v, err = %v", retry.Limits, err)
	}

	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	replayed, err := s.BillingCheckoutWork(ctx, checkout.ID)
	if err != nil || !reflect.DeepEqual(replayed, checkout) {
		t.Fatalf("replayed checkout = %#v, err = %v; want %#v", replayed, err, checkout)
	}
	saved, err := s.BillingCheckout(ctx, session.Token, account.WorkspaceID, checkout.ID)
	if err != nil || !reflect.DeepEqual(saved, checkout) {
		t.Fatalf("saved checkout = %#v, err = %v; want %#v", saved, err, checkout)
	}
}

func TestBillingCheckoutWithoutConfiguredHostingLimitsStaysDefaultAfterConfiguration(t *testing.T) {
	t.Parallel()
	s, path := newStore(t)
	ctx := t.Context()
	account, session := verifiedAccount(t, s, "checkout-default-limits@example.test")
	customer, err := s.RequestBillingCustomer(ctx, session.Token, account.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BindBillingCustomer(ctx, customer.RequestID, "cus_default_limits"); err != nil {
		t.Fatal(err)
	}
	if err = s.ConfigureBillingPlan(ctx, "legacy", "price_legacy", true); err != nil {
		t.Fatal(err)
	}

	checkout, err := s.RequestBillingCheckout(ctx, session.Token, account.WorkspaceID, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if checkout.Limits != nil {
		t.Fatalf("unconfigured checkout limits = %#v, want nil", checkout.Limits)
	}
	configured := HostingPlanLimits{Projects: 2, Uploads: 2, UploadBytes: 2 * 1024 * 1024, Node: false}
	if err = s.ConfigureHostingLimits(ctx, "legacy", configured); err != nil {
		t.Fatal(err)
	}
	retry, err := s.RequestBillingCheckout(ctx, session.Token, account.WorkspaceID, "legacy")
	if err != nil || retry.Limits != nil {
		t.Fatalf("retry limits = %#v, err = %v; want nil", retry.Limits, err)
	}

	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	saved, err := s.BillingCheckout(ctx, session.Token, account.WorkspaceID, checkout.ID)
	if err != nil || saved.Limits != nil {
		t.Fatalf("reloaded limits = %#v, err = %v; want nil", saved.Limits, err)
	}
	if _, err = s.BillingCheckout(ctx, session.Token, account.WorkspaceID, "missing"); !errors.Is(err, ErrDenied) {
		t.Fatalf("missing checkout error = %v", err)
	}
}
