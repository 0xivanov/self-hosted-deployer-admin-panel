//go:build integration

package portal

import (
	"os"
	"strings"
	"testing"
)

// Restore the genuine pre-mode schema before tests rewind selected historical
// migrations. Keep sandbox fixture rows, including buyer recovery relationships.
func downgradeMerchantSchema55(t *testing.T, s *Store) {
	t.Helper()
	tables := []string{"merchant_accounts", "merchant_products", "merchant_orders", "merchant_events", "merchant_refunds", "merchant_buyer_sessions", "merchant_order_recovery_codes", "merchant_order_recovery_grants"}
	for _, table := range tables {
		rows, err := s.db.Query("PRAGMA table_info(" + table + ")")
		if err != nil {
			t.Fatal(err)
		}
		var columns []string
		for rows.Next() {
			var cid, notnull, pk int
			var name, kind string
			var def any
			if err = rows.Scan(&cid, &name, &kind, &notnull, &def, &pk); err != nil {
				t.Fatal(err)
			}
			if name != "mode" {
				columns = append(columns, name)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.db.Exec("CREATE TEMP TABLE fixture_" + table + " AS SELECT " + strings.Join(columns, ",") + " FROM " + table + " WHERE mode='test'"); err != nil {
			t.Fatal(err)
		}
	}
	for i := len(tables) - 1; i >= 0; i-- {
		if _, err := s.db.Exec("DROP TABLE " + tables[i]); err != nil {
			t.Fatal(err)
		}
	}
	ddl, err := os.ReadFile("testdata/merchant-schema-55.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(string(ddl)); err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		if _, err = s.db.Exec("INSERT INTO " + table + " SELECT * FROM fixture_" + table); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.db.Exec("PRAGMA user_version=55"); err != nil {
		t.Fatal(err)
	}
}
