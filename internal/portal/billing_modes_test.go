//go:build integration

package portal

import (
	"errors"
	"os"
	"testing"
	"time"
)

func TestBillingModesDoNotShareCustomerOrCheckout(t *testing.T) {
	s, _, owner, session := billingCheckoutFixture(t)
	sandbox, err := s.BillingCustomer(t.Context(), session.Token, owner.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	testCheckout, err := s.RequestBillingCheckout(t.Context(), session.Token, owner.WorkspaceID, "starter")
	if err != nil {
		t.Fatal(err)
	}
	s.billingMode = "live"
	if _, err = s.BillingCustomer(t.Context(), session.Token, owner.WorkspaceID); !errors.Is(err, ErrDenied) {
		t.Fatalf("live saw sandbox customer: %v", err)
	}
	liveCustomer, err := s.RequestBillingCustomer(t.Context(), session.Token, owner.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if liveCustomer.RequestID == sandbox.RequestID || liveCustomer.CustomerID != "" {
		t.Fatal("shared customer binding")
	}
	plans, err := s.AvailableBillingPlans(t.Context(), session.Token, owner.WorkspaceID)
	if err != nil || len(plans) != 0 {
		t.Fatalf("sandbox plans leaked: %+v %v", plans, err)
	}
	if _, err = s.BillingCheckout(t.Context(), session.Token, owner.WorkspaceID, testCheckout.ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("sandbox checkout leaked: %v", err)
	}
}

func TestLiveHostingIgnoresPaidSandboxEvidence(t *testing.T) {
	s, _, owner, session := billingCheckoutFixture(t)
	now := time.Unix(1700000000, 0)
	s.now = func() time.Time { return now }
	if err := s.ConfigureHostingPolicy(t.Context(), owner.WorkspaceID, true); err != nil {
		t.Fatal(err)
	}
	checkout, err := s.RequestBillingCheckout(t.Context(), session.Token, owner.WorkspaceID, "starter")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BindBillingCheckout(t.Context(), checkout.ID, "cs_test_hosting", "https://checkout.stripe.com/c/pay/cs_test_hosting"); err != nil {
		t.Fatal(err)
	}
	checkoutEvent(t, s, "evt_hosting", "checkout.session.completed", "cs_test_hosting", checkout.CustomerID, checkout.ID, "sub_hosting")
	if _, err = s.ProcessBillingCheckoutEvent(t.Context(), "evt_hosting"); err != nil {
		t.Fatal(err)
	}
	putHostingEvidence(t, s, checkout.CustomerID, now)
	access, err := s.WorkspaceHostingAccess(t.Context(), session.Token, owner.WorkspaceID)
	if err != nil || !access.Allowed {
		t.Fatalf("sandbox baseline: %+v %v", access, err)
	}
	s.billingMode = "live"
	access, err = s.WorkspaceHostingAccess(t.Context(), session.Token, owner.WorkspaceID)
	if err != nil || access.Allowed || access.Mode != "live_subscription" {
		t.Fatalf("live entitlement from test evidence: %+v %v", access, err)
	}
	s.billingMode = ""
	access, err = s.WorkspaceHostingAccess(t.Context(), session.Token, owner.WorkspaceID)
	if err != nil || !access.Allowed {
		t.Fatalf("sandbox history changed: %+v %v", access, err)
	}
}

func TestBillingModesMigrationPreservesLegacyAndScopesForeignKeys(t *testing.T) {
	s, _ := newStore(t)
	owner, _ := verifiedAccount(t, s, "billing-mode-migration@example.test")
	for _, table := range []string{"hosting_plan_limits", "billing_charges", "billing_work", "billing_subscriptions", "billing_checkouts", "billing_customers", "billing_plans", "billing_events"} {
		if _, err := s.db.Exec("DROP TABLE " + table); err != nil {
			t.Fatal(err)
		}
	}
	ddl, err := os.ReadFile("testdata/billing-schema-54.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(string(ddl) + "PRAGMA user_version=54"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("INSERT INTO billing_events(id,event_type,provider_created,fingerprint,payload,received_at) VALUES('evt_old','invoice.paid',1,'fingerprint','{}',2); INSERT INTO billing_plans(id,price_id,enabled) VALUES('starter','price_old',1)"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("INSERT INTO billing_customers VALUES(?,?,?,?,?,?)", owner.WorkspaceID, "request-old", owner.ID, owner.Email, "cus_old", 3); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("INSERT INTO billing_checkouts(id,workspace_id,actor_id,customer_id,plan_id,price_id,state,created_at) VALUES('checkout-old',?,?,'cus_old','starter','price_old','completed',4)", owner.WorkspaceID, owner.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("INSERT INTO billing_subscriptions(id,checkout_id,workspace_id,customer_id,plan_id,price_id,first_event) VALUES('sub_old','checkout-old',?,'cus_old','starter','price_old','evt_old')", owner.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("INSERT INTO billing_charges(id,subscription_id,customer_id) VALUES('ch_old','sub_old','cus_old'); INSERT INTO billing_work(kind,reference,attempts) VALUES('subscription','sub_old',3); INSERT INTO hosting_plan_limits VALUES('starter',5,5,1024,1)"); err != nil {
		t.Fatal(err)
	}
	if err = s.migrateBillingModes(); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"hosting_plan_limits", "billing_charges", "billing_work", "billing_subscriptions", "billing_checkouts", "billing_customers", "billing_plans", "billing_events"} {
		var n int
		if err = s.db.QueryRow("SELECT count(*) FROM " + table + " WHERE mode='test'").Scan(&n); err != nil || n != 1 {
			t.Fatalf("%s: %d %v", table, n, err)
		}
	}
	// Live children cannot bind to otherwise matching sandbox parents.
	if _, err = s.db.Exec("INSERT INTO billing_charges(id,subscription_id,customer_id,mode) VALUES('ch_bad','sub_old','cus_old','live')"); err == nil {
		t.Fatal("cross-mode foreign key accepted")
	}
	if _, err = s.db.Exec("INSERT INTO billing_events SELECT id,event_type,provider_created,fingerprint,payload,state,received_at,'live' FROM billing_events WHERE mode='test'"); err != nil {
		t.Fatalf("same provider event ID across modes: %v", err)
	}
	if _, err = s.db.Exec("INSERT INTO billing_customers SELECT workspace_id,request_id,actor_id,email,customer_id,created_at,'live' FROM billing_customers WHERE mode='test'"); err != nil {
		t.Fatalf("same workspace and customer ID across modes: %v", err)
	}
	if _, err = s.db.Exec("INSERT INTO billing_work(kind,reference,mode) VALUES('subscription','sub_old','live')"); err != nil {
		t.Fatalf("same work identity across modes: %v", err)
	}
	var foreignKeys int
	if err = s.db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		t.Fatalf("FK enforcement: %d %v", foreignKeys, err)
	}
	if err = s.migrateBillingModes(); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
}
