//go:build integration

package portal

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	stripe "github.com/stripe/stripe-go/v86"
)

const billingSecret = "whsec_synthetic_billing_fixture"

func billingPayload(t *testing.T, id, customer string, counter int) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{"id": id, "object": "event", "type": "invoice.paid", "api_version": stripe.APIVersion, "created": 1700000000, "livemode": false, "pending_webhooks": counter, "data": map[string]any{"object": map[string]any{"id": "in_fixture", "customer": customer, "amount_paid": json.Number("9007199254740993")}}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func billingSignature(body []byte) string {
	stamp := time.Now().Unix()
	mac := hmac.New(sha256.New, []byte(billingSecret))
	fmt.Fprintf(mac, "%d.", stamp)
	mac.Write(body)
	return fmt.Sprintf("t=%d,v1=%x", stamp, mac.Sum(nil))
}
func TestBillingInboxPersistenceAndConcurrentDuplicates(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, path := newStore(t)
	body := billingPayload(t, "evt_fixture", "cus_fixture", 2)
	var wg sync.WaitGroup
	results := make(chan BillingReceipt, 2)
	for range 2 {
		wg.Go(func() {
			r, err := s.AcceptBillingWebhook(ctx, body, billingSignature(body), billingSecret)
			if err != nil {
				t.Error(err)
			}
			results <- r
		})
	}
	wg.Wait()
	close(results)
	fresh := 0
	for r := range results {
		if r.ID != "evt_fixture" {
			t.Fatal(r)
		}
		if !r.Duplicate {
			fresh++
		}
	}
	if fresh != 1 {
		t.Fatal("duplicate event inserted", fresh)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	counterChange := billingPayload(t, "evt_fixture", "cus_fixture", 1)
	r, err := s.AcceptBillingWebhook(ctx, counterChange, billingSignature(counterChange), billingSecret)
	if err != nil || !r.Duplicate {
		t.Fatal("delivery metadata broke deduplication", r, err)
	}
	changed := billingPayload(t, "evt_fixture", "cus_other", 1)
	if _, err = s.AcceptBillingWebhook(ctx, changed, billingSignature(changed), billingSecret); !errors.Is(err, ErrBillingConflict) {
		t.Fatal("conflicting event replaced original", err)
	}
	var payload []byte
	var state string
	if err = s.db.QueryRow("SELECT payload,state FROM billing_events WHERE id='evt_fixture'").Scan(&payload, &state); err != nil || state != "pending" || !bytes.Contains(payload, []byte("9007199254740993")) || !bytes.Contains(payload, []byte("cus_fixture")) {
		t.Fatal("payload changed", string(payload), state, err)
	}
	if _, err = s.AcceptBillingWebhook(ctx, append(body, ' '), billingSignature(body), billingSecret); err == nil {
		t.Fatal("tampered event persisted")
	}
}
func TestBillingWebhookAcknowledgesOnlyDurableEvents(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)
	h, err := BillingWebhookHandler(s, "billing.example.test", billingSecret)
	if err != nil {
		t.Fatal(err)
	}
	body := billingPayload(t, "evt_http", "cus_fixture", 1)
	for _, tc := range []struct {
		name, url, signature, origin string
		code                         int
	}{
		{"signed", "https://billing.example.test/hooks", billingSignature(body), "", 204},
		{"duplicate", "https://billing.example.test/hooks", billingSignature(body), "", 204},
		{"unsigned", "https://billing.example.test/hooks", "", "", 400},
		{"plaintext", "http://billing.example.test/hooks", billingSignature(body), "", 403},
		{"foreign host", "https://other.example.test/hooks", billingSignature(body), "", 403},
		{"browser", "https://billing.example.test/hooks", billingSignature(body), "https://portal.example.test", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", tc.url, bytes.NewReader(body))
			req.Header.Set("Stripe-Signature", tc.signature)
			req.Header.Set("Origin", tc.origin)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != tc.code {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "https://billing.example.test/hooks", bytes.NewReader(body))
	req.Header.Set("Stripe-Signature", billingSignature(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 503 {
		t.Fatal("acknowledged unavailable database", w.Code)
	}
}
func TestBillingInboxCapacity(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)
	_, err := s.db.Exec(`WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<10000) INSERT INTO billing_events SELECT 'fixture-'||x,'invoice.paid',1,'fixture',x'7b7d','pending',1 FROM n`)
	if err != nil {
		t.Fatal(err)
	}
	body := billingPayload(t, "evt_capacity", "cus_fixture", 1)
	if _, err = s.AcceptBillingWebhook(t.Context(), body, billingSignature(body), billingSecret); !errors.Is(err, ErrBillingCapacity) {
		t.Fatal("inbox capacity exceeded", err)
	}
}
