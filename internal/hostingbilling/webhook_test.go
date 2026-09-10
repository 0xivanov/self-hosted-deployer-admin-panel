package hostingbilling

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	stripe "github.com/stripe/stripe-go/v86"
)

const signingSecret = "whsec_synthetic_testing_only"

func sign(body []byte, stamp int64) string {
	mac := hmac.New(sha256.New, []byte(signingSecret))
	fmt.Fprintf(mac, "%d.", stamp)
	mac.Write(body)
	return fmt.Sprintf("t=%d,v1=%x", stamp, mac.Sum(nil))
}
func eventBody(t *testing.T, change func(map[string]any)) []byte {
	t.Helper()
	event := map[string]any{"id": "evt_fixture", "object": "event", "type": "invoice.paid", "created": time.Now().Unix(), "api_version": stripe.APIVersion, "livemode": false, "data": map[string]any{"object": map[string]any{"id": "in_fixture", "object": "invoice", "customer": "cus_fixture"}}}
	if change != nil {
		change(event)
	}
	body, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	return body
}
func TestTestWebhookBoundary(t *testing.T) {
	t.Parallel()
	now := time.Now().Unix()
	body := eventBody(t, nil)
	event, err := VerifyTestEvent(body, sign(body, now), signingSecret)
	if err != nil || event.ID != "evt_fixture" || event.SHA256 == "" {
		t.Fatal(event, err)
	}
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
		offset int64
	}{
		{"live event", func(e map[string]any) { e["livemode"] = true }, 0}, {"missing mode", func(e map[string]any) { delete(e, "livemode") }, 0}, {"merchant event", func(e map[string]any) { e["account"] = "acct_fixture" }, 0}, {"v2 account context", func(e map[string]any) { e["context"] = "acct_fixture" }, 0}, {"wrong API version", func(e map[string]any) { e["api_version"] = "1900-01-01" }, 0}, {"missing data", func(e map[string]any) { delete(e, "data") }, 0}, {"old signature", nil, -600}, {"future signature", nil, 600},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := eventBody(t, tc.change)
			if _, err := VerifyTestEvent(b, sign(b, now+tc.offset), signingSecret); err == nil {
				t.Fatal("untrusted event accepted")
			}
		})
	}
	if _, err = VerifyTestEvent(append(body, ' '), sign(body, now), signingSecret); err == nil {
		t.Fatal("body alteration accepted")
	}
	if _, err = VerifyTestEvent(body, sign(body, now)+fmt.Sprintf(",t=%d", now), signingSecret); err == nil {
		t.Fatal("ambiguous timestamp accepted")
	}
	if _, err = VerifyTestEvent(body, sign(body, now)+",v1="+strings.Repeat("0", 64), signingSecret); err != nil {
		t.Fatal("rotating signature rejected", err)
	}
	if _, err = VerifyTestEvent(make([]byte, MaxWebhookBytes+1), sign(body, now), signingSecret); err == nil {
		t.Fatal("oversized event accepted")
	}
}
