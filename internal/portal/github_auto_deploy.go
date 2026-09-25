package portal

import (
	"context"
	"database/sql"
	"errors"
)

// SetGitHubAutoDeploy changes future push processing and fences queued work by
// advancing the connection revision. It does not alter the published website.
func (s *Store) SetGitHubAutoDeploy(ctx context.Context, session, project string, enabled bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, actor, err := s.projectClientOwner(ctx, tx, session, project)
	if err != nil {
		return err
	}
	var authorizer string
	var current bool
	err = tx.QueryRowContext(ctx, `SELECT c.actor_id,c.deploy_on_push FROM github_connections c
 JOIN users u ON u.id=c.actor_id AND u.verified=1 AND u.disabled=0
 JOIN memberships m ON m.user_id=u.id AND m.workspace_id=? AND m.role='owner'
 WHERE c.project_id=? AND c.connected=1`, p.WorkspaceID, project).Scan(&authorizer, &current)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDenied
	}
	if err != nil {
		return err
	}
	if enabled {
		if err = s.requireHostingKind(ctx, tx, p.WorkspaceID, p.Kind); err != nil {
			return err
		}
	}
	if current == enabled {
		return tx.Commit()
	}
	if _, err = tx.ExecContext(ctx, "UPDATE github_connections SET deploy_on_push=?,revision=revision+1,updated_at=? WHERE project_id=?", enabled, s.now().Unix(), project); err != nil {
		return err
	}
	if err = audit(ctx, tx, actor, p.WorkspaceID, "project.github-auto-deploy.changed:"+project, s.now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}
