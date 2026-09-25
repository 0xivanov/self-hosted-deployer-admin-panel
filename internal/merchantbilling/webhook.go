package merchantbilling

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	stripe "github.com/stripe/stripe-go/v86"
	"github.com/stripe/stripe-go/v86/webhook"
)

const MaxWebhookBytes = 1 << 20

var ErrWebhook = errors.New("invalid merchant webhook")
var ErrUnsupportedEvent = errors.New("unsupported merchant webhook event")

type CheckoutEvent struct {
	Live            bool
	ID              string
	AccountID       string
	OrderID         string
	SessionID       string
	RefundRequestID string
	RefundID        string
	PaymentIntentID string
	Type            string
	SHA256          string
	Created         int64
}

type merchantWebhookEnvelope struct {
	ID       string `json:"id"`
	Object   string `json:"object"`
	Livemode *bool  `json:"livemode"`
	Account  string `json:"account"`
	Context  string `json:"context"`
	Type     string `json:"type"`
	Created  int64  `json:"created"`
	Data     struct {
		Object json.RawMessage `json:"object"`
	} `json:"data"`
}

func validMerchantEventID(id string) bool {
	if !strings.HasPrefix(id, "evt_") || len(id) <= len("evt_") || len(id) > 255 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_') {
			return false
		}
	}
	return true
}

func verifyMerchantWebhookEnvelope(body []byte, signature, secret string, live bool) (merchantWebhookEnvelope, string, error) {
	if len(body) == 0 || len(body) > MaxWebhookBytes || len(signature) > 8192 || !strings.HasPrefix(secret, "whsec_") || len(secret) < 16 {
		return merchantWebhookEnvelope{}, "", ErrWebhook
	}
	var timestamp int64
	count := 0
	for _, part := range strings.Split(signature, ",") {
		pair := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(pair) == 2 && pair[0] == "t" {
			count++
			var err error
			timestamp, err = strconv.ParseInt(pair[1], 10, 64)
			if err != nil {
				return merchantWebhookEnvelope{}, "", ErrWebhook
			}
		}
	}
	if count != 1 || timestamp > time.Now().Add(5*time.Minute).Unix() {
		return merchantWebhookEnvelope{}, "", ErrWebhook
	}
	event, err := webhook.ConstructEvent(body, signature, secret)
	if err != nil || event.APIVersion != stripe.APIVersion {
		return merchantWebhookEnvelope{}, "", ErrWebhook
	}
	var envelope merchantWebhookEnvelope
	if json.Unmarshal(body, &envelope) != nil || envelope.Object != "event" || envelope.Livemode == nil || *envelope.Livemode != live || !validAccountID(envelope.Account) || envelope.Context != "" || !validMerchantEventID(envelope.ID) || envelope.Type == "" || envelope.Created <= 0 || len(envelope.Data.Object) == 0 {
		return merchantWebhookEnvelope{}, "", ErrWebhook
	}
	sum := sha256.Sum256(body)
	return envelope, hex.EncodeToString(sum[:]), nil
}

func VerifyTestMerchantEvent(body []byte, signature, secret string) (CheckoutEvent, error) {
	return verifyMerchantEvent(body, signature, secret, false)
}

// VerifyLiveMerchantEvent verifies a connected-account event in live mode.
// Consumers must bind the returned account, order and mode before any mutation.
func VerifyLiveMerchantEvent(body []byte, signature, secret string) (CheckoutEvent, error) {
	return verifyMerchantEvent(body, signature, secret, true)
}

func verifyMerchantEvent(body []byte, signature, secret string, live bool) (CheckoutEvent, error) {
	envelope, digest, err := verifyMerchantWebhookEnvelope(body, signature, secret, live)
	if err != nil {
		return CheckoutEvent{}, err
	}
	supportedCheckout := envelope.Type == "checkout.session.completed" || envelope.Type == "checkout.session.expired" || envelope.Type == "checkout.session.async_payment_succeeded" || envelope.Type == "checkout.session.async_payment_failed"
	supportedRefund := envelope.Type == "refund.created" || envelope.Type == "refund.updated" || envelope.Type == "refund.failed"
	if !supportedCheckout && !supportedRefund {
		return CheckoutEvent{}, ErrUnsupportedEvent
	}
	if supportedRefund {
		var refund struct {
			ID            string            `json:"id"`
			Object        string            `json:"object"`
			PaymentIntent json.RawMessage   `json:"payment_intent"`
			Metadata      map[string]string `json:"metadata"`
		}
		if json.Unmarshal(envelope.Data.Object, &refund) != nil || refund.Object != "refund" || !validRefundID(refund.ID) || !validRequestID(refund.Metadata["merchant_order"]) || !validRequestID(refund.Metadata["merchant_refund"]) {
			return CheckoutEvent{}, ErrWebhook
		}
		var paymentIntentID string
		if len(refund.PaymentIntent) > 0 && string(refund.PaymentIntent) != "null" {
			if refund.PaymentIntent[0] == '"' {
				if json.Unmarshal(refund.PaymentIntent, &paymentIntentID) != nil {
					return CheckoutEvent{}, ErrWebhook
				}
			} else {
				var expanded struct {
					ID     string `json:"id"`
					Object string `json:"object"`
					Live   *bool  `json:"livemode"`
				}
				if json.Unmarshal(refund.PaymentIntent, &expanded) != nil || expanded.Object != "payment_intent" || expanded.Live == nil || *expanded.Live != live {
					return CheckoutEvent{}, ErrWebhook
				}
				paymentIntentID = expanded.ID
			}
		}
		if !validPaymentIntentID(paymentIntentID) {
			return CheckoutEvent{}, ErrWebhook
		}
		return CheckoutEvent{Live: live, ID: envelope.ID, AccountID: envelope.Account, OrderID: refund.Metadata["merchant_order"], RefundRequestID: refund.Metadata["merchant_refund"], RefundID: refund.ID, PaymentIntentID: paymentIntentID, Type: envelope.Type, SHA256: digest, Created: envelope.Created}, nil
	}
	var session struct {
		ID                string            `json:"id"`
		Object            string            `json:"object"`
		Livemode          *bool             `json:"livemode"`
		Mode              string            `json:"mode"`
		ClientReferenceID string            `json:"client_reference_id"`
		Metadata          map[string]string `json:"metadata"`
	}
	if json.Unmarshal(envelope.Data.Object, &session) != nil || session.Livemode == nil || *session.Livemode != live || session.Object != "checkout.session" || !validSessionIDForMode(session.ID, live) || session.Mode != "payment" || !validRequestID(session.Metadata["merchant_order"]) || session.ClientReferenceID != session.Metadata["merchant_order"] {
		return CheckoutEvent{}, ErrWebhook
	}
	return CheckoutEvent{Live: live, ID: envelope.ID, AccountID: envelope.Account, OrderID: session.Metadata["merchant_order"], SessionID: session.ID, Type: envelope.Type, SHA256: digest, Created: envelope.Created}, nil
}

func VerifyTestCheckoutEvent(body []byte, signature, secret string) (CheckoutEvent, error) {
	event, err := VerifyTestMerchantEvent(body, signature, secret)
	if err == nil && event.RefundID != "" {
		return CheckoutEvent{}, ErrUnsupportedEvent
	}
	return event, err
}
