// Package hostingbilling integrates platform hosting subscriptions. Merchant
// website sales belong to a separate Connect integration and accounting scope.
package hostingbilling

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/stripe/stripe-go/v86/webhook"
)

const MaxWebhookBytes = 1 << 20

var ErrWebhook = errors.New("invalid hosting billing webhook")

type Event struct {
	ID      string
	Type    string
	Created int64
	SHA256  string
	Object  json.RawMessage
}

// VerifyTestEvent uses Stripe's raw-body signature and API-version validation.
// Durable deduplication and customer/subscription ownership checks must follow
// before any hosting entitlement is changed. Signature validation alone grants
// no workspace authority, and a Checkout redirect never proves payment.
func VerifyTestEvent(body []byte, signature, secret string) (Event, error) {
	if len(body) == 0 || len(body) > MaxWebhookBytes || len(signature) > 8192 || !strings.HasPrefix(secret, "whsec_") || len(secret) < 16 {
		return Event{}, ErrWebhook
	}
	// Stripe rejects old timestamps. Also reject far-future timestamps and
	// ambiguous repeated timestamp fields, rather than extending replay windows.
	var timestamp int64
	count := 0
	for _, part := range strings.Split(signature, ",") {
		pair := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(pair) == 2 && pair[0] == "t" {
			count++
			value, err := strconv.ParseInt(pair[1], 10, 64)
			if err != nil {
				return Event{}, ErrWebhook
			}
			timestamp = value
		}
	}
	if count != 1 || timestamp > time.Now().Add(5*time.Minute).Unix() {
		return Event{}, ErrWebhook
	}
	event, err := webhook.ConstructEvent(body, signature, secret)
	if err != nil {
		return Event{}, ErrWebhook
	}
	// Require an explicit test-mode envelope. A missing boolean must not be
	// mistaken for false, and connected-account events must never enter hosting.
	var envelope struct {
		Livemode *bool  `json:"livemode"`
		Account  string `json:"account"`
		Context  string `json:"context"`
	}
	if json.Unmarshal(body, &envelope) != nil || envelope.Livemode == nil || *envelope.Livemode || envelope.Account != "" || envelope.Context != "" || event.ID == "" || event.Type == "" || event.Created <= 0 || event.Data == nil || len(event.Data.Raw) == 0 {
		return Event{}, ErrWebhook
	}
	raw := strings.TrimSpace(string(event.Data.Raw))
	if !strings.HasPrefix(raw, "{") {
		return Event{}, ErrWebhook
	}
	sum := sha256.Sum256(body)
	return Event{ID: event.ID, Type: string(event.Type), Created: event.Created, SHA256: hex.EncodeToString(sum[:]), Object: append(json.RawMessage(nil), event.Data.Raw...)}, nil
}
