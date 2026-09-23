//go:build integration

package portal

import "testing"

func TestContainerMigrationRejectsOrphansAndRestoresForeignKeys(t *testing.T) {
	s, _ := newStore(t)
	// Simulate a damaged schema-42 database. A migration must neither bless it
	// nor leave foreign-key enforcement disabled on its pooled connection.
	if _, err := s.db.Exec(`PRAGMA foreign_keys=OFF;
 INSERT INTO projects(id,workspace_id,name,kind) VALUES('orphan','missing','orphan','static');
 PRAGMA user_version=42; PRAGMA foreign_keys=ON;`); err != nil {
		t.Fatal(err)
	}
	if err := s.migrateContainers(); err == nil {
		t.Fatal("accepted orphaned project")
	}
	var version, enabled, count int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("PRAGMA foreign_keys").Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("SELECT count(*) FROM projects WHERE id='orphan'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if version != 42 || enabled != 1 || count != 1 {
		t.Fatalf("failed migration changed state: version=%d fk=%d rows=%d", version, enabled, count)
	}
	if _, err := s.db.Exec("INSERT INTO projects(id,workspace_id,name,kind) VALUES('another','missing','another','static')"); err == nil {
		t.Fatal("foreign keys not enforced")
	}
}
