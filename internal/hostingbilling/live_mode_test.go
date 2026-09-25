//go:build integration

package hostingbilling

import (
	"encoding/json"
	stripe "github.com/stripe/stripe-go/v86"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLiveClientRequiresExplicitMatchingSecret(t *testing.T) {
	plans := map[string]string{"starter": "price_fixture"}
	for _, live := range []bool{false, true} {
		key := "sk_live_synthetic_fixture"
		if live {
			key = "sk_test_synthetic_fixture"
		}
		if _, err := newClient(key, "https://portal.example.test/ok", "https://portal.example.test/cancel", plans, "", live); err == nil {
			t.Fatal("accepted wrong-mode secret")
		}
	}
}

func TestLiveCheckoutAcceptsOnlyLiveProviderResponse(t *testing.T) {
	for _, providerLive := range []bool{false, true} {
		t.Run(map[bool]string{false: "sandbox response", true: "live response"}[providerLive], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Idempotency-Key") != "persistent-live-request" {
					t.Error("missing persistent request")
				}
				prefix := "cs_test_"
				if providerLive {
					prefix = "cs_live_"
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{"id": prefix + "fixture", "object": "checkout.session", "livemode": providerLive, "url": "https://checkout.stripe.com/c/pay/" + prefix + "fixture"})
			}))
			defer server.Close()
			c, err := newClient("sk_live_synthetic_fixture", "https://portal.example.test/ok", "https://portal.example.test/cancel", map[string]string{"starter": "price_fixture"}, server.URL, true)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			_, err = c.CreateCheckout(t.Context(), "cus_fixture", "starter", "persistent-live-request")
			if (err == nil) != providerLive {
				t.Fatalf("mode result %v", err)
			}
		})
	}
}

func TestLiveSubscriptionRequiresMatchingNestedModes(t *testing.T) {
	for _, change := range []string{"none", "subscription", "price", "invoice"} {
		t.Run(change, func(t *testing.T) {
			fixture := subscriptionFixture()
			fixture["livemode"] = true
			price := fixture["items"].(map[string]any)["data"].([]any)[0].(map[string]any)["price"].(map[string]any)
			price["livemode"] = true
			invoice := fixture["latest_invoice"].(map[string]any)
			invoice["livemode"] = true
			switch change {
			case "subscription":
				fixture["livemode"] = false
			case "price":
				price["livemode"] = false
			case "invoice":
				invoice["livemode"] = false
			}
			raw, _ := json.Marshal(fixture)
			var subscription stripe.Subscription
			if err := json.Unmarshal(raw, &subscription); err != nil {
				t.Fatal(err)
			}
			_, err := normalizeSubscriptionForMode(&subscription, "sub_fixture", "cus_fixture", "price_fixture", 1700000000, true)
			if (err == nil) != (change == "none") {
				t.Fatalf("nested mode: %v", err)
			}
			if _, err = normalizeSubscription(&subscription, "sub_fixture", "cus_fixture", "price_fixture", 1700000000); err == nil {
				t.Fatal("sandbox accepted live subscription")
			}
		})
	}
}

func TestLiveWebhookBoundary(t *testing.T) {
	for _, live := range []bool{false, true} {
		body := eventBody(t, func(e map[string]any) { e["livemode"] = live })
		signature := sign(body, time.Now().Unix())
		event, err := VerifyLiveEvent(body, signature, signingSecret)
		if (err == nil) != live {
			t.Fatalf("live verifier: %v", err)
		}
		if live && !event.Live {
			t.Fatal("mode lost")
		}
		if _, err = VerifyTestEvent(body, signature, signingSecret); (err == nil) == live {
			t.Fatalf("test verifier: %v", err)
		}
	}
}
