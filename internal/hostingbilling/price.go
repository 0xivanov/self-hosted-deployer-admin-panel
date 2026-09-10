package hostingbilling

import (
	"context"
	"errors"
	"time"

	stripe "github.com/stripe/stripe-go/v86"
)

// PlanPrice is a provider-verified base price, not a final invoice or tax quote.
// AmountMinor uses the currency's minor unit and must not be formatted by
// assuming that every currency has two decimal places.
type PlanPrice struct {
	Plan          string `json:"plan"`
	PriceID       string `json:"-"`
	AmountMinor   int64  `json:"amount_minor"`
	Currency      string `json:"currency"`
	Interval      string `json:"interval"`
	IntervalCount int64  `json:"interval_count"`
	TaxBehavior   string `json:"tax_behavior"`
	ObservedAt    int64  `json:"observed_at"`
}

// RetrievePlanPrice only reads the operator-configured price. The first hosting
// product supports fixed, licensed recurring prices, without tiers, quantity
// transforms, custom amounts or alternate currencies.
func (c *Client) RetrievePlanPrice(ctx context.Context, plan, expectedPrice string) (PlanPrice, error) {
	price, ok := c.plans[plan]
	if !ok || price != expectedPrice || !providerID(price, "price_") {
		return PlanPrice{}, errors.New("hosting price configuration mismatch")
	}
	params := &stripe.PriceRetrieveParams{}
	params.AddExpand("currency_options")
	observed := time.Now().Unix()
	p, err := c.stripe.V1Prices.Retrieve(ctx, price, params)
	if err != nil {
		return PlanPrice{}, errors.New("hosting price unavailable")
	}
	return normalizePlanPrice(p, plan, price, observed)
}
func normalizePlanPrice(p *stripe.Price, plan, price string, observed int64) (PlanPrice, error) {
	invalid := errors.New("unsupported hosting price")
	if p == nil || p.ID != price || p.Object != "price" || p.Deleted || p.Livemode || !p.Active || p.Type != "recurring" || p.BillingScheme != "per_unit" || p.CustomUnitAmount != nil || p.TransformQuantity != nil || len(p.Tiers) != 0 || p.TiersMode != "" || p.Recurring == nil || p.Recurring.UsageType != "licensed" || p.Recurring.Meter != "" || p.UnitAmount < 0 || p.UnitAmount > 99999999 || p.UnitAmountDecimal != float64(p.UnitAmount) {
		return PlanPrice{}, invalid
	}
	for currency, option := range p.CurrencyOptions {
		if currency != string(p.Currency) || option == nil || option.CustomUnitAmount != nil || len(option.Tiers) != 0 || option.UnitAmount != p.UnitAmount || option.UnitAmountDecimal != p.UnitAmountDecimal || string(option.TaxBehavior) != string(p.TaxBehavior) {
			return PlanPrice{}, invalid
		}
	}
	currency := string(p.Currency)
	if len(currency) != 3 {
		return PlanPrice{}, invalid
	}
	for _, r := range currency {
		if r < 'a' || r > 'z' {
			return PlanPrice{}, invalid
		}
	}
	switch p.Recurring.Interval {
	case "day", "week", "month", "year":
	default:
		return PlanPrice{}, invalid
	}
	if p.Recurring.IntervalCount < 1 {
		return PlanPrice{}, invalid
	}
	switch p.TaxBehavior {
	case "inclusive", "exclusive", "unspecified":
	default:
		return PlanPrice{}, invalid
	}
	return PlanPrice{Plan: plan, PriceID: price, AmountMinor: p.UnitAmount, Currency: currency, Interval: string(p.Recurring.Interval), IntervalCount: p.Recurring.IntervalCount, TaxBehavior: string(p.TaxBehavior), ObservedAt: observed}, nil
}
