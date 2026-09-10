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

type chargeWorkerProvider struct {
	workerProvider
	chargeReaderFunc
}

func TestChargeEventsFetchCurrentProviderState(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"charge.refunded", "charge.dispute.created", "charge.dispute.updated", "charge.dispute.closed", "charge.dispute.funds_withdrawn", "charge.dispute.funds_reinstated"} {
		t.Run(kind, func(t *testing.T) {
			s, _, a, session := billingCheckoutFixture(t)
			ctx := t.Context()
			c, err := s.RequestBillingCheckout(ctx, session.Token, a.WorkspaceID, "starter")
			if err != nil {
				t.Fatal(err)
			}
			if err = s.BindBillingCheckout(ctx, c.ID, "cs_test_risk", "https://checkout.stripe.com/c/pay/cs_test_risk"); err != nil {
				t.Fatal(err)
			}
			checkoutEvent(t, s, "evt_risk_bind", "checkout.session.completed", "cs_test_risk", c.CustomerID, c.ID, "sub_risk")
			if _, err = s.ProcessBillingCheckoutEvent(ctx, "evt_risk_bind"); err != nil {
				t.Fatal(err)
			}
			object := map[string]any{"id": "dp_risk", "object": "dispute", "livemode": false, "charge": "ch_risk", "status": "won"}
			if kind == "charge.refunded" {
				object = map[string]any{"id": "ch_risk", "object": "charge", "livemode": false, "amount_refunded": 1}
			}
			body, err := json.Marshal(map[string]any{"id": "evt_risk", "object": "event", "api_version": stripe.APIVersion, "type": kind, "created": 1700000000, "livemode": false, "data": map[string]any{"object": object}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.AcceptBillingWebhook(ctx, body, billingSignature(body), billingSecret); err != nil {
				t.Fatal(err)
			}
			calls := 0
			reader := chargeReaderFunc(func(_ context.Context, id string) (hostingbilling.ChargeObservation, error) {
				calls++
				if id != "ch_risk" {
					t.Error("incorrect charge lookup")
				}
				return hostingbilling.ChargeObservation{ChargeID: id, CustomerID: c.CustomerID, PaymentIntentID: "pi_risk", InvoiceID: "in_risk", SubscriptionID: "sub_risk", Currency: "eur", AmountCaptured: 1000, AmountRefunded: 500, Disputed: true, ObservedAt: time.Now().Unix()}, nil
			})
			failed := chargeReaderFunc(func(context.Context, string) (hostingbilling.ChargeObservation, error) {
				return hostingbilling.ChargeObservation{}, errors.New("unavailable")
			})
			if done, err := s.ProcessBillingChargeEvent(ctx, "evt_risk", failed); done || err == nil {
				t.Fatal("failed lookup acknowledged", done, err)
			}
			if worked, err := s.BillingWorkOnce(ctx, chargeWorkerProvider{chargeReaderFunc: reader}); !worked || err != nil {
				t.Fatal(worked, err)
			}
			got, err := s.BillingChargeObservation(ctx, session.Token, a.WorkspaceID, "ch_risk")
			if err != nil || !got.Disputed || got.AmountRefunded != 500 {
				t.Fatal("event payload replaced current state", got, err)
			}
			if done, err := s.ProcessBillingChargeEvent(ctx, "evt_risk", reader); !done || err != nil || calls != 1 {
				t.Fatal("duplicate repeated provider call", done, err, calls)
			}
			var pending int
			if err = s.db.QueryRow("SELECT count(*) FROM billing_work WHERE kind='subscription' AND reference='sub_risk' AND done=0 AND next_attempt=0").Scan(&pending); err != nil || pending != 1 {
				t.Fatal("refresh missing", pending, err)
			}
		})
	}
}
