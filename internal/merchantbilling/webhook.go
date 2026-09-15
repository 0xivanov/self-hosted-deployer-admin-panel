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
	ID        string
	AccountID string
	OrderID   string
	SessionID string
	Type      string
	SHA256    string
	Created   int64
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

func VerifyTestCheckoutEvent(body []byte, signature, secret string) (CheckoutEvent, error) {
	if len(body) == 0 || len(body) > MaxWebhookBytes || len(signature) > 8192 || !strings.HasPrefix(secret, "whsec_") || len(secret) < 16 {
		return CheckoutEvent{}, ErrWebhook
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
				return CheckoutEvent{}, ErrWebhook
			}
		}
	}
	if count != 1 || timestamp > time.Now().Add(5*time.Minute).Unix() {
		return CheckoutEvent{}, ErrWebhook
	}
	event, err := webhook.ConstructEvent(body, signature, secret)
	if err != nil || event.APIVersion != stripe.APIVersion {
		return CheckoutEvent{}, ErrWebhook
	}
	var envelope struct {
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
	if json.Unmarshal(body, &envelope) != nil || envelope.Object != "event" || envelope.Livemode == nil || *envelope.Livemode || !validAccountID(envelope.Account) || envelope.Context != "" || !validMerchantEventID(envelope.ID) || envelope.Type == "" || envelope.Created <= 0 {
		return CheckoutEvent{}, ErrWebhook
	}
	supported := envelope.Type == "checkout.session.completed" || envelope.Type == "checkout.session.expired" || envelope.Type == "checkout.session.async_payment_succeeded" || envelope.Type == "checkout.session.async_payment_failed"
	if !supported {
		return CheckoutEvent{}, ErrUnsupportedEvent
	}
	if len(envelope.Data.Object) == 0 {
		return CheckoutEvent{}, ErrWebhook
	}
	var session struct {
		ID                string            `json:"id"`
		Object            string            `json:"object"`
		Livemode          *bool             `json:"livemode"`
		Mode              string            `json:"mode"`
		ClientReferenceID string            `json:"client_reference_id"`
		Metadata          map[string]string `json:"metadata"`
	}
	if json.Unmarshal(envelope.Data.Object, &session) != nil || session.Livemode == nil || *session.Livemode || session.Object != "checkout.session" || !validSessionID(session.ID) || session.Mode != "payment" || !validRequestID(session.Metadata["merchant_order"]) || session.ClientReferenceID != session.Metadata["merchant_order"] {
		return CheckoutEvent{}, ErrWebhook
	}
	sum := sha256.Sum256(body)
	return CheckoutEvent{ID: envelope.ID, AccountID: envelope.Account, OrderID: session.Metadata["merchant_order"], SessionID: session.ID, Type: envelope.Type, SHA256: hex.EncodeToString(sum[:]), Created: envelope.Created}, nil
}
