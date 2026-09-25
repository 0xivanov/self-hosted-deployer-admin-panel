//go:build integration

package portal

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestDomainQuoteRetentionPreservesOrdersAndOtherWorkspaces(t *testing.T) {
	s, _, owner, session, q, p := domainOrderFixture(t)
	order, err := s.RequestDomainOrder(t.Context(), p, session.Token, owner.WorkspaceID, q.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CancelDomainOrder(t.Context(), session.Token, owner.WorkspaceID, order.ID); err != nil {
		t.Fatal(err)
	}
	other, _ := verifiedAccount(t, s, "retention-other@example.test")
	old := q.Offer
	old.ExpiresAt = s.now().Add(-25 * time.Hour)
	raw, _ := json.Marshal(old)
	if _, err = s.db.Exec("UPDATE domain_quotes SET offer=?,created_at=? WHERE id=?", raw, s.now().Add(-26*time.Hour).Unix(), q.ID); err != nil {
		t.Fatal(err)
	}
	// Fill the remaining capacity with expired, unreferenced quotes.
	if _, err = s.db.Exec("WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<999) INSERT INTO domain_quotes SELECT 'old-'||x,?,?, '{}',?,? FROM n", owner.WorkspaceID, owner.ID, raw, s.now().Add(-26*time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("INSERT INTO domain_quotes VALUES('other-old',?,?,'{}',?,?)", other.WorkspaceID, other.ID, raw, s.now().Add(-26*time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RequestDomainQuote(t.Context(), p, session.Token, other.WorkspaceID, "other.com", 0); !errors.Is(err, ErrDenied) {
		t.Fatal("unauthorized cleanup", err)
	}
	var count int
	if err = s.db.QueryRow("SELECT count(*) FROM domain_quotes WHERE workspace_id=?", owner.WorkspaceID).Scan(&count); err != nil || count != 1000 {
		t.Fatal(count, err)
	}
	if _, err = s.RequestDomainQuote(t.Context(), p, session.Token, owner.WorkspaceID, "new.com", 0); err != nil {
		t.Fatal("capacity not reclaimed", err)
	}
	if err = s.db.QueryRow("SELECT count(*) FROM domain_quotes WHERE workspace_id=?", owner.WorkspaceID).Scan(&count); err != nil || count != 2 {
		t.Fatal(count, err)
	}
	if _, err = s.DomainQuote(t.Context(), session.Token, owner.WorkspaceID, q.ID); err != nil {
		t.Fatal("order evidence removed", err)
	}
	if err = s.db.QueryRow("SELECT count(*) FROM domain_quotes WHERE id='other-old'").Scan(&count); err != nil || count != 1 {
		t.Fatal("other workspace changed", count, err)
	}
}

func TestDomainQuoteRetentionKeepsRecentAndMalformedOffers(t *testing.T) {
	s, _, owner, session, q, p := domainOrderFixture(t)
	recent := q.Offer
	recent.ExpiresAt = s.now().Add(-time.Hour)
	raw, _ := json.Marshal(recent)
	if _, err := s.db.Exec("UPDATE domain_quotes SET offer=?,created_at=0 WHERE id=?", raw, q.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("INSERT INTO domain_quotes VALUES('malformed',?,?,'{}','{}',0)", owner.WorkspaceID, owner.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RequestDomainQuote(t.Context(), p, session.Token, owner.WorkspaceID, "new.com", 0); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRow("SELECT count(*) FROM domain_quotes WHERE workspace_id=?", owner.WorkspaceID).Scan(&count); err != nil || count != 3 {
		t.Fatal(count, err)
	}
}
