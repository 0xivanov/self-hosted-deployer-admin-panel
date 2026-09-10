package domains

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestPurchaseName(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, input, want string }{
		{"canonical", "example.com", "example.com"}, {"case and padding", " Example.NET ", "example.net"}, {"hyphen", "my-site.org", "my-site.org"}, {"maximum label", strings.Repeat("a", 63) + ".com", strings.Repeat("a", 63) + ".com"},
		{"URL", "https://example.com", ""}, {"subdomain", "www.example.com", ""}, {"path", "example.com/x", ""}, {"port", "example.com:443", ""}, {"IDN", "xn--bcher-kva.com", ""}, {"Unicode", "bücher.com", ""}, {"wildcard", "*.com", ""}, {"trailing dot", "example.com.", ""}, {"reserved hyphens", "ab--cd.com", ""}, {"unsupported suffix", "example.co.uk", ""}, {"long label", strings.Repeat("a", 64) + ".com", ""}, {"separator injection", "example.com&x=1", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := PurchaseName(tc.input)
			if tc.want == "" {
				if err == nil {
					t.Fatal("unsafe domain accepted", got)
				}
			} else if err != nil || got != tc.want {
				t.Fatal(got, err)
			}
		})
	}
}
func TestOfferRequiresFreshExplicitPrices(t *testing.T) {
	t.Parallel()
	now := time.Unix(1700000000, 0)
	for _, tc := range []struct {
		name   string
		change func(*RegistrarQuote)
		markup int64
		valid  bool
	}{
		{"standard", nil, 200, true}, {"premium", func(q *RegistrarQuote) { q.Premium = true }, 200, false}, {"unknown premium", func(q *RegistrarQuote) { q.PremiumChecked = false }, 200, false}, {"taken", func(q *RegistrarQuote) { q.Available = false }, 200, false}, {"foreign name", func(q *RegistrarQuote) { q.Domain = "other.com" }, 200, false}, {"expired", func(q *RegistrarQuote) { q.CheckedAt = now.Add(-5 * time.Minute) }, 200, false}, {"future", func(q *RegistrarQuote) { q.CheckedAt = now.Add(time.Second) }, 200, false}, {"missing renewal", func(q *RegistrarQuote) { q.RenewalMinor = 0 }, 200, false}, {"negative markup", nil, -1, false}, {"overflow", func(q *RegistrarQuote) { q.RegistrationMinor = math.MaxInt64 }, 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := RegistrarQuote{Domain: "example.com", Available: true, PremiumChecked: true, Currency: "usd", RegistrationMinor: 1100, RenewalMinor: 1300, CheckedAt: now}
			if tc.change != nil {
				tc.change(&q)
			}
			offer, err := OfferFor(q, "Example.COM", tc.markup, now)
			if !tc.valid {
				if err == nil {
					t.Fatal("unsafe offer", offer)
				}
			} else if err != nil || offer.RegistrationMinor != 1300 || offer.RenewalMinor != 1500 || offer.Years != 1 || !offer.ExpiresAt.Equal(now.Add(5*time.Minute)) {
				t.Fatal(offer, err)
			}
		})
	}
}
