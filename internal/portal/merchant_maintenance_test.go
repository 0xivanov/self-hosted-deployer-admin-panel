//go:build integration

package portal

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/merchantbilling"
)

func TestMerchantMaintenanceUpdatesReadinessAndPayments(t *testing.T) {
	s, _, _, _, product := orderFixture(t)
	ctx := t.Context()
	order, err := s.RequestMerchantOrder(ctx, randomToken(), product.ID, product.Revision, randomToken())
	if err != nil {
		t.Fatal(err)
	}
	p := shopProvider{onboardingProvider: onboardingProvider{merchantProviderFixture: merchantProviderFixture{read: func(_ context.Context, id, country, request string) (merchantbilling.Account, error) {
		return merchantbilling.Account{ID: id, Country: country, RequestID: request, ObservedAt: time.Now().Unix(), DetailsSubmitted: true, ChargesEnabled: false, PayoutsEnabled: true, CardPayments: "inactive"}, nil
	}}}, checkoutProviderFixture: checkoutProviderFixture{
		create: func(context.Context, string, merchantbilling.CheckoutOrder) (merchantbilling.Checkout, error) {
			return openCheckout("cs_test_maintenance"), nil
		},
		read: func(_ context.Context, account, id string, request merchantbilling.CheckoutOrder) (merchantbilling.Checkout, error) {
			if account != "acct_orders" || request.RequestID != order.ID {
				t.Fatal("wrong bound order")
			}
			v := openCheckout(id)
			v.State = "complete"
			v.PaymentStatus = "paid"
			v.PaymentIntentID = "pi_maintenance"
			v.URL = ""
			return v, nil
		},
	}}
	if _, err = s.DispatchMerchantOrder(ctx, order.ID, p); err != nil {
		t.Fatal(err)
	}
	p.create = func(context.Context, string, merchantbilling.CheckoutOrder) (merchantbilling.Checkout, error) {
		t.Fatal("worker created checkout")
		return merchantbilling.Checkout{}, nil
	}
	result, err := s.MaintainMerchants(ctx, p, "", "", 10)
	if err != nil || result.AccountsChecked != 1 || result.OrdersChecked != 1 || result.Failures != 0 {
		t.Fatal(result, err)
	}
	var payment string
	if err = s.db.QueryRow("SELECT payment_status FROM merchant_orders WHERE id=?", order.ID).Scan(&payment); err != nil || payment != "paid" {
		t.Fatal(payment, err)
	}
	if _, err = s.RequestMerchantOrder(ctx, randomToken(), product.ID, product.Revision, randomToken()); err == nil {
		t.Fatal("disabled merchant accepted new purchase")
	}
	result, err = s.MaintainMerchants(ctx, p, "", "", 10)
	if err != nil || result.OrdersChecked != 0 {
		t.Fatal("paid order polled again", result, err)
	}
}

func TestMerchantMaintenanceFailureCursorAndUnknownSubmission(t *testing.T) {
	s, _, _, _, product := orderFixture(t)
	ctx := t.Context()
	order, err := s.RequestMerchantOrder(ctx, randomToken(), product.ID, product.Revision, randomToken())
	if err != nil {
		t.Fatal(err)
	}
	p := shopProvider{onboardingProvider: onboardingProvider{merchantProviderFixture: merchantProviderFixture{read: func(context.Context, string, string, string) (merchantbilling.Account, error) {
		return merchantbilling.Account{}, errors.New("private provider failure")
	}}}, checkoutProviderFixture: checkoutProviderFixture{
		create: func(context.Context, string, merchantbilling.CheckoutOrder) (merchantbilling.Checkout, error) {
			return merchantbilling.Checkout{}, errors.New("lost reply")
		},
		read: func(context.Context, string, string, merchantbilling.CheckoutOrder) (merchantbilling.Checkout, error) {
			t.Fatal("unmapped submission fetched")
			return merchantbilling.Checkout{}, nil
		},
	}}
	if _, err = s.DispatchMerchantOrder(ctx, order.ID, p); err == nil {
		t.Fatal("expected unknown submission")
	}
	result, err := s.MaintainMerchants(ctx, p, "", "", 1)
	if err != nil || result.Failures != 1 || result.AccountCursor == "" || result.OrdersChecked != 0 {
		t.Fatal(result, err)
	}
	next, err := s.MaintainMerchants(ctx, p, result.AccountCursor, result.OrderCursor, 1)
	if err != nil || next.AccountCursor != "" || next.AccountsChecked != 0 {
		t.Fatal("cursor did not wrap", next, err)
	}
	if _, err = s.MaintainMerchants(ctx, p, "", "", 101); err == nil {
		t.Fatal("unbounded batch")
	}
}

func TestMerchantMaintenanceTimeoutAdvancesAndNewerReadWins(t *testing.T) {
	s, _, _, _, _ := orderFixture(t)
	ctx := t.Context()
	timeout := shopProvider{onboardingProvider: onboardingProvider{merchantProviderFixture: merchantProviderFixture{read: func(context.Context, string, string, string) (merchantbilling.Account, error) {
		return merchantbilling.Account{}, context.DeadlineExceeded
	}}}}
	result, err := s.MaintainMerchants(ctx, timeout, "", "", 1)
	if err != nil || result.Failures != 1 || result.AccountCursor == "" {
		t.Fatal("individual timeout stopped cursor", result, err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan MerchantMaintenanceResult, 1)
	read := func(_ context.Context, id, country, request string) (merchantbilling.Account, error) {
		return merchantbilling.Account{ID: id, Country: country, RequestID: request, ObservedAt: time.Now().Unix(), ChargesEnabled: false}, nil
	}
	slow := shopProvider{onboardingProvider: onboardingProvider{merchantProviderFixture: merchantProviderFixture{read: func(c context.Context, id, country, request string) (merchantbilling.Account, error) {
		close(entered)
		<-release
		v, e := read(c, id, country, request)
		v.ChargesEnabled = true
		return v, e
	}}}}
	go func() { r, _ := s.MaintainMerchants(ctx, slow, "", "", 1); done <- r }()
	<-entered
	fast := shopProvider{onboardingProvider: onboardingProvider{merchantProviderFixture: merchantProviderFixture{read: read}}}
	result, err = s.MaintainMerchants(ctx, fast, "", "", 1)
	close(release)
	if err != nil || result.Failures != 0 {
		t.Fatal(result, err)
	}
	if old := <-done; old.Failures != 1 {
		t.Fatal("older read accepted", old)
	}
	var enabled bool
	if err = s.db.QueryRow("SELECT json_extract(snapshot,'$.ChargesEnabled') FROM merchant_accounts").Scan(&enabled); err != nil || enabled {
		t.Fatal("old capabilities replaced newer", enabled, err)
	}
}
