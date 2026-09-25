package portal

import (
	"context"
	"errors"
)

// Rebuild the parent table without renaming the old one. Renaming it first
// would rewrite every child foreign key to point at the temporary old name.
// Foreign keys are disabled only on this pinned migration connection, outside
// the transaction, and every reference is checked before the new schema commits.
func (s *Store) migrateGitHubPushReplay() (result error) {
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
	if version >= 53 {
		return nil
	}
	if version != 52 {
		return errors.New("GitHub replay migration requires portal schema 52")
	}
	if _, err = conn.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		return err
	}
	defer func() {
		_, err := conn.ExecContext(ctx, "PRAGMA foreign_keys=ON")
		if err != nil {
			result = errors.Join(result, err)
		}
	}()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version >= 53 {
		return nil
	}
	if version != 52 {
		return errors.New("portal schema changed during GitHub replay migration")
	}
	_, err = tx.ExecContext(ctx, `CREATE TABLE github_push_events_next(
 id TEXT PRIMARY KEY,
 payload_hash TEXT NOT NULL REFERENCES github_push_receipts(payload_hash) ON DELETE CASCADE,
 project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
 connection_revision INTEGER NOT NULL,
 actor_id TEXT NOT NULL REFERENCES users(id),
 installation_id INTEGER NOT NULL,repository_id INTEGER NOT NULL,repository TEXT NOT NULL,
 ref TEXT NOT NULL,before_sha TEXT NOT NULL,after_sha TEXT NOT NULL,
 deleted INTEGER NOT NULL CHECK(deleted IN (0,1)),
 state TEXT NOT NULL CHECK(state='pending'),created_at INTEGER NOT NULL,
 UNIQUE(project_id,payload_hash));
 INSERT INTO github_push_events_next SELECT * FROM github_push_events;
 DROP TABLE github_push_events;
 ALTER TABLE github_push_events_next RENAME TO github_push_events;
 CREATE INDEX github_push_events_payload ON github_push_events(payload_hash);
 CREATE INDEX github_push_events_project ON github_push_events(project_id,created_at,id);
 PRAGMA user_version=53;`)
	if err != nil {
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
		return errors.New("GitHub replay migration would leave invalid references")
	}
	return tx.Commit()
}
