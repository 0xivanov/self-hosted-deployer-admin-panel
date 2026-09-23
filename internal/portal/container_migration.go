package portal

import (
	"context"
	"errors"
)

// Rebuild the parent table without renaming the old one. Renaming it first
// would rewrite every child foreign key to point at the temporary old name.
// Foreign keys are disabled only on this pinned migration connection, outside
// the transaction, and every reference is checked before the new schema commits.
func (s *Store) migrateContainers() (result error) {
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
	if version == 43 {
		return nil
	}
	if version != 42 {
		return errors.New("container migration requires portal schema 42")
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
	if version == 43 {
		return nil
	}
	if version != 42 {
		return errors.New("portal schema changed during container migration")
	}
	_, err = tx.ExecContext(ctx, `CREATE TABLE projects_next(id TEXT PRIMARY KEY,workspace_id TEXT NOT NULL REFERENCES workspaces(id),name TEXT NOT NULL,kind TEXT NOT NULL CHECK(kind IN ('static','node','container')),deletion_requested_at INTEGER NOT NULL DEFAULT 0,deletion_error TEXT NOT NULL DEFAULT '',UNIQUE(workspace_id,name));
 INSERT INTO projects_next SELECT id,workspace_id,name,kind,deletion_requested_at,deletion_error FROM projects;
 DROP TABLE projects;
 ALTER TABLE projects_next RENAME TO projects;
 CREATE TABLE IF NOT EXISTS container_releases(id TEXT PRIMARY KEY,project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,actor_id TEXT NOT NULL REFERENCES users(id),request_key TEXT NOT NULL,revision INTEGER NOT NULL CHECK(revision>0),input BLOB NOT NULL,image BLOB NOT NULL,created_at INTEGER NOT NULL,UNIQUE(project_id,request_key),UNIQUE(project_id,revision));
 PRAGMA user_version=43;`)
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
		return errors.New("container migration would leave invalid references")
	}
	return tx.Commit()
}
