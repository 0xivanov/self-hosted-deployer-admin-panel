package merchantbilling

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	stripe "github.com/stripe/stripe-go/v86"
)

const merchantWebhookSecret = "whsec_test_secret_1234"

func merchantWebhookBody(t *testing.T, typ string, live bool, account, session, order string, mode any, metadata map[string]string) []byte {
	t.Helper()
	body := map[string]any{
		"id":          "evt_test_webhook_123",
		"object":      "event",
		"api_version": stripe.APIVersion,
		"livemode":    live,
		"account":     account,
		"context":     "",
		"type":        typ,
		"created":     time.Now().Unix(),
		"data": map[string]any{"object": map[string]any{
			"id":                  session,
			"object":              "checkout.session",
			"livemode":            live,
			"mode":                mode,
			"client_reference_id": order,
			"metadata":            metadata,
		}},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func merchantWebhookSignature(body []byte, secret string, timestamp int64) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(strconv.FormatInt(timestamp, 10) + "."))
	_, _ = mac.Write(body)
	return fmt.Sprintf("t=%d,v1=%s", timestamp, hex.EncodeToString(mac.Sum(nil)))
}

func TestMerchantCheckoutWebhookValid(t *testing.T) {
	order := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	body := merchantWebhookBody(t, "checkout.session.completed", false, "acct_test_account", "cs_test_session_123", order, "payment", map[string]string{
		"merchant_order": order,
		"unrelated":      "allowed",
	})
	timestamp := time.Now().Unix()
	event, err := VerifyTestCheckoutEvent(body, merchantWebhookSignature(body, merchantWebhookSecret, timestamp), merchantWebhookSecret)
	if err != nil {
		t.Fatalf("VerifyTestCheckoutEvent() error = %v", err)
	}
	if event.ID != "evt_test_webhook_123" || event.AccountID != "acct_test_account" || event.OrderID != order || event.SessionID != "cs_test_session_123" || event.Type != "checkout.session.completed" || event.Created == 0 {
		t.Fatalf("unexpected event: %+v", event)
	}
	wantHash := sha256.Sum256(body)
	if event.SHA256 != hex.EncodeToString(wantHash[:]) {
		t.Fatalf("SHA256 = %q, want raw body hash", event.SHA256)
	}
}

func TestMerchantCheckoutWebhookRejectsBadSignatures(t *testing.T) {
	order := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	body := merchantWebhookBody(t, "checkout.session.completed", false, "acct_test_account", "cs_test_session_123", order, "payment", map[string]string{"merchant_order": order})
	now := time.Now().Unix()
	tests := []struct {
		name string
		sig  string
	}{
		{"tampered", merchantWebhookSignature(append(body, ' '), merchantWebhookSecret, now)},
		{"expired", merchantWebhookSignature(body, merchantWebhookSecret, now-10*60)},
		{"future", merchantWebhookSignature(body, merchantWebhookSecret, now+6*60)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := VerifyTestCheckoutEvent(body, tc.sig, merchantWebhookSecret); !errors.Is(err, ErrWebhook) {
				t.Fatalf("error = %v, want ErrWebhook", err)
			}
		})
	}
}

func TestMerchantCheckoutWebhookRejectsEnvelopeAndSessionScope(t *testing.T) {
	order := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	tests := []struct {
		name string
		body []byte
	}{
		{"live envelope", merchantWebhookBody(t, "checkout.session.completed", true, "acct_test_account", "cs_test_session_123", order, "payment", map[string]string{"merchant_order": order})},
		{"missing account", merchantWebhookBody(t, "checkout.session.completed", false, "", "cs_test_session_123", order, "payment", map[string]string{"merchant_order": order})},
		{"foreign account grammar", merchantWebhookBody(t, "checkout.session.completed", false, "cus_not_account", "cs_test_session_123", order, "payment", map[string]string{"merchant_order": order})},
		{"live session", merchantWebhookBody(t, "checkout.session.completed", true, "acct_test_account", "cs_test_session_123", order, "payment", map[string]string{"merchant_order": order})},
		{"missing mode", merchantWebhookBody(t, "checkout.session.completed", false, "acct_test_account", "cs_test_session_123", order, nil, map[string]string{"merchant_order": order})},
		{"wrong mode", merchantWebhookBody(t, "checkout.session.completed", false, "acct_test_account", "cs_test_session_123", order, "subscription", map[string]string{"merchant_order": order})},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := VerifyTestCheckoutEvent(tc.body, merchantWebhookSignature(tc.body, merchantWebhookSecret, time.Now().Unix()), merchantWebhookSecret); !errors.Is(err, ErrWebhook) {
				t.Fatalf("error = %v, want ErrWebhook", err)
			}
		})
	}
}

func TestMerchantCheckoutWebhookRejectsMismatchedOrderReference(t *testing.T) {
	order := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	other := "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	body := merchantWebhookBody(t, "checkout.session.completed", false, "acct_test_account", "cs_test_session_123", other, "payment", map[string]string{"merchant_order": order})
	if _, err := VerifyTestCheckoutEvent(body, merchantWebhookSignature(body, merchantWebhookSecret, time.Now().Unix()), merchantWebhookSecret); !errors.Is(err, ErrWebhook) {
		t.Fatalf("error = %v, want ErrWebhook", err)
	}
}

func TestMerchantCheckoutWebhookUnsupportedEvent(t *testing.T) {
	order := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	body := merchantWebhookBody(t, "account.updated", false, "acct_test_account", "cs_test_session_123", order, "payment", map[string]string{"merchant_order": order})
	if _, err := VerifyTestCheckoutEvent(body, merchantWebhookSignature(body, merchantWebhookSecret, time.Now().Unix()), merchantWebhookSecret); !errors.Is(err, ErrUnsupportedEvent) {
		t.Fatalf("error = %v, want ErrUnsupportedEvent", err)
	}
}

func TestMerchantCheckoutWebhookExplicitTestMode(t *testing.T) {
	order := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	for _, target := range []string{"envelope", "session", "live session only"} {
		body := merchantWebhookBody(t, "checkout.session.completed", false, "acct_test_account", "cs_test_session_123", order, "payment", map[string]string{"merchant_order": order})
		var envelope map[string]any
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Fatal(err)
		}
		session := envelope["data"].(map[string]any)["object"].(map[string]any)
		switch target {
		case "envelope":
			delete(envelope, "livemode")
		case "session":
			delete(session, "livemode")
		default:
			session["livemode"] = true
		}
		body, err := json.Marshal(envelope)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = VerifyTestCheckoutEvent(body, merchantWebhookSignature(body, merchantWebhookSecret, time.Now().Unix()), merchantWebhookSecret); !errors.Is(err, ErrWebhook) {
			t.Fatal(target, err)
		}
	}
}
