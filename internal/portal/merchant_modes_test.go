//go:build integration

package portal

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/merchantbilling"
)

type liveMerchantAccountFixture struct{ merchantProviderFixture }

func (liveMerchantAccountFixture) BillingMode() string { return "live" }

type liveMerchantCheckoutFixture struct{ checkoutProviderFixture }

func (liveMerchantCheckoutFixture) BillingMode() string { return "live" }

func TestMerchantLiveModeDispatchAndReconcile(t *testing.T) {
	s, _, owner, session, _ := orderFixture(t)
	ctx := t.Context()
	s.merchantMode = "live"

	intent, err := s.RequestMerchantAccount(ctx, session.Token, owner.WorkspaceID, "BG")
	if err != nil {
		t.Fatal(err)
	}
	accountProvider := liveMerchantAccountFixture{merchantProviderFixture{create: func(_ context.Context, country, request string) (merchantbilling.Account, error) {
		return merchantbilling.Account{ID: "acct_shared", Country: country, RequestID: request, DetailsSubmitted: true, ChargesEnabled: true, PayoutsEnabled: true, CardPayments: "active", ObservedAt: time.Now().Unix()}, nil
	}}}
	if _, err = s.DispatchMerchantAccount(ctx, intent.RequestID, accountProvider); err != nil {
		t.Fatal(err)
	}
	active := true
	product, err := s.SaveMerchantProduct(ctx, session.Token, MerchantProductInput{Workspace: owner.WorkspaceID, Key: randomToken(), Name: "Live product", Currency: "eur", AmountMinor: 1250, Active: &active})
	if err != nil {
		t.Fatal(err)
	}
	order, err := s.RequestMerchantOrder(ctx, randomToken(), product.ID, product.Revision, randomToken())
	if err != nil {
		t.Fatal(err)
	}
	provider := liveMerchantCheckoutFixture{checkoutProviderFixture{
		create: func(context.Context, string, merchantbilling.CheckoutOrder) (merchantbilling.Checkout, error) {
			return openCheckout("cs_live_shared"), nil
		},
		read: func(context.Context, string, string, merchantbilling.CheckoutOrder) (merchantbilling.Checkout, error) {
			result := openCheckout("cs_live_shared")
			result.ObservedAt = time.Now().Unix()
			return result, nil
		},
	}}
	bound, err := s.DispatchMerchantOrder(ctx, order.ID, provider)
	if err != nil || bound.SessionID != "cs_live_shared" {
		t.Fatalf("live dispatch: %+v %v", bound, err)
	}
	reconciled, err := s.ReconcileMerchantOrder(ctx, order.ID, "cs_live_shared", provider)
	if err != nil || reconciled.SessionID != "cs_live_shared" {
		t.Fatalf("live reconcile: %+v %v", reconciled, err)
	}

	s.merchantMode = "test"
	var testCount, liveCount int
	if err = s.db.QueryRow("SELECT count(*) FROM merchant_orders WHERE mode='test' AND id=?", order.ID).Scan(&testCount); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRow("SELECT count(*) FROM merchant_orders WHERE mode='live' AND id=?", order.ID).Scan(&liveCount); err != nil {
		t.Fatal(err)
	}
	if testCount != 0 || liveCount != 1 {
		t.Fatalf("mode isolation counts test=%d live=%d", testCount, liveCount)
	}
}

func TestMerchantModesHideSandboxDataAndBuyerRecovery(t *testing.T) {
	s, _, owner, session, product := orderFixture(t)
	buyer := newBuyerToken(t, s)
	order, err := s.RequestMerchantOrder(t.Context(), buyer, product.ID, product.Revision, randomToken())
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := s.IssueMerchantOrderRecoveryCode(t.Context(), buyer, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	s.merchantMode = "live"
	if _, err = s.MerchantAccount(t.Context(), session.Token, owner.WorkspaceID); !errors.Is(err, ErrDenied) {
		t.Fatalf("live read sandbox account: %v", err)
	}
	if _, err = s.BuyerMerchantOrder(t.Context(), buyer, order.ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("live read sandbox order: %v", err)
	}
	liveBuyer := newBuyerToken(t, s)
	if _, err = s.RedeemMerchantOrderRecoveryCode(t.Context(), liveBuyer, recovery.Code); !errors.Is(err, ErrDenied) {
		t.Fatalf("live redeemed sandbox recovery: %v", err)
	}
	if _, err = s.RequestMerchantOrder(t.Context(), liveBuyer, product.ID, product.Revision, randomToken()); !errors.Is(err, ErrDenied) {
		t.Fatalf("live bought sandbox product: %v", err)
	}
	if err = s.RevokeMerchantBuyer(t.Context(), buyer); err != nil {
		t.Fatal(err)
	}
	s.merchantMode = "test"
	if _, err = s.BuyerMerchantOrder(t.Context(), buyer, order.ID); err != nil {
		t.Fatalf("live revocation changed sandbox session: %v", err)
	}
}

func TestMerchantModesMigrationPreservesHistoryAndForeignKeys(t *testing.T) {
	s, _, owner, session, product := orderFixture(t)
	buyer := newBuyerToken(t, s)
	order, err := s.RequestMerchantOrder(t.Context(), buyer, product.ID, product.Revision, randomToken())
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := s.IssueMerchantOrderRecoveryCode(t.Context(), buyer, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	second := newBuyerToken(t, s)
	if _, err = s.RedeemMerchantOrderRecoveryCode(t.Context(), second, recovery.Code); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("UPDATE merchant_orders SET state='complete',payment_status='paid',session_id='cs_test_paid',payment_intent_id='pi_paid' WHERE id=?", order.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RequestMerchantRefund(t.Context(), session.Token, owner.WorkspaceID, order.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("INSERT INTO merchant_events(id,account_id,order_id,session_id,event_type,body_hash,created_at,received_at) VALUES('evt_old','acct_orders',?,'cs_test_paid','checkout.session.completed','hash',1,2)", order.ID); err != nil {
		t.Fatal(err)
	}
	downgradeMerchantSchema55(t, s)
	if err = s.migrateMerchantModes(); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"merchant_accounts", "merchant_products", "merchant_orders", "merchant_events", "merchant_refunds", "merchant_buyer_sessions", "merchant_order_recovery_codes", "merchant_order_recovery_grants"} {
		var before, after int
		if err = s.db.QueryRow("SELECT count(*) FROM fixture_" + table).Scan(&before); err != nil {
			t.Fatal(err)
		}
		if err = s.db.QueryRow("SELECT count(*) FROM " + table + " WHERE mode='test'").Scan(&after); err != nil || before != after || before == 0 {
			t.Fatalf("%s history counts %d/%d: %v", table, before, after, err)
		}
		var columns []string
		rows, queryErr := s.db.Query("PRAGMA table_info(fixture_" + table + ")")
		if queryErr != nil {
			t.Fatal(queryErr)
		}
		for rows.Next() {
			var cid, notnull, pk int
			var name, kind string
			var def any
			if queryErr = rows.Scan(&cid, &name, &kind, &notnull, &def, &pk); queryErr != nil {
				rows.Close()
				t.Fatal(queryErr)
			}
			columns = append(columns, name)
		}
		queryErr = rows.Err()
		rows.Close()
		if queryErr != nil {
			t.Fatal(queryErr)
		}
		columnList := strings.Join(columns, ",")
		var missing int
		if err = s.db.QueryRow("SELECT count(*) FROM (SELECT " + columnList + " FROM fixture_" + table + " EXCEPT SELECT " + columnList + " FROM " + table + " WHERE mode='test')").Scan(&missing); err != nil || missing != 0 {
			t.Fatalf("%s migrated rows missing=%d: %v", table, missing, err)
		}
		if err = s.db.QueryRow("SELECT count(*) FROM (SELECT " + columnList + " FROM " + table + " WHERE mode='test' EXCEPT SELECT " + columnList + " FROM fixture_" + table + ")").Scan(&missing); err != nil || missing != 0 {
			t.Fatalf("%s migrated rows added=%d: %v", table, missing, err)
		}
	}
	if _, err = s.BuyerMerchantOrder(t.Context(), second, order.ID); err != nil {
		t.Fatalf("recovery grant lost: %v", err)
	}
	if _, err = s.db.Exec("INSERT INTO merchant_order_recovery_codes(mode,order_id,code_hash,expires_at) VALUES('live',?,'cross-mode',9999999999)", order.ID); err == nil {
		t.Fatal("cross-mode order FK accepted")
	}
	if _, err = s.db.Exec("INSERT INTO merchant_buyer_sessions(mode,token_hash,expires_at) SELECT 'live',token_hash,expires_at FROM merchant_buyer_sessions WHERE mode='test'"); err != nil {
		t.Fatal("same token hash should be independent by mode", err)
	}
	if err = s.migrateMerchantModes(); err != nil {
		t.Fatal("repeat migration", err)
	}
	var version, foreignKeys int
	if err = s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 57 {
		t.Fatal(version, err)
	}
	if err = s.db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		t.Fatal(foreignKeys, err)
	}
}
