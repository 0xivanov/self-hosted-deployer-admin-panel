package portal

import (
	"context"
	"errors"
	"strings"
)

// billingModeValue is operator-owned state. No request may choose its mode.
// The mode is fixed when the store opens, before it serves requests.
func (s *Store) billingModeValue() string {
	if s.billingMode == "live" {
		return "live"
	}
	return "test"
}

func (s *Store) migrateBillingModes() (result error) {
	ctx := context.Background()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var version int
	if err = conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version >= 55 {
		return nil
	}
	if version != 54 {
		return errors.New("billing modes migration requires schema 54")
	}
	if _, err = conn.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		return err
	}
	defer func() { _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys=ON"); result = errors.Join(result, err) }()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version != 54 {
		return errors.New("billing schema changed during migration")
	}
	// Build all replacement parents and children before replacing any old table.
	// Refer to final names so SQLite cannot rewrite child references to old tables.
	mode := ",mode TEXT NOT NULL DEFAULT 'test' CHECK(mode IN ('test','live'))"
	tables := []struct{ name, columns, ddl string }{
		{"billing_events", "id,event_type,provider_created,fingerprint,payload,state,received_at", `id TEXT NOT NULL,event_type TEXT NOT NULL,provider_created INTEGER NOT NULL,fingerprint TEXT NOT NULL,payload BLOB NOT NULL,state TEXT NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','processed','ignored')),received_at INTEGER NOT NULL` + mode + `,PRIMARY KEY(mode,id)`},
		{"billing_customers", "workspace_id,request_id,actor_id,email,customer_id,created_at", `workspace_id TEXT NOT NULL REFERENCES workspaces(id),request_id TEXT NOT NULL,actor_id TEXT NOT NULL REFERENCES users(id),email TEXT NOT NULL,customer_id TEXT,created_at INTEGER NOT NULL` + mode + `,PRIMARY KEY(mode,workspace_id),UNIQUE(mode,request_id),UNIQUE(mode,customer_id)`},
		{"billing_plans", "id,price_id,enabled,price_snapshot,price_observed,next_refresh,price_generation", `id TEXT NOT NULL,price_id TEXT NOT NULL,enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),price_snapshot BLOB,price_observed INTEGER NOT NULL DEFAULT 0,next_refresh INTEGER NOT NULL DEFAULT 0,price_generation INTEGER NOT NULL DEFAULT 0` + mode + `,PRIMARY KEY(mode,id)`},
		{"billing_checkouts", "id,workspace_id,actor_id,customer_id,plan_id,price_id,session_id,checkout_url,state,created_at,hosting_limits", `id TEXT NOT NULL,workspace_id TEXT NOT NULL REFERENCES workspaces(id),actor_id TEXT NOT NULL REFERENCES users(id),customer_id TEXT NOT NULL,plan_id TEXT NOT NULL,price_id TEXT NOT NULL,session_id TEXT,checkout_url TEXT NOT NULL DEFAULT '',state TEXT NOT NULL CHECK(state IN ('pending','open','completed','expired')),created_at INTEGER NOT NULL,hosting_limits BLOB` + mode + `,PRIMARY KEY(mode,id),UNIQUE(mode,session_id),FOREIGN KEY(mode,customer_id) REFERENCES billing_customers(mode,customer_id),FOREIGN KEY(mode,plan_id) REFERENCES billing_plans(mode,id)`},
		{"billing_subscriptions", "id,checkout_id,workspace_id,customer_id,plan_id,price_id,state,first_event,reconciliation_generation,snapshot", `id TEXT NOT NULL,checkout_id TEXT NOT NULL,workspace_id TEXT NOT NULL REFERENCES workspaces(id),customer_id TEXT NOT NULL,plan_id TEXT NOT NULL,price_id TEXT NOT NULL,state TEXT NOT NULL DEFAULT 'awaiting_reconciliation',first_event TEXT NOT NULL,reconciliation_generation INTEGER NOT NULL DEFAULT 0,snapshot BLOB` + mode + `,PRIMARY KEY(mode,id),UNIQUE(mode,checkout_id),FOREIGN KEY(mode,checkout_id) REFERENCES billing_checkouts(mode,id),FOREIGN KEY(mode,customer_id) REFERENCES billing_customers(mode,customer_id),FOREIGN KEY(mode,plan_id) REFERENCES billing_plans(mode,id),FOREIGN KEY(mode,first_event) REFERENCES billing_events(mode,id)`},
		{"billing_work", "kind,reference,next_attempt,attempts,lease_hash,lease_until,done", `kind TEXT NOT NULL CHECK(kind IN ('customer','checkout','event','subscription')),reference TEXT NOT NULL,next_attempt INTEGER NOT NULL DEFAULT 0,attempts INTEGER NOT NULL DEFAULT 0,lease_hash TEXT NOT NULL DEFAULT '',lease_until INTEGER NOT NULL DEFAULT 0,done INTEGER NOT NULL DEFAULT 0 CHECK(done IN (0,1))` + mode + `,PRIMARY KEY(mode,kind,reference)`},
		{"billing_charges", "id,generation,subscription_id,customer_id,invoice_id,payment_intent_id,snapshot,next_refresh", `id TEXT NOT NULL,generation INTEGER NOT NULL DEFAULT 0,subscription_id TEXT,customer_id TEXT,invoice_id TEXT NOT NULL DEFAULT '',payment_intent_id TEXT NOT NULL DEFAULT '',snapshot BLOB,next_refresh INTEGER NOT NULL DEFAULT 0` + mode + `,PRIMARY KEY(mode,id),FOREIGN KEY(mode,subscription_id) REFERENCES billing_subscriptions(mode,id),FOREIGN KEY(mode,customer_id) REFERENCES billing_customers(mode,customer_id)`},
		{"hosting_plan_limits", "plan_id,projects,uploads,upload_bytes,node", `plan_id TEXT NOT NULL,projects INTEGER NOT NULL CHECK(projects BETWEEN 1 AND 100),uploads INTEGER NOT NULL CHECK(uploads BETWEEN 1 AND 20),upload_bytes INTEGER NOT NULL CHECK(upload_bytes BETWEEN 1 AND 104857600),node INTEGER NOT NULL CHECK(node IN (0,1))` + mode + `,PRIMARY KEY(mode,plan_id),FOREIGN KEY(mode,plan_id) REFERENCES billing_plans(mode,id)`},
	}
	for _, table := range tables {
		if _, err = tx.ExecContext(ctx, "CREATE TABLE "+table.name+"_mode_next("+table.ddl+")"); err != nil {
			return err
		}
		// Old rows are unambiguously sandbox data: previous binaries reject live keys/events.
		if _, err = tx.ExecContext(ctx, "INSERT INTO "+table.name+"_mode_next("+table.columns+",mode) SELECT "+table.columns+",'test' FROM "+table.name); err != nil {
			return err
		}
	}
	for _, table := range tables {
		if _, err = tx.ExecContext(ctx, "DROP TABLE "+table.name); err != nil {
			return err
		}
	}
	for _, table := range tables {
		if _, err = tx.ExecContext(ctx, "ALTER TABLE "+table.name+"_mode_next RENAME TO "+table.name); err != nil {
			return err
		}
	}
	indexes := []string{
		"CREATE INDEX billing_events_pending ON billing_events(mode,state,received_at)",
		"CREATE UNIQUE INDEX billing_checkout_active ON billing_checkouts(mode,workspace_id) WHERE state IN ('pending','open','completed')",
		"CREATE INDEX billing_work_due ON billing_work(mode,done,next_attempt,lease_until)",
		"CREATE INDEX billing_charges_subscription ON billing_charges(mode,subscription_id)",
		"CREATE INDEX billing_charges_refresh ON billing_charges(mode,next_refresh)",
	}
	if _, err = tx.ExecContext(ctx, strings.Join(indexes, ";")+"; PRAGMA user_version=55"); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	invalid := rows.Next()
	readErr := rows.Err()
	rows.Close()
	if readErr != nil {
		return readErr
	}
	if invalid {
		return errors.New("billing migration would leave invalid references")
	}
	return tx.Commit()
}
