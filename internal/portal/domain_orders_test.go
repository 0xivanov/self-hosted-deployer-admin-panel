//go:build integration

package portal

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/domains"
	"net/http"
	"testing"
	"time"
)

func domainOrderFixture(t *testing.T) (*Store, string, Account, Session, DomainQuote, domainQuoteFunc) {
	t.Helper()
	s, path := newStore(t)
	a, session := verifiedAccount(t, s, "domain-order@example.test")
	p := domainQuoteFunc(func(_ context.Context, name string) (domains.RegistrarQuote, error) {
		return domains.RegistrarQuote{Domain: name, Available: true, PremiumChecked: true, Currency: "eur", RegistrationMinor: 900, RenewalMinor: 1000, CheckedAt: time.Now()}, nil
	})
	q, err := s.RequestDomainQuote(t.Context(), p, session.Token, a.WorkspaceID, "example.com", 200)
	if err != nil {
		t.Fatal(err)
	}
	return s, path, a, session, q, p
}
func TestDomainOrderPrepareReplayCancelAndIsolation(t *testing.T) {
	s, path, a, session, q, p := domainOrderFixture(t)
	ctx := t.Context()
	order, err := s.RequestDomainOrder(ctx, p, session.Token, a.WorkspaceID, q.ID)
	if err != nil || order.State != "awaiting_payment" || order.Offer.RegistrationMinor != 1100 {
		t.Fatal(order, err)
	}
	repeat, err := s.RequestDomainOrder(ctx, p, session.Token, a.WorkspaceID, q.ID)
	if err != nil || repeat.ID != order.ID {
		t.Fatal(repeat, err)
	}
	other, foreign := verifiedAccount(t, s, "domain-order-other@example.test")
	if _, err = s.RequestDomainOrder(ctx, p, foreign.Token, other.WorkspaceID, q.ID); !errors.Is(err, ErrDenied) {
		t.Fatal("foreign quote", err)
	}
	if _, err = s.CancelDomainOrder(ctx, foreign.Token, a.WorkspaceID, order.ID); !errors.Is(err, ErrDenied) {
		t.Fatal("foreign cancel", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	canceled, err := s.CancelDomainOrder(ctx, session.Token, a.WorkspaceID, order.ID)
	if err != nil || canceled.State != "canceled" {
		t.Fatal(canceled, err)
	}
	if _, err = s.CancelDomainOrder(ctx, session.Token, a.WorkspaceID, order.ID); err != nil {
		t.Fatal(err)
	}
	orders, err := s.DomainOrders(ctx, session.Token, a.WorkspaceID)
	if err != nil || len(orders) != 1 || orders[0].State != "canceled" {
		t.Fatal(orders, err)
	}
	if replay, err := s.RequestDomainOrder(ctx, p, session.Token, a.WorkspaceID, q.ID); err != nil || replay.State != "canceled" {
		t.Fatal("canceled order resurrected", replay, err)
	}
}
func TestDomainOrderRejectsChangedAvailabilityOrPrice(t *testing.T) {
	for _, change := range []string{"price", "renewal", "currency", "availability", "premium", "expired"} {
		t.Run(change, func(t *testing.T) {
			s, _, a, session, q, p := domainOrderFixture(t)
			changed := domainQuoteFunc(func(ctx context.Context, name string) (domains.RegistrarQuote, error) {
				v, err := p.QuoteDomain(ctx, name)
				switch change {
				case "price":
					v.RegistrationMinor++
				case "renewal":
					v.RenewalMinor++
				case "currency":
					v.Currency = "usd"
				case "availability":
					v.Available = false
				case "premium":
					v.Premium = true
				case "expired":
					v.CheckedAt = time.Now().Add(-6 * time.Minute)
				}
				return v, err
			})
			if _, err := s.RequestDomainOrder(t.Context(), changed, session.Token, a.WorkspaceID, q.ID); err == nil {
				t.Fatal("invalid quote accepted")
			}
			orders, err := s.DomainOrders(t.Context(), session.Token, a.WorkspaceID)
			if err != nil || len(orders) != 0 {
				t.Fatal(orders, err)
			}
		})
	}
}
func TestDomainOrderRechecksOwnerAfterProvider(t *testing.T) {
	s, _, a, session, q, p := domainOrderFixture(t)
	changed := domainQuoteFunc(func(ctx context.Context, name string) (domains.RegistrarQuote, error) {
		if _, err := s.db.Exec("UPDATE users SET disabled=1 WHERE id=?", a.ID); err != nil {
			t.Fatal(err)
		}
		return p.QuoteDomain(ctx, name)
	})
	if _, err := s.RequestDomainOrder(t.Context(), changed, session.Token, a.WorkspaceID, q.ID); !errors.Is(err, ErrDenied) {
		t.Fatal("revoked owner", err)
	}
}

func TestDomainOrderHTTPPrepareCancel(t *testing.T) {
	s, _, a, session, q, p := domainOrderFixture(t)
	h, err := NewHTTP(s, HTTPOptions{Origin: "https://portal.example.test", DomainQuotes: p})
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: h.cookie, Value: session.Token}
	body := `{"workspace":"` + a.WorkspaceID + `","quote":"` + q.ID + `"}`
	if w := portalRequest(h, "POST", "/api/domains/orders", body, h.origin, "", cookie); w.Code != 403 {
		t.Fatal("csrf", w.Code)
	}
	w := portalRequest(h, "POST", "/api/domains/orders", body, h.origin, csrfFor(session.Token), cookie)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var order DomainOrder
	if err = json.Unmarshal(w.Body.Bytes(), &order); err != nil || order.State != "awaiting_payment" {
		t.Fatal(order, err)
	}
	if w = portalRequest(h, "POST", "/api/domains/orders/cancel", `{"workspace":"`+a.WorkspaceID+`","id":"`+order.ID+`"}`, h.origin, csrfFor(session.Token), cookie); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = portalRequest(h, "GET", "/api/domains/orders?workspace="+a.WorkspaceID, "", "", "", nil); w.Code != 401 {
		t.Fatal("anonymous", w.Code)
	}
}
