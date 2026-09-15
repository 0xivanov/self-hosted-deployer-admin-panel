//go:build integration

package portal

import (
	"context"
	"errors"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/merchantbilling"
	"net/http"
	"strings"
	"testing"
	"time"
)

type refundProvider struct {
	shopProvider
	createRefund func(context.Context, string, merchantbilling.RefundRequest) (merchantbilling.Refund, error)
	readRefund   func(context.Context, string, string, merchantbilling.RefundRequest) (merchantbilling.Refund, error)
}

func (p refundProvider) CreateRefund(c context.Context, a string, r merchantbilling.RefundRequest) (merchantbilling.Refund, error) {
	return p.createRefund(c, a, r)
}
func (p refundProvider) RetrieveRefund(c context.Context, a, id string, r merchantbilling.RefundRequest) (merchantbilling.Refund, error) {
	return p.readRefund(c, a, id, r)
}
func paidOrderFixture(t *testing.T) (*Store, string, Account, Session, MerchantOrder) {
	t.Helper()
	s, path, a, session, product := orderFixture(t)
	order, err := s.RequestMerchantOrder(t.Context(), randomToken(), product.ID, product.Revision, randomToken())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("UPDATE merchant_orders SET state='complete',payment_status='paid',session_id='cs_test_paid',payment_intent_id='pi_paid' WHERE id=?", order.ID); err != nil {
		t.Fatal(err)
	}
	return s, path, a, session, order
}
func TestMerchantRefundLostReplyAndIsolation(t *testing.T) {
	s, path, a, session, order := paidOrderFixture(t)
	ctx := t.Context()
	r, err := s.RequestMerchantRefund(ctx, session.Token, a.WorkspaceID, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	repeat, err := s.RequestMerchantRefund(ctx, session.Token, a.WorkspaceID, order.ID)
	if err != nil || repeat.ID != r.ID {
		t.Fatal(repeat, err)
	}
	other, foreign := verifiedAccount(t, s, "refund-foreign@example.test")
	if _, err = s.RequestMerchantRefund(ctx, foreign.Token, other.WorkspaceID, order.ID); !errors.Is(err, ErrDenied) {
		t.Fatal("foreign order", err)
	}
	calls := 0
	p := refundProvider{createRefund: func(_ context.Context, account string, input merchantbilling.RefundRequest) (merchantbilling.Refund, error) {
		calls++
		if account != "acct_orders" || input.RequestID != r.ID || input.OrderID != order.ID || input.PaymentIntentID != "pi_paid" || input.AmountMinor != 1250 || input.Currency != "eur" {
			t.Fatal("refund binding", input)
		}
		return merchantbilling.Refund{}, errors.New("lost reply")
	}}
	if _, err = s.DispatchMerchantRefund(ctx, r.ID, p); err == nil {
		t.Fatal("unknown outcome")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.DispatchMerchantRefund(ctx, r.ID, p); !errors.Is(err, ErrBillingConflict) || calls != 1 {
		t.Fatal("duplicate refund", calls, err)
	}
	p.readRefund = func(_ context.Context, account, id string, input merchantbilling.RefundRequest) (merchantbilling.Refund, error) {
		if account != "acct_orders" || input.PaymentIntentID != "pi_paid" {
			t.Fatal("wrong retrieval")
		}
		return merchantbilling.Refund{ID: id, State: "succeeded", ObservedAt: time.Now().Unix()}, nil
	}
	saved, err := s.ReconcileMerchantRefund(ctx, r.ID, "re_saved", p)
	if err != nil || saved.State != "succeeded" {
		t.Fatal(saved, err)
	}
	p.readRefund = func(context.Context, string, string, merchantbilling.RefundRequest) (merchantbilling.Refund, error) {
		return merchantbilling.Refund{ID: "re_saved", State: "failed", ObservedAt: time.Now().Unix()}, nil
	}
	if updated, e := s.ReconcileMerchantRefund(ctx, r.ID, "re_saved", p); e != nil || updated.State != "failed" {
		t.Fatal("late bank failure not recorded", updated, e)
	}
	if _, err = s.MerchantRefunds(ctx, foreign.Token, a.WorkspaceID); !errors.Is(err, ErrDenied) {
		t.Fatal("foreign history", err)
	}
}
func TestMerchantRefundOwnerHTTP(t *testing.T) {
	s, _, a, session, order := paidOrderFixture(t)
	p := refundProvider{createRefund: func(context.Context, string, merchantbilling.RefundRequest) (merchantbilling.Refund, error) {
		return merchantbilling.Refund{ID: "re_http", State: "pending", ObservedAt: time.Now().Unix()}, nil
	}}
	h, err := NewHTTP(s, HTTPOptions{Origin: "https://portal.example.test", Merchant: p, MerchantCountries: []string{"BG"}})
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: h.cookie, Value: session.Token}
	body := `{"workspace":"` + a.WorkspaceID + `","order":"` + order.ID + `"}`
	if w := portalRequest(h, "POST", "/api/merchant/refunds", body, h.origin, "", cookie); w.Code != 403 {
		t.Fatal("csrf", w.Code)
	}
	w := portalRequest(h, "POST", "/api/merchant/refunds", body, h.origin, csrfFor(session.Token), cookie)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"pending"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, secret := range []string{"re_http", "acct_orders", "pi_paid", "provider_id", "actor_id"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("private field", secret)
		}
	}
	w = portalRequest(h, "GET", "/api/merchant/refunds?workspace="+a.WorkspaceID, "", "", "", cookie)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"enabled":true`) {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestMerchantRefundRejectsUnpaidAndRevokedActor(t *testing.T) {
	s, _, a, session, order := paidOrderFixture(t)
	ctx := t.Context()
	if _, err := s.db.Exec("UPDATE merchant_orders SET payment_status='unpaid' WHERE id=?", order.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RequestMerchantRefund(ctx, session.Token, a.WorkspaceID, order.ID); !errors.Is(err, ErrBillingConflict) {
		t.Fatal("unpaid refund", err)
	}
	if _, err := s.db.Exec("UPDATE merchant_orders SET payment_status='paid' WHERE id=?", order.ID); err != nil {
		t.Fatal(err)
	}
	r, err := s.RequestMerchantRefund(ctx, session.Token, a.WorkspaceID, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("UPDATE users SET disabled=1 WHERE id=?", a.ID); err != nil {
		t.Fatal(err)
	}
	p := refundProvider{createRefund: func(context.Context, string, merchantbilling.RefundRequest) (merchantbilling.Refund, error) {
		t.Fatal("revoked actor sent refund")
		return merchantbilling.Refund{}, nil
	}}
	if _, err = s.DispatchMerchantRefund(ctx, r.ID, p); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
}

func TestMerchantRefundOlderCreateReplyCannotOverwriteRefresh(t *testing.T) {
	s, _, a, session, order := paidOrderFixture(t)
	ctx := t.Context()
	r, err := s.RequestMerchantRefund(ctx, session.Token, a.WorkspaceID, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	p := refundProvider{createRefund: func(context.Context, string, merchantbilling.RefundRequest) (merchantbilling.Refund, error) {
		close(entered)
		<-release
		return merchantbilling.Refund{ID: "re_race", State: "pending", ObservedAt: time.Now().Unix()}, nil
	}, readRefund: func(context.Context, string, string, merchantbilling.RefundRequest) (merchantbilling.Refund, error) {
		return merchantbilling.Refund{ID: "re_race", State: "succeeded", ObservedAt: time.Now().Unix()}, nil
	}}
	go func() { _, err := s.DispatchMerchantRefund(ctx, r.ID, p); done <- err }()
	<-entered
	if _, err = s.DispatchMerchantRefund(ctx, r.ID, p); !errors.Is(err, ErrBillingConflict) {
		close(release)
		t.Fatal("concurrent dispatch", err)
	}
	saved, err := s.ReconcileMerchantRefund(ctx, r.ID, "re_race", p)
	close(release)
	if err != nil || saved.State != "succeeded" {
		t.Fatal(saved, err)
	}
	if err = <-done; !errors.Is(err, ErrBillingConflict) {
		t.Fatal("older create overwritten refund", err)
	}
}
