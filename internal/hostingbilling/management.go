package hostingbilling

import (
	"context"
	"errors"
	"net/url"

	stripe "github.com/stripe/stripe-go/v86"
)

type Management struct {
	client        *Client
	configuration string
}

func NewManagement(client *Client, configuration string) (*Management, error) {
	if client == nil || !providerID(configuration, "bpc_") {
		return nil, errors.New("a test portal configuration is required")
	}
	return &Management{client: client, configuration: configuration}, nil
}

// CreateManagementSession produces an ephemeral link for an already-authorized
// workspace customer. Configuration and return location are operator-owned.
func (m *Management) CreateManagementSession(ctx context.Context, customer string) (string, error) {
	if !providerID(customer, "cus_") {
		return "", errors.New("invalid billing customer")
	}
	config, err := m.client.stripe.V1BillingPortalConfigurations.Retrieve(ctx, m.configuration, &stripe.BillingPortalConfigurationRetrieveParams{})
	if err != nil || !supportedManagementConfiguration(config, m.configuration) {
		return "", errors.New("billing management configuration is unsupported")
	}
	params := &stripe.BillingPortalSessionCreateParams{Customer: stripe.String(customer), Configuration: stripe.String(m.configuration), ReturnURL: stripe.String(m.client.success)}
	params.AddExpand("configuration")
	session, err := m.client.stripe.V1BillingPortalSessions.Create(ctx, params)
	if err != nil {
		return "", errors.New("billing management unavailable")
	}
	if session == nil || session.Livemode || session.Customer != customer || session.CustomerAccount != "" || session.OnBehalfOf != "" || !supportedManagementConfiguration(session.Configuration, m.configuration) || session.ReturnURL != m.client.success {
		return "", errors.New("billing management identity mismatch")
	}
	u, err := url.Parse(session.URL)
	if err != nil || u.Scheme != "https" || u.Host != "billing.stripe.com" || u.User != nil || len(session.URL) > 8192 {
		return "", errors.New("invalid billing management link")
	}
	return session.URL, nil
}

// Restrict the MVP portal to invoices, payment methods and cancellation at the
// end of the paid period. Public login and subscription edits bypass assumptions
// of the current workspace billing flow and must remain disabled.
func supportedManagementConfiguration(c *stripe.BillingPortalConfiguration, id string) bool {
	if c == nil || c.ID != id || !c.Active || c.Livemode || c.Application != nil || c.Features == nil || c.LoginPage == nil || c.LoginPage.Enabled {
		return false
	}
	f := c.Features
	return f.SubscriptionUpdate != nil && !f.SubscriptionUpdate.Enabled && f.SubscriptionCancel != nil && f.SubscriptionCancel.Enabled && f.SubscriptionCancel.Mode == "at_period_end" && f.SubscriptionCancel.ProrationBehavior == "none" && f.PaymentMethodUpdate != nil && f.PaymentMethodUpdate.Enabled && f.InvoiceHistory != nil && f.InvoiceHistory.Enabled
}
