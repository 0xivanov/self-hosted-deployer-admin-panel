//go:build integration

package portal

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/merchantbilling"
)

type maintenanceRefundProvider struct {
	states  []string
	reads   int
	creates int
	readErr error
}

func (p *maintenanceRefundProvider) CreateRefund(context.Context, string, merchantbilling.RefundRequest) (merchantbilling.Refund, error) {
	p.creates++
	return merchantbilling.Refund{}, errors.New("create must not be called")
}

func (p *maintenanceRefundProvider) RetrieveRefund(_ context.Context, _ string, id string, _ merchantbilling.RefundRequest) (merchantbilling.Refund, error) {
	p.reads++
	if p.readErr != nil {
		return merchantbilling.Refund{}, p.readErr
	}
	state := p.states[p.reads-1]
	return merchantbilling.Refund{ID: id, State: state, ObservedAt: time.Now().Unix()}, nil
}

func TestMerchantRefundMaintenanceRefreshesLateBankStates(t *testing.T) {
	s, _, _, session, order := paidOrderFixture(t)
	defer s.Close()
	if _, err := s.RequestMerchantRefund(t.Context(), session.Token, order.WorkspaceID, order.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE merchant_refunds SET state='pending',provider_id='re_pending' WHERE order_id=?", order.ID); err != nil {
		t.Fatal(err)
	}
	p := &maintenanceRefundProvider{states: []string{"pending", "succeeded", "failed"}}
	for _, want := range []string{"pending", "succeeded", "failed"} {
		result, err := s.MaintainMerchantRefunds(t.Context(), p, "", 2)
		if err != nil || result.Checked != 1 || result.Failures != 0 || result.Cursor != "" {
			t.Fatalf("maintenance result=%+v err=%v", result, err)
		}
		var state string
		if err = s.db.QueryRow("SELECT state FROM merchant_refunds WHERE order_id=?", order.ID).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state != want {
			t.Fatalf("state=%q want %q", state, want)
		}
	}
	if p.reads != 3 || p.creates != 0 {
		t.Fatalf("provider calls reads=%d creates=%d", p.reads, p.creates)
	}
}

func TestMerchantRefundMaintenanceSkipsUnmappedAndAdvancesFailures(t *testing.T) {
	s, _, _, session, order := paidOrderFixture(t)
	defer s.Close()
	if _, err := s.RequestMerchantRefund(t.Context(), session.Token, order.WorkspaceID, order.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE merchant_refunds SET state='submitted',provider_id='' WHERE order_id=?", order.ID); err != nil {
		t.Fatal(err)
	}
	p := &maintenanceRefundProvider{states: []string{"succeeded"}, readErr: errors.New("provider unavailable")}
	result, err := s.MaintainMerchantRefunds(t.Context(), p, "", 1)
	if err != nil || result.Checked != 0 || result.Failures != 0 || result.Cursor != "" || p.reads != 0 || p.creates != 0 {
		t.Fatalf("unmapped result=%+v err=%v reads=%d creates=%d", result, err, p.reads, p.creates)
	}
	if _, err = s.db.Exec("UPDATE merchant_refunds SET state='pending',provider_id='re_pending' WHERE order_id=?", order.ID); err != nil {
		t.Fatal(err)
	}
	result, err = s.MaintainMerchantRefunds(t.Context(), p, "", 1)
	if err != nil || result.Checked != 0 || result.Failures != 1 || result.Cursor == "" || p.reads != 1 {
		t.Fatalf("failed result=%+v err=%v reads=%d", result, err, p.reads)
	}
	result, err = s.MaintainMerchantRefunds(t.Context(), p, result.Cursor, 1)
	if err != nil || result.Cursor != "" || result.Checked != 0 || result.Failures != 0 {
		t.Fatalf("cursor result=%+v err=%v", result, err)
	}
}
