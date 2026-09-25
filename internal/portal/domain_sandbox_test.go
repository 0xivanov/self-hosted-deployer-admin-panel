//go:build integration

package portal

import (
	"context"
	"errors"
	"testing"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/domains"
)

func TestSandboxDomainEvidencePersistsAndCannotSwitchEnvironment(t *testing.T) {
	s, _, owner, session, _, base := domainOrderFixture(t)
	sandbox := domainQuoteFunc(func(ctx context.Context, name string) (domains.RegistrarQuote, error) {
		q, err := base(ctx, name)
		q.Environment = "sandbox"
		return q, err
	})
	q, err := s.RequestDomainQuote(t.Context(), sandbox, session.Token, owner.WorkspaceID, "sandbox-example.com", 0)
	if err != nil || q.Offer.Environment != "sandbox" {
		t.Fatal(q, err)
	}
	saved, err := s.DomainQuote(t.Context(), session.Token, owner.WorkspaceID, q.ID)
	if err != nil || saved.Offer.Environment != "sandbox" {
		t.Fatal(saved, err)
	}
	if _, err = s.RequestDomainOrder(t.Context(), base, session.Token, owner.WorkspaceID, q.ID); !errors.Is(err, ErrDomainOrderConflict) {
		t.Fatal("provider environment changed", err)
	}
	order, err := s.RequestDomainOrder(t.Context(), sandbox, session.Token, owner.WorkspaceID, q.ID)
	if err != nil || order.Offer.Environment != "sandbox" {
		t.Fatal(order, err)
	}
	orders, err := s.DomainOrders(t.Context(), session.Token, owner.WorkspaceID)
	if err != nil || len(orders) != 1 || orders[0].Offer.Environment != "sandbox" {
		t.Fatal(orders, err)
	}
}
