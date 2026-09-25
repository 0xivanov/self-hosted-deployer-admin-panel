//go:build integration

package portal

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/hostingbilling"
	stripe "github.com/stripe/stripe-go/v86"
)

func modeBillingPayload(t *testing.T, id string, live bool) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"id": id, "object": "event", "type": "invoice.paid", "api_version": stripe.APIVersion,
		"created": 1700000000, "livemode": live, "data": map[string]any{
			"object": map[string]any{"id": "in_mode", "object": "invoice", "customer": "cus_mode", "livemode": live},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func modeBillingSignature(body []byte) string {
	stamp := time.Now().Unix()
	mac := hmac.New(sha256.New, []byte(billingSecret))
	fmt.Fprintf(mac, "%d.", stamp)
	_, _ = mac.Write(body)
	return fmt.Sprintf("t=%d,v1=%x", stamp, mac.Sum(nil))
}

func TestBillingWebhookSameEventIDIsolatedByMode(t *testing.T) {
	s, _ := newStore(t)
	testBody := modeBillingPayload(t, "evt_same_mode", false)
	if _, err := s.AcceptBillingWebhook(t.Context(), testBody, modeBillingSignature(testBody), billingSecret); err != nil {
		t.Fatal(err)
	}
	s.billingMode = "live"
	liveBody := modeBillingPayload(t, "evt_same_mode", true)
	if _, err := s.AcceptBillingWebhook(t.Context(), liveBody, modeBillingSignature(liveBody), billingSecret); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRow("SELECT count(*) FROM billing_events WHERE id=?", "evt_same_mode").Scan(&count); err != nil || count != 2 {
		t.Fatalf("same event ID was not isolated: count=%d err=%v", count, err)
	}
}

func TestBillingWebhookRejectsWrongMode(t *testing.T) {
	s, _ := newStore(t)
	liveBody := modeBillingPayload(t, "evt_wrong_mode", true)
	if _, err := s.AcceptBillingWebhook(t.Context(), liveBody, modeBillingSignature(liveBody), billingSecret); err == nil {
		t.Fatal("live webhook accepted by test store")
	}
	s.billingMode = "live"
	testBody := modeBillingPayload(t, "evt_wrong_mode_test", false)
	if _, err := s.AcceptBillingWebhook(t.Context(), testBody, modeBillingSignature(testBody), billingSecret); err == nil {
		t.Fatal("test webhook accepted by live store")
	}
}

type livePanicBillingProvider struct{}

func (livePanicBillingProvider) BillingMode() string { return "live" }
func (livePanicBillingProvider) CreateCustomer(context.Context, string, string) (string, error) {
	panic("live provider must not process sandbox work")
}
func (livePanicBillingProvider) CreatePinnedCheckout(context.Context, string, string, string, string) (hostingbilling.Checkout, error) {
	panic("live provider must not process sandbox work")
}
func (livePanicBillingProvider) RetrieveSubscription(context.Context, string, string, string) (hostingbilling.SubscriptionSnapshot, error) {
	panic("live provider must not process sandbox work")
}

func TestLiveBillingWorkerIgnoresSandboxOnlyWork(t *testing.T) {
	s, _, owner, session := billingCheckoutFixture(t)
	if _, err := s.RequestBillingCheckout(t.Context(), session.Token, owner.WorkspaceID, "starter"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("INSERT INTO billing_work(mode,kind,reference,next_attempt,attempts,lease_hash,lease_until,done) VALUES('test','customer','sandbox-only',0,7,'sandbox-lease',999,0)"); err != nil {
		t.Fatal(err)
	}
	s.billingMode = "live"
	worked, err := s.BillingWorkOnce(t.Context(), livePanicBillingProvider{})
	if err != nil || worked {
		t.Fatalf("live worker discovered sandbox work: worked=%v err=%v", worked, err)
	}
	var attempts int
	var lease string
	if err = s.db.QueryRow("SELECT attempts,lease_hash FROM billing_work WHERE mode='test' AND kind='customer' AND reference='sandbox-only'").Scan(&attempts, &lease); err != nil {
		t.Fatal(err)
	}
	if attempts != 7 || lease != "sandbox-lease" {
		t.Fatalf("sandbox task was leased or changed: attempts=%d lease=%q", attempts, lease)
	}
	var liveTasks int
	if err = s.db.QueryRow("SELECT count(*) FROM billing_work WHERE mode='live'").Scan(&liveTasks); err != nil || liveTasks != 0 {
		t.Fatalf("live worker created tasks from sandbox rows: count=%d err=%v", liveTasks, err)
	}
}
