package portal

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// RenameProject changes only the display name. Runtime identities and domains
// remain tied to the immutable project ID.
func (s *Store) RenameProject(ctx context.Context, token, project, name string) (Project, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 100 {
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
	if p.Name == name {
		return p, tx.Commit()
	}
	var existing string
	err = tx.QueryRowContext(ctx, "SELECT id FROM projects WHERE workspace_id=? AND name=? AND id<>?", p.WorkspaceID, name, project).Scan(&existing)
	if err == nil {
		return Project{}, ErrExists
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Project{}, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE projects SET name=? WHERE id=?", name, project); err != nil {
		return Project{}, err
	}
	if err = audit(ctx, tx, actor, p.WorkspaceID, "project.renamed:"+project, s.now().Unix()); err != nil {
		return Project{}, err
	}
	p.Name = name
	return p, tx.Commit()
}
