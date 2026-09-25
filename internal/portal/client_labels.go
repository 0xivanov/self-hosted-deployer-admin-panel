package portal

import (
	"context"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

func (s *Store) migrateClientLabels() error {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err = tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version >= 54 {
		return nil
	}
	if version != 53 {
		return errors.New("client labels migration requires portal schema 53")
	}
	if _, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS project_client_labels(project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,label TEXT NOT NULL); PRAGMA user_version=54;`); err != nil {
		return err
	}
	return tx.Commit()
}

// SetProjectClientLabel stores workspace-only portfolio metadata. It never
// changes project identity, runtime configuration, billing or client access.
func (s *Store) SetProjectClientLabel(ctx context.Context, token, project, label string) (Project, error) {
	label = strings.TrimSpace(label)
	if !utf8.ValidString(label) || utf8.RuneCountInString(label) > 100 || strings.ContainsFunc(label, unicode.IsControl) {
		return Project{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Project{}, err
	}
	defer tx.Rollback()
	p, actor, err := s.uploadProject(ctx, tx, token, project, true)
	if err != nil {
		return Project{}, err
	}
	if p.ClientLabel == label {
		return p, tx.Commit()
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO project_client_labels(project_id,label) VALUES(?,?) ON CONFLICT(project_id) DO UPDATE SET label=excluded.label", project, label); err != nil {
		return Project{}, err
	}
	if err = audit(ctx, tx, actor, p.WorkspaceID, "project.client-label-updated:"+project, s.now().Unix()); err != nil {
		return Project{}, err
	}
	p.ClientLabel = label
	return p, tx.Commit()
}
