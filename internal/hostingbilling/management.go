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
	params := &stripe.BillingPortalSessionCreateParams{Customer: stripe.String(customer), Configuration: stripe.String(m.configuration), ReturnURL: stripe.String(m.client.success)}
	session, err := m.client.stripe.V1BillingPortalSessions.Create(ctx, params)
	if err != nil {
		return "", errors.New("billing management unavailable")
	}
	if session == nil || session.Livemode || session.Customer != customer || session.CustomerAccount != "" || session.OnBehalfOf != "" || session.Configuration == nil || session.Configuration.ID != m.configuration || session.ReturnURL != m.client.success {
		return "", errors.New("billing management identity mismatch")
	}
	u, err := url.Parse(session.URL)
	if err != nil || u.Scheme != "https" || u.Host != "billing.stripe.com" || u.User != nil || len(session.URL) > 8192 {
		return "", errors.New("invalid billing management link")
	}
	return session.URL, nil
}
