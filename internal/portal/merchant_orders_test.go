//go:build integration

package portal

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/merchantbilling"
)

type checkoutProviderFixture struct {
	create func(context.Context, string, merchantbilling.CheckoutOrder) (merchantbilling.Checkout, error)
	read   func(context.Context, string, string, merchantbilling.CheckoutOrder) (merchantbilling.Checkout, error)
}

func (p checkoutProviderFixture) CreateCheckout(ctx context.Context, account string, o merchantbilling.CheckoutOrder) (merchantbilling.Checkout, error) {
	return p.create(ctx, account, o)
}
func (p checkoutProviderFixture) RetrieveCheckout(ctx context.Context, account, id string, o merchantbilling.CheckoutOrder) (merchantbilling.Checkout, error) {
	return p.read(ctx, account, id, o)
}
func orderFixture(t *testing.T) (*Store, string, Account, Session, MerchantProduct) {
	t.Helper()
	s, path := newStore(t)
	a, session := verifiedAccount(t, s, "order-merchant@example.test")
	ctx := t.Context()
	intent, err := s.RequestMerchantAccount(ctx, session.Token, a.WorkspaceID, "BG")
	if err != nil {
		t.Fatal(err)
	}
	p := merchantProviderFixture{create: func(_ context.Context, country, request string) (merchantbilling.Account, error) {
		return merchantbilling.Account{ID: "acct_orders", Country: country, RequestID: request, DetailsSubmitted: true, ChargesEnabled: true, PayoutsEnabled: true, CardPayments: "active", ObservedAt: time.Now().Unix()}, nil
	}}
	if _, err = s.DispatchMerchantAccount(ctx, intent.RequestID, p); err != nil {
		t.Fatal(err)
	}
	active := true
	product, err := s.SaveMerchantProduct(ctx, session.Token, MerchantProductInput{Workspace: a.WorkspaceID, Key: randomToken(), Name: "Product", Currency: "eur", AmountMinor: 1250, Active: &active})
	if err != nil {
		t.Fatal(err)
	}
	return s, path, a, session, product
}
func openCheckout(id string) merchantbilling.Checkout {
	return merchantbilling.Checkout{ID: id, URL: "https://checkout.stripe.com/c/pay/" + id, State: "open", PaymentStatus: "unpaid", ObservedAt: time.Now().Unix()}
}

func TestMerchantOrderPriceConsentAndBuyerIsolation(t *testing.T) {
	t.Parallel()
	s, _, a, session, product := orderFixture(t)
	ctx := t.Context()
	buyer, key := randomToken(), randomToken()
	order, err := s.RequestMerchantOrder(ctx, buyer, product.ID, product.Revision, key)
	if err != nil || order.AmountMinor != 1250 || order.State != "requested" {
		t.Fatal(order, err)
	}
	repeat, err := s.RequestMerchantOrder(ctx, buyer, product.ID, product.Revision, key)
	if err != nil || repeat.ID != order.ID {
		t.Fatal(repeat, err)
	}
	active := true
	edited, err := s.SaveMerchantProduct(ctx, session.Token, MerchantProductInput{Workspace: a.WorkspaceID, ID: product.ID, Name: "New name", Currency: "usd", AmountMinor: 2000, Active: &active, Revision: product.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RequestMerchantOrder(ctx, buyer, product.ID, product.Revision, randomToken()); err == nil {
		t.Fatal("stale price consent")
	}
	if _, err = s.RequestMerchantOrder(ctx, buyer, product.ID, edited.Revision, key); err == nil {
		t.Fatal("request changed product revision")
	}
	if _, err = s.BuyerMerchantOrder(ctx, randomToken(), order.ID); !errors.Is(err, ErrDenied) {
		t.Fatal("other buyer", err)
	}
	p := checkoutProviderFixture{create: func(_ context.Context, account string, input merchantbilling.CheckoutOrder) (merchantbilling.Checkout, error) {
		if account != "acct_orders" || input.RequestID != order.ID || input.Name != "Product" || input.Currency != "eur" || input.AmountMinor != 1250 {
			t.Fatal("changed agreed checkout", input)
		}
		return openCheckout("cs_test_saved"), nil
	}}
	bound, err := s.DispatchMerchantOrder(ctx, order.ID, p)
	if err != nil || bound.State != "open" {
		t.Fatal(bound, err)
	}
	p.create = func(context.Context, string, merchantbilling.CheckoutOrder) (merchantbilling.Checkout, error) {
		t.Fatal("duplicate checkout")
		return merchantbilling.Checkout{}, nil
	}
	if _, err = s.DispatchMerchantOrder(ctx, order.ID, p); err != nil {
		t.Fatal(err)
	}
	list, err := s.MerchantOrders(ctx, session.Token, a.WorkspaceID)
	if err != nil || len(list) != 1 {
		t.Fatal(list, err)
	}
	_, foreign := verifiedAccount(t, s, "order-other@example.test")
	if _, err = s.MerchantOrders(ctx, foreign.Token, a.WorkspaceID); !errors.Is(err, ErrDenied) {
		t.Fatal("other merchant", err)
	}
}

func TestMerchantOrderLostReplyAndPaidReconciliation(t *testing.T) {
	t.Parallel()
	s, path, _, _, product := orderFixture(t)
	ctx := t.Context()
	buyer := randomToken()
	order, err := s.RequestMerchantOrder(ctx, buyer, product.ID, product.Revision, randomToken())
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	p := checkoutProviderFixture{create: func(context.Context, string, merchantbilling.CheckoutOrder) (merchantbilling.Checkout, error) {
		calls.Add(1)
		close(entered)
		<-release
		return merchantbilling.Checkout{}, errors.New("lost reply")
	}}
	result := make(chan error, 1)
	go func() { _, err := s.DispatchMerchantOrder(ctx, order.ID, p); result <- err }()
	<-entered
	_, duplicateErr := s.DispatchMerchantOrder(ctx, order.ID, p)
	close(release)
	err = <-result
	if !errors.Is(duplicateErr, ErrBillingConflict) || err == nil {
		t.Fatal(duplicateErr, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.DispatchMerchantOrder(ctx, order.ID, p); !errors.Is(err, ErrBillingConflict) {
		t.Fatal("restarted creation", err)
	}
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
	p.read = func(_ context.Context, account, id string, input merchantbilling.CheckoutOrder) (merchantbilling.Checkout, error) {
		v := openCheckout(id)
		v.State = "complete"
		v.PaymentStatus = "paid"
		v.PaymentIntentID = "pi_order"
		v.URL = ""
		return v, nil
	}
	paid, err := s.ReconcileMerchantOrder(ctx, order.ID, "cs_test_recovered", p)
	if err != nil || paid.PaymentStatus != "paid" {
		t.Fatal(paid, err)
	}
	p.read = func(_ context.Context, account, id string, input merchantbilling.CheckoutOrder) (merchantbilling.Checkout, error) {
		return openCheckout(id), nil
	}
	if _, err = s.ReconcileMerchantOrder(ctx, order.ID, "cs_test_recovered", p); !errors.Is(err, ErrBillingConflict) {
		t.Fatal("regressed paid order", err)
	}
	if _, err = s.ReconcileMerchantOrder(ctx, order.ID, "cs_test_other", p); !errors.Is(err, ErrBillingConflict) {
		t.Fatal("replaced checkout", err)
	}
}

func TestMerchantOrderRejectsUnavailableSales(t *testing.T) {
	t.Parallel()
	s, _, a, session, product := orderFixture(t)
	ctx := t.Context()
	order, err := s.RequestMerchantOrder(ctx, randomToken(), product.ID, product.Revision, randomToken())
	if err != nil {
		t.Fatal(err)
	}
	disabled := false
	if _, err = s.SaveMerchantProduct(ctx, session.Token, MerchantProductInput{Workspace: a.WorkspaceID, ID: product.ID, Name: product.Name, Currency: product.Currency, AmountMinor: product.AmountMinor, Active: &disabled, Revision: product.Revision}); err != nil {
		t.Fatal(err)
	}
	p := checkoutProviderFixture{create: func(context.Context, string, merchantbilling.CheckoutOrder) (merchantbilling.Checkout, error) {
		t.Fatal("disabled product checkout")
		return merchantbilling.Checkout{}, nil
	}}
	if _, err = s.DispatchMerchantOrder(ctx, order.ID, p); err == nil {
		t.Fatal("disabled product")
	}
	if _, err = s.RequestMerchantOrder(ctx, randomToken(), product.ID, product.Revision, randomToken()); err == nil {
		t.Fatal("disabled product order")
	}
}

func TestMerchantOrderRequiresFreshReadyAccount(t *testing.T) {
	t.Parallel()
	s, _, _, _, product := orderFixture(t)
	ctx := t.Context()
	for _, kind := range []string{"stale", "charges", "details", "payouts", "card", "identity"} {
		t.Run(kind, func(t *testing.T) {
			var raw []byte
			if err := s.db.QueryRow("SELECT snapshot FROM merchant_accounts WHERE account_id='acct_orders'").Scan(&raw); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if _, err := s.db.Exec("UPDATE merchant_accounts SET snapshot=? WHERE account_id='acct_orders'", raw); err != nil {
					t.Error(err)
				}
			}()
			var snapshot merchantbilling.Account
			if err := json.Unmarshal(raw, &snapshot); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "stale":
				snapshot.ObservedAt -= 300
			case "charges":
				snapshot.ChargesEnabled = false
			case "details":
				snapshot.DetailsSubmitted = false
			case "payouts":
				snapshot.PayoutsEnabled = false
			case "card":
				snapshot.CardPayments = "pending"
			case "identity":
				snapshot.ID = "acct_foreign"
			}
			changed, _ := json.Marshal(snapshot)
			if _, err := s.db.Exec("UPDATE merchant_accounts SET snapshot=? WHERE account_id='acct_orders'", changed); err != nil {
				t.Fatal(err)
			}
			if _, err := s.RequestMerchantOrder(ctx, randomToken(), product.ID, product.Revision, randomToken()); err == nil {
				t.Fatal("unready merchant accepted", kind)
			}
		})
	}
}

func TestMerchantOrderReconciliationIdentityAndGeneration(t *testing.T) {
	t.Parallel()
	s, _, _, _, product := orderFixture(t)
	ctx := t.Context()
	order, err := s.RequestMerchantOrder(ctx, randomToken(), product.ID, product.Revision, randomToken())
	if err != nil {
		t.Fatal(err)
	}
	p := checkoutProviderFixture{create: func(context.Context, string, merchantbilling.CheckoutOrder) (merchantbilling.Checkout, error) {
		return openCheckout("cs_test_generation"), nil
	}}
	if _, err = s.DispatchMerchantOrder(ctx, order.ID, p); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	slow := p
	slow.read = func(_ context.Context, account, id string, o merchantbilling.CheckoutOrder) (merchantbilling.Checkout, error) {
		close(entered)
		<-release
		return openCheckout(id), nil
	}
	result := make(chan error, 1)
	go func() { _, err := s.ReconcileMerchantOrder(ctx, order.ID, "cs_test_generation", slow); result <- err }()
	<-entered
	p.read = func(_ context.Context, account, id string, o merchantbilling.CheckoutOrder) (merchantbilling.Checkout, error) {
		v := openCheckout(id)
		v.State = "complete"
		v.PaymentStatus = "paid"
		v.PaymentIntentID = "pi_done"
		v.URL = ""
		return v, nil
	}
	_, newErr := s.ReconcileMerchantOrder(ctx, order.ID, "cs_test_generation", p)
	close(release)
	oldErr := <-result
	if newErr != nil || !errors.Is(oldErr, ErrBillingConflict) {
		t.Fatal(newErr, oldErr)
	}
	p.read = func(context.Context, string, string, merchantbilling.CheckoutOrder) (merchantbilling.Checkout, error) {
		return openCheckout("cs_test_foreign"), nil
	}
	if _, err = s.ReconcileMerchantOrder(ctx, order.ID, "cs_test_generation", p); !errors.Is(err, ErrBillingConflict) {
		t.Fatal("wrong returned session", err)
	}
}

func TestMerchantOrderLateCreateCannotReplaceReconciliation(t *testing.T) {
	s, _, _, _, product := orderFixture(t)
	ctx := t.Context()
	order, err := s.RequestMerchantOrder(ctx, randomToken(), product.ID, product.Revision, randomToken())
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	p := checkoutProviderFixture{
		create: func(context.Context, string, merchantbilling.CheckoutOrder) (merchantbilling.Checkout, error) {
			close(entered)
			<-release
			return openCheckout("cs_test_race"), nil
		},
		read: func(context.Context, string, string, merchantbilling.CheckoutOrder) (merchantbilling.Checkout, error) {
			observed := openCheckout("cs_test_race")
			observed.URL = "https://checkout.stripe.com/c/pay/reconciled"
			return observed, nil
		},
	}
	go func() { _, err := s.DispatchMerchantOrder(ctx, order.ID, p); result <- err }()
	<-entered
	_, err = s.ReconcileMerchantOrder(ctx, order.ID, "cs_test_race", p)
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	if err = <-result; !errors.Is(err, ErrBillingConflict) {
		t.Fatal("late create accepted", err)
	}
}

func TestMerchantOrderProviderValidation(t *testing.T) {
	now := time.Now().Unix()
	for _, id := range []string{"cs_test_", "cs_test_bad/id", "cs_test_bad\n", "cs_live_abc"} {
		result := openCheckout(id)
		if validMerchantCheckoutResult(result, now, now) {
			t.Fatalf("accepted session %q", id)
		}
	}
	result := openCheckout("cs_test_valid")
	result.State, result.PaymentStatus, result.PaymentIntentID = "complete", "paid", "pi_"
	if validMerchantCheckoutResult(result, now, now) {
		t.Fatal("accepted empty intent")
	}
}
