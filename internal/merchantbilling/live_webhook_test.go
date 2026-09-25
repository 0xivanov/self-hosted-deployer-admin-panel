package merchantbilling

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestMerchantWebhookModesStaySeparate(t *testing.T) {
	order := strings.Repeat("a", 64)
	for _, live := range []bool{false, true} {
		session := "cs_test_mode"
		if live {
			session = "cs_live_mode"
		}
		body := merchantWebhookBody(t, "checkout.session.completed", live, "acct_mode", session, order, "payment", map[string]string{"merchant_order": order})
		signature := merchantWebhookSignature(body, merchantWebhookSecret, time.Now().Unix())
		verify, other := VerifyTestMerchantEvent, VerifyLiveMerchantEvent
		if live {
			verify, other = other, verify
		}
		event, err := verify(body, signature, merchantWebhookSecret)
		if err != nil || event.Live != live || event.AccountID != "acct_mode" {
			t.Fatalf("matching mode: %+v %v", event, err)
		}
		if _, err = other(body, signature, merchantWebhookSecret); err == nil {
			t.Fatal("accepted opposite mode envelope")
		}
		var payload map[string]any
		if err = json.Unmarshal(body, &payload); err != nil {
			t.Fatal(err)
		}
		payload["data"].(map[string]any)["object"].(map[string]any)["livemode"] = !live
		mixed, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = verify(mixed, merchantWebhookSignature(mixed, merchantWebhookSecret, time.Now().Unix()), merchantWebhookSecret); err == nil {
			t.Fatal("accepted mixed nested mode")
		}
	}
}

func TestLiveMerchantRefundWebhookModeAndScope(t *testing.T) {
	order := strings.Repeat("b", 64)
	metadata := map[string]string{"merchant_order": order, "merchant_refund": order}
	for _, intent := range []any{"pi_live_order", map[string]any{"id": "pi_live_order", "object": "payment_intent", "livemode": true}} {
		body := merchantRefundWebhookBody(t, "refund.updated", true, intent, "re_live_order", order, order, metadata)
		event, err := VerifyLiveMerchantEvent(body, merchantWebhookSignature(body, merchantWebhookSecret, time.Now().Unix()), merchantWebhookSecret)
		if err != nil || !event.Live || event.AccountID != "acct_test_account" || event.PaymentIntentID != "pi_live_order" {
			t.Fatalf("live refund signal: %+v %v", event, err)
		}
	}
	for _, intent := range []any{map[string]any{"id": "pi_wrong", "object": "payment_intent", "livemode": false}, map[string]any{"id": "pi_missing"}} {
		body := merchantRefundWebhookBody(t, "refund.updated", true, intent, "re_live_order", order, order, metadata)
		if _, err := VerifyLiveMerchantEvent(body, merchantWebhookSignature(body, merchantWebhookSecret, time.Now().Unix()), merchantWebhookSecret); err == nil {
			t.Fatal("accepted wrong or missing expanded intent mode")
		}
	}
}
