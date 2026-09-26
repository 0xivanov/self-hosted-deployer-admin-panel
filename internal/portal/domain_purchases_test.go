//go:build integration

package portal

import (
	"context"
	"errors"
	"testing"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/domainbilling"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/domains"
)

type domainPayFixture struct {
	paid           bool
	creates, reads int
}

func (p *domainPayFixture) CreateCheckout(context.Context, domainbilling.Order) (domainbilling.Checkout, error) {
	p.creates++
	return domainbilling.Checkout{ID: "cs_test_domain", URL: "https://checkout.stripe.com/c/pay/cs_test_domain"}, nil
}
func (p *domainPayFixture) ReadCheckout(context.Context, string, domainbilling.Order) (domainbilling.Checkout, error) {
	p.reads++
	return domainbilling.Checkout{ID: "cs_test_domain", Paid: p.paid}, nil
}

type domainRegistrarFixture struct {
	registrations, connections int
	err                        error
}

func (r *domainRegistrarFixture) Register(context.Context, string, string) error {
	r.registrations++
	return r.err
}
func (r *domainRegistrarFixture) Connect(context.Context, string, string) error {
	r.connections++
	return nil
}
func TestSandboxDomainPurchaseRequiresPaymentAndPersistsConnection(t *testing.T) {
	s, _, owner, session, _, base := domainOrderFixture(t)
	q := domainQuoteFunc(func(ctx context.Context, name string) (domains.RegistrarQuote, error) {
		q, e := base(ctx, name)
		q.Environment = "sandbox"
		return q, e
	})
	quote, err := s.RequestDomainQuote(t.Context(), q, session.Token, owner.WorkspaceID, "sandbox-example.com", 0)
	if err != nil {
		t.Fatal(err)
	}
	order, err := s.RequestDomainOrder(t.Context(), q, session.Token, owner.WorkspaceID, quote.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The fixture workspace owns a single project only after this insert.
	project := randomToken()
	if _, err = s.db.Exec("INSERT INTO projects(id,workspace_id,name,kind) VALUES(?,?,?,'static')", project, owner.WorkspaceID, "Domain website"); err != nil {
		t.Fatal(err)
	}
	payment := &domainPayFixture{}
	registrar := &domainRegistrarFixture{}
	d := NewDomainPurchases(s, q, payment, registrar)
	if _, err = d.Start(t.Context(), "invalid", owner.WorkspaceID, order.ID, project); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	purchase, err := d.Start(t.Context(), session.Token, owner.WorkspaceID, order.ID, project)
	if err != nil || purchase.State != "payment_pending" || purchase.CheckoutURL == "" || registrar.registrations != 0 {
		t.Fatal(purchase, err)
	}
	if _, err = s.CancelDomainOrder(t.Context(), session.Token, owner.WorkspaceID, order.ID); !errors.Is(err, ErrDomainOrderConflict) {
		t.Fatal("checkout canceled without payment reconciliation", err)
	}
	payment.paid = true
	d.process(t.Context(), order.ID)
	purchase, err = d.Sync(t.Context(), session.Token, owner.WorkspaceID, order.ID)
	if err != nil || purchase.State != "connected" || purchase.ProjectID != project || registrar.registrations != 1 || registrar.connections != 1 {
		t.Fatal(purchase, err)
	}
	restarted := NewDomainPurchases(s, q, payment, registrar)
	if _, err = restarted.Sync(t.Context(), session.Token, owner.WorkspaceID, order.ID); err != nil {
		t.Fatal(err)
	}
	if payment.creates != 1 || registrar.registrations != 1 || registrar.connections != 1 {
		t.Fatal("completed purchase repeated")
	}
	list, err := restarted.List(t.Context(), session.Token, owner.WorkspaceID)
	if err != nil || len(list) != 1 || list[0].State != "connected" {
		t.Fatal(list, err)
	}
}
