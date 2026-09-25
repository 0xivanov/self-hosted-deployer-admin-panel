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

type liveWorkflowProvider struct{}

func (liveWorkflowProvider) BillingMode() string { return "live" }
func (liveWorkflowProvider) CreateCustomer(_ context.Context, email, request string) (string, error) {
	if email == "" || request == "" {
		return "", errors.New("missing customer identity")
	}
	return "cus_live_workflow", nil
}
func (liveWorkflowProvider) CreatePinnedCheckout(_ context.Context, customer, plan, price, request string) (hostingbilling.Checkout, error) {
	if customer != "cus_live_workflow" || plan != "starter" || price != "price_live" || request == "" {
		return hostingbilling.Checkout{}, errors.New("unexpected checkout identity")
	}
	return hostingbilling.Checkout{ID: "cs_live_workflow", URL: "https://checkout.stripe.com/c/pay/cs_live_workflow"}, nil
}
func (liveWorkflowProvider) RetrieveSubscription(_ context.Context, id, customer, price string) (hostingbilling.SubscriptionSnapshot, error) {
	if id != "sub_live_workflow" || customer != "cus_live_workflow" || price != "price_live" {
		return hostingbilling.SubscriptionSnapshot{}, errors.New("unexpected subscription identity")
	}
	return hostingbilling.SubscriptionSnapshot{ID: id, CustomerID: customer, PriceID: price, Status: "active", ObservedAt: time.Now().Unix()}, nil
}

func liveWorkflowCheckoutEvent(t *testing.T, s *Store, id, checkoutID, customer, reference string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"id": id, "object": "event", "api_version": stripe.APIVersion, "type": "checkout.session.completed", "created": 1700000000, "livemode": true,
		"data": map[string]any{"object": map[string]any{
			"id": checkoutID, "object": "checkout.session", "livemode": true, "mode": "subscription", "status": "complete",
			"customer": customer, "client_reference_id": reference, "subscription": "sub_live_workflow",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcceptBillingWebhook(t.Context(), body, billingSignature(body), billingSecret); err != nil {
		t.Fatal(err)
	}
}

func TestLiveBillingWorkflowAndModeIsolation(t *testing.T) {
	t.Parallel()
	s, path, owner, session := billingCheckoutFixture(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	live, err := OpenWithBillingMode(path, "live")
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	ctx := t.Context()
	if err = live.ConfigureBillingPlan(ctx, "starter", "price_live", true); err != nil {
		t.Fatal(err)
	}
	customer, err := live.RequestBillingCustomer(ctx, session.Token, owner.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	provider := liveWorkflowProvider{}
	if worked, err := live.BillingWorkOnce(ctx, provider); !worked || err != nil {
		t.Fatal(worked, err)
	}
	if customer, err = live.BillingCustomer(ctx, session.Token, owner.WorkspaceID); err != nil || customer.CustomerID != "cus_live_workflow" {
		t.Fatal(customer, err)
	}
	checkout, err := live.RequestBillingCheckout(ctx, session.Token, owner.WorkspaceID, "starter")
	if err != nil {
		t.Fatal(err)
	}
	if worked, err := live.BillingWorkOnce(ctx, provider); !worked || err != nil {
		t.Fatal(worked, err)
	}
	liveWorkflowCheckoutEvent(t, live, "evt_live_workflow", "cs_live_workflow", "cus_live_workflow", checkout.ID)
	if worked, err := live.BillingWorkOnce(ctx, provider); !worked || err != nil {
		t.Fatal(worked, err)
	}
	if worked, err := live.BillingWorkOnce(ctx, provider); !worked || err != nil {
		t.Fatal(worked, err)
	}
	snapshot, err := live.BillingSubscriptionSnapshot(ctx, session.Token, owner.WorkspaceID, "sub_live_workflow")
	if err != nil || snapshot == nil || snapshot.Status != "active" {
		t.Fatal(snapshot, err)
	}
	if err = live.Close(); err != nil {
		t.Fatal(err)
	}
	testStore, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer testStore.Close()
	if _, err = testStore.BillingCustomer(ctx, session.Token, owner.WorkspaceID); err != nil {
		t.Fatal("sandbox customer unavailable", err)
	}
	if _, err = testStore.BillingCheckout(ctx, session.Token, owner.WorkspaceID, checkout.ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("sandbox saw live checkout: %v", err)
	}
}
