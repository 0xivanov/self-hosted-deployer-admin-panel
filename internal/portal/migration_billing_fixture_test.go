//go:build integration

package portal

import (
	"os"
	"testing"
)

// downgradeBillingSchema54 replaces the mode-scoped billing tables with their
// pre-mode schema while retaining the sandbox rows. Historical migration tests
// start from the current schema, so recreating only hosting_plan_limits leaves
// its foreign key incompatible with the legacy billing_plans primary key.
func downgradeBillingSchema54(t *testing.T, s *Store) {
	t.Helper()
	const backup = `
CREATE TEMP TABLE fixture_billing_events AS SELECT id,event_type,provider_created,fingerprint,payload,state,received_at FROM billing_events WHERE mode='test';
CREATE TEMP TABLE fixture_billing_customers AS SELECT workspace_id,request_id,actor_id,email,customer_id,created_at FROM billing_customers WHERE mode='test';
CREATE TEMP TABLE fixture_billing_plans AS SELECT id,price_id,enabled,price_snapshot,price_observed,next_refresh,price_generation FROM billing_plans WHERE mode='test';
CREATE TEMP TABLE fixture_billing_checkouts AS SELECT id,workspace_id,actor_id,customer_id,plan_id,price_id,session_id,checkout_url,state,created_at,hosting_limits FROM billing_checkouts WHERE mode='test';
CREATE TEMP TABLE fixture_billing_subscriptions AS SELECT id,checkout_id,workspace_id,customer_id,plan_id,price_id,state,first_event,reconciliation_generation,snapshot FROM billing_subscriptions WHERE mode='test';
CREATE TEMP TABLE fixture_billing_work AS SELECT kind,reference,next_attempt,attempts,lease_hash,lease_until,done FROM billing_work WHERE mode='test';
CREATE TEMP TABLE fixture_billing_charges AS SELECT id,generation,subscription_id,customer_id,invoice_id,payment_intent_id,snapshot,next_refresh FROM billing_charges WHERE mode='test';
CREATE TEMP TABLE fixture_hosting_plan_limits AS SELECT plan_id,projects,uploads,upload_bytes,node FROM hosting_plan_limits WHERE mode='test';`
	if _, err := s.db.Exec(backup); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"hosting_plan_limits", "billing_charges", "billing_work", "billing_subscriptions", "billing_checkouts", "billing_customers", "billing_plans", "billing_events"} {
		if _, err := s.db.Exec("DROP TABLE " + table); err != nil {
			t.Fatal(err)
		}
	}
	ddl, err := os.ReadFile("testdata/billing-schema-54.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(string(ddl)); err != nil {
		t.Fatal(err)
	}
	// Restore parents before children so foreign keys remain valid.
	if _, err = s.db.Exec(`
INSERT INTO billing_events SELECT * FROM fixture_billing_events;
INSERT INTO billing_plans SELECT * FROM fixture_billing_plans;
INSERT INTO billing_customers SELECT * FROM fixture_billing_customers;
INSERT INTO billing_checkouts SELECT * FROM fixture_billing_checkouts;
INSERT INTO billing_subscriptions SELECT * FROM fixture_billing_subscriptions;
INSERT INTO billing_charges SELECT * FROM fixture_billing_charges;

INSERT INTO billing_work SELECT * FROM fixture_billing_work;
INSERT INTO hosting_plan_limits SELECT * FROM fixture_hosting_plan_limits;`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("PRAGMA user_version=54"); err != nil {
		t.Fatal(err)
	}
}
