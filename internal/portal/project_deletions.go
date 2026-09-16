package portal

import (
	"context"
	"database/sql"
	"errors"
)

var (
	ErrProjectDeleting     = errors.New("project deletion is already requested")
	ErrProjectBusy         = errors.New("project has an operation in flight")
	ErrProjectNameMismatch = errors.New("project name confirmation does not match")
)

// DeleteProject records a deletion request. External artifacts are removed by
// the worker after this transaction commits.
func (s *Store) DeleteProject(ctx context.Context, token, project, name string) (Project, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Project{}, err
	}
	defer tx.Rollback()
	var p Project
	var requestedAt int64
	err = tx.QueryRowContext(ctx, "SELECT id,workspace_id,name,kind,deletion_requested_at,deletion_error FROM projects WHERE id=?", project).Scan(&p.ID, &p.WorkspaceID, &p.Name, &p.Kind, &requestedAt, &p.DeletionError)
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, ErrDenied
	}
	if err != nil {
		return Project{}, err
	}
	p.Deleting = requestedAt != 0
	actor, err := s.authorizeOwner(ctx, tx, token, p.WorkspaceID)
	if err != nil {
		return Project{}, err
	}
	if name != p.Name {
		return Project{}, ErrProjectNameMismatch
	}
	if p.Deleting {
		return p, tx.Commit()
	}
	var busy int
	err = tx.QueryRowContext(ctx, `SELECT
	 (SELECT count(*) FROM publication_jobs WHERE project_id=? AND state IN ('queued','running'))+
	 (SELECT count(*) FROM node_builds WHERE project_id=? AND state IN ('queued','running'))+
	 (SELECT count(*) FROM node_deployments WHERE project_id=? AND state IN ('queued','running'))`, project, project, project).Scan(&busy)
	if err != nil {
		return Project{}, err
	}
	if busy != 0 {
		return Project{}, ErrProjectBusy
	}
	requestedAt = s.now().Unix()
	if requestedAt == 0 {
		requestedAt = 1
	}
	if _, err = tx.ExecContext(ctx, "UPDATE projects SET deletion_requested_at=?,deletion_error='' WHERE id=? AND deletion_requested_at=0", requestedAt, project); err != nil {
		return Project{}, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE project_domains SET state='removing',message=? WHERE project_id=?", "Project deletion pending", project); err != nil {
		return Project{}, err
	}
	if err = audit(ctx, tx, actor, p.WorkspaceID, "project.deletion_requested:"+project, requestedAt); err != nil {
		return Project{}, err
	}
	p.Deleting = true
	p.DeletionError = ""
	return p, tx.Commit()
}
