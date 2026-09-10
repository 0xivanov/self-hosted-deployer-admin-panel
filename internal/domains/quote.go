package domains

import (
	"errors"
	"math"
	"time"
)

var ErrQuote = errors.New("a fresh standard-price registration and renewal quote is required")

// RegistrarQuote is normalized provider evidence, never browser-supplied prices.
// Integer minor units avoid rounding money through floating-point conversions.
type RegistrarQuote struct {
	Domain                          string
	Available                       bool
	Premium                         bool
	PremiumChecked                  bool
	Currency                        string
	RegistrationMinor, RenewalMinor int64
	CheckedAt                       time.Time
}
type Offer struct {
	Domain            string    `json:"domain"`
	Currency          string    `json:"currency"`
	RegistrationMinor int64     `json:"registration_minor"`
	RenewalMinor      int64     `json:"renewal_minor"`
	Years             int       `json:"years"`
	ExpiresAt         time.Time `json:"expires_at"`
}

// OfferFor creates a one-year offer with an explicit fixed markup on registration
// and renewal. It cannot reserve the domain. Recheck availability and provider
// pricing before billing/registering; renewal prices are estimates for later use.
func OfferFor(q RegistrarQuote, requested string, markupMinor int64, now time.Time) (Offer, error) {
	name, err := PurchaseName(requested)
	if err != nil {
		return Offer{}, err
	}
	if q.Domain != name || !q.Available || q.Premium || !q.PremiumChecked || q.RegistrationMinor <= 0 || q.RenewalMinor <= 0 || markupMinor < 0 || q.CheckedAt.After(now) || !q.CheckedAt.Add(5*time.Minute).After(now) {
		return Offer{}, ErrQuote
	}
	// Launch currencies are intentionally limited to currencies with two decimals.
	if q.Currency != "usd" && q.Currency != "eur" {
		return Offer{}, ErrQuote
	}
	if q.RegistrationMinor > math.MaxInt64-markupMinor || q.RenewalMinor > math.MaxInt64-markupMinor {
		return Offer{}, ErrQuote
	}
	return Offer{Domain: name, Currency: q.Currency, RegistrationMinor: q.RegistrationMinor + markupMinor, RenewalMinor: q.RenewalMinor + markupMinor, Years: 1, ExpiresAt: q.CheckedAt.Add(5 * time.Minute)}, nil
}
