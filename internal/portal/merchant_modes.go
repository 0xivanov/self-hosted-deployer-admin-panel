package portal

import (
	"context"
	"errors"
	"strings"
)

// merchantModeValue is independent of hosting billing; zero-value stores retain
// sandbox behavior for compatibility. Public openers validate explicit modes.
func (s *Store) merchantModeValue() string {
	if s.merchantMode == "live" {
		return "live"
	}
	return "test"
}

func (s *Store) migrateMerchantModes() (result error) {
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
	if version >= 56 {
		return nil
	}
	if version != 55 {
		return errors.New("merchant modes migration requires schema 55")
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
	if version != 55 {
		return errors.New("merchant schema changed during migration")
	}
	// Build all replacement parents and children before replacing any old table.
	// Refer to final names so SQLite cannot rewrite child references to old tables.
	tables := []struct{ name, columns, ddl string }{
		{"merchant_accounts", "workspace_id,request_id,actor_id,country,state,account_id,created_at,submitted_at,snapshot,observation_generation", `workspace_id TEXT NOT NULL REFERENCES workspaces(id),request_id TEXT NOT NULL,actor_id TEXT NOT NULL REFERENCES users(id),country TEXT NOT NULL,state TEXT NOT NULL CHECK(state IN ('requested','submitted','bound')),account_id TEXT,created_at INTEGER NOT NULL,submitted_at INTEGER NOT NULL DEFAULT 0,snapshot BLOB, observation_generation INTEGER NOT NULL DEFAULT 0,mode TEXT NOT NULL DEFAULT 'test' CHECK(mode IN ('test','live')),PRIMARY KEY(mode,workspace_id),UNIQUE(mode,request_id),UNIQUE(mode,account_id)`},
		{"merchant_products", "id,workspace_id,request_key,name,currency,amount_minor,active,revision,created_at,updated_at", `id TEXT NOT NULL,workspace_id TEXT NOT NULL REFERENCES workspaces(id),request_key TEXT NOT NULL,name TEXT NOT NULL,currency TEXT NOT NULL CHECK(currency IN ('eur','usd','gbp')),amount_minor INTEGER NOT NULL CHECK(amount_minor BETWEEN 50 AND 99999999),active INTEGER NOT NULL CHECK(active IN (0,1)),revision INTEGER NOT NULL CHECK(revision>0),created_at INTEGER NOT NULL,updated_at INTEGER NOT NULL,mode TEXT NOT NULL DEFAULT 'test' CHECK(mode IN ('test','live')),UNIQUE(mode,workspace_id,request_key),PRIMARY KEY(mode,id)`},
		{"merchant_orders", "id,workspace_id,product_id,product_revision,buyer_hash,request_key,account_id,name,currency,amount_minor,state,payment_status,session_id,checkout_url,payment_intent_id,created_at,submitted_at,observation_generation,observed_at,fulfilled_at,fulfilled_by", `id TEXT NOT NULL,workspace_id TEXT NOT NULL REFERENCES workspaces(id),product_id TEXT NOT NULL,product_revision INTEGER NOT NULL,buyer_hash TEXT NOT NULL,request_key TEXT NOT NULL,account_id TEXT NOT NULL,name TEXT NOT NULL,currency TEXT NOT NULL,amount_minor INTEGER NOT NULL,state TEXT NOT NULL CHECK(state IN ('requested','submitted','open','complete','expired')),payment_status TEXT NOT NULL DEFAULT 'unpaid' CHECK(payment_status IN ('unpaid','paid')),session_id TEXT,checkout_url TEXT NOT NULL DEFAULT '',payment_intent_id TEXT NOT NULL DEFAULT '',created_at INTEGER NOT NULL,submitted_at INTEGER NOT NULL DEFAULT 0,observation_generation INTEGER NOT NULL DEFAULT 0,observed_at INTEGER NOT NULL DEFAULT 0, fulfilled_at INTEGER NOT NULL DEFAULT 0, fulfilled_by TEXT NOT NULL DEFAULT '',mode TEXT NOT NULL DEFAULT 'test' CHECK(mode IN ('test','live')),UNIQUE(mode,buyer_hash,request_key),UNIQUE(mode,account_id,session_id),PRIMARY KEY(mode,id),FOREIGN KEY(mode,product_id) REFERENCES merchant_products(mode,id),FOREIGN KEY(mode,account_id) REFERENCES merchant_accounts(mode,account_id)`},
		{"merchant_events", "id,account_id,order_id,session_id,event_type,body_hash,state,created_at,received_at,next_attempt,refund_request_id,provider_refund_id", `id TEXT NOT NULL,account_id TEXT NOT NULL,order_id TEXT NOT NULL,session_id TEXT NOT NULL,event_type TEXT NOT NULL,body_hash TEXT NOT NULL,state TEXT NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','done')),created_at INTEGER NOT NULL,received_at INTEGER NOT NULL,next_attempt INTEGER NOT NULL DEFAULT 0, refund_request_id TEXT NOT NULL DEFAULT '', provider_refund_id TEXT NOT NULL DEFAULT '',mode TEXT NOT NULL DEFAULT 'test' CHECK(mode IN ('test','live')),PRIMARY KEY(mode,id),FOREIGN KEY(mode,order_id) REFERENCES merchant_orders(mode,id)`},
		{"merchant_refunds", "id,order_id,workspace_id,actor_id,account_id,payment_intent_id,currency,amount_minor,state,provider_id,created_at,submitted_at,observed_at,generation", `id TEXT NOT NULL,order_id TEXT NOT NULL,workspace_id TEXT NOT NULL REFERENCES workspaces(id),actor_id TEXT NOT NULL,account_id TEXT NOT NULL,payment_intent_id TEXT NOT NULL,currency TEXT NOT NULL,amount_minor INTEGER NOT NULL,state TEXT NOT NULL CHECK(state IN ('requested','submitted','pending','requires_action','succeeded','failed','canceled')),provider_id TEXT,created_at INTEGER NOT NULL,submitted_at INTEGER NOT NULL DEFAULT 0,observed_at INTEGER NOT NULL DEFAULT 0,generation INTEGER NOT NULL DEFAULT 0,mode TEXT NOT NULL DEFAULT 'test' CHECK(mode IN ('test','live')),UNIQUE(mode,account_id,provider_id),PRIMARY KEY(mode,id),UNIQUE(mode,order_id),FOREIGN KEY(mode,order_id) REFERENCES merchant_orders(mode,id)`},
		{"merchant_buyer_sessions", "token_hash,expires_at", `token_hash TEXT NOT NULL,expires_at INTEGER NOT NULL,mode TEXT NOT NULL DEFAULT 'test' CHECK(mode IN ('test','live')),PRIMARY KEY(mode,token_hash)`},
		{"merchant_order_recovery_codes", "order_id,code_hash,expires_at", `order_id TEXT NOT NULL,code_hash TEXT NOT NULL,expires_at INTEGER NOT NULL,mode TEXT NOT NULL DEFAULT 'test' CHECK(mode IN ('test','live')),PRIMARY KEY(mode,order_id),UNIQUE(mode,code_hash),FOREIGN KEY(mode,order_id) REFERENCES merchant_orders(mode,id) ON DELETE CASCADE`},
		{"merchant_order_recovery_grants", "order_id,session_hash,created_at", `order_id TEXT NOT NULL,session_hash TEXT NOT NULL,created_at INTEGER NOT NULL,mode TEXT NOT NULL DEFAULT 'test' CHECK(mode IN ('test','live')),PRIMARY KEY(mode,order_id,session_hash),FOREIGN KEY(mode,order_id) REFERENCES merchant_orders(mode,id) ON DELETE CASCADE,FOREIGN KEY(mode,session_hash) REFERENCES merchant_buyer_sessions(mode,token_hash) ON DELETE CASCADE`},
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
		"CREATE INDEX merchant_products_workspace ON merchant_products(mode,workspace_id,id)",
		"CREATE INDEX merchant_orders_workspace ON merchant_orders(mode,workspace_id,created_at,id)",
		"CREATE INDEX merchant_orders_buyer ON merchant_orders(mode,buyer_hash,created_at)",
		"CREATE INDEX merchant_events_due ON merchant_events(mode,state,next_attempt,id)",
		"CREATE INDEX merchant_refunds_workspace ON merchant_refunds(mode,workspace_id,created_at)",
		"CREATE INDEX merchant_buyer_sessions_expiry ON merchant_buyer_sessions(mode,expires_at)",
		"CREATE INDEX merchant_order_recovery_grants_session ON merchant_order_recovery_grants(mode,session_hash,order_id)",
	}
	if _, err = tx.ExecContext(ctx, strings.Join(indexes, ";")+"; PRAGMA user_version=56"); err != nil {
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
		return errors.New("merchant migration would leave invalid references")
	}
	return tx.Commit()
}
