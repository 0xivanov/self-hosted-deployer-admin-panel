package portal

import (
	"context"
	"database/sql"
	"errors"
)

var ErrClientUnavailable = errors.New("client account unavailable or has workspace access")
var ErrClientLimit = errors.New("maximum of 20 clients per website reached")

type ProjectClient struct {
	Email     string `json:"email"`
	CreatedAt int64  `json:"created_at"`
}
type SharedWebsite struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Published bool   `json:"published"`
	Domain    string `json:"domain,omitempty"`
}

func (s *Store) projectClientOwner(ctx context.Context, tx *sql.Tx, token, project string) (Project, string, error) {
	p, _, err := s.uploadProject(ctx, tx, token, project, true)
	if err != nil {
		return p, "", err
	}
	actor, err := s.authorizeOwner(ctx, tx, token, p.WorkspaceID)
	return p, actor, err
}
func (s *Store) ProjectClients(ctx context.Context, token, project string) ([]ProjectClient, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, _, err = s.projectClientOwner(ctx, tx, token, project); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT u.email,c.created_at FROM project_clients c JOIN users u ON u.id=c.user_id WHERE c.project_id=? ORDER BY u.email", project)
	if err != nil {
		return nil, err
	}
	clients := []ProjectClient{}
	for rows.Next() {
		var c ProjectClient
		if err = rows.Scan(&c.Email, &c.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		clients = append(clients, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return clients, tx.Commit()
}

// Client grants do not create workspace membership. Existing workspace roles
// remain authoritative and cannot be restricted by adding a client grant.
func (s *Store) ChangeProjectClient(ctx context.Context, token, project, email string, grant bool) error {
	email, err := normalizeEmail(email)
	if err != nil {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, actor, err := s.projectClientOwner(ctx, tx, token, project)
	if err != nil {
		return err
	}
	var user string
	query := "SELECT id FROM users WHERE email=?"
	if grant {
		query += " AND verified=1 AND disabled=0"
	}
	err = tx.QueryRowContext(ctx, query, email).Scan(&user)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrClientUnavailable
	}
	if err != nil {
		return err
	}
	action := "project.client-revoked:"
	if grant {
		var memberships, count, existing int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM memberships WHERE user_id=? AND workspace_id=?", user, p.WorkspaceID).Scan(&memberships); err != nil {
			return err
		}
		if memberships > 0 {
			return ErrClientUnavailable
		}
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM project_clients WHERE project_id=?", project).Scan(&count); err != nil {
			return err
		}
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM project_clients WHERE project_id=? AND user_id=?", project, user).Scan(&existing); err != nil {
			return err
		}
		if existing > 0 {
			return tx.Commit()
		}
		if count >= 20 {
			return ErrClientLimit
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO project_clients VALUES(?,?,?) ON CONFLICT(project_id,user_id) DO NOTHING", project, user, s.now().Unix())
		action = "project.client-granted:"
	} else {
		_, err = tx.ExecContext(ctx, "DELETE FROM project_clients WHERE project_id=? AND user_id=?", project, user)
	}
	if err != nil {
		return err
	}
	if err = audit(ctx, tx, actor, p.WorkspaceID, action+project+":"+user, s.now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

// SharedWebsites exposes only a review summary, not project APIs, source files,
// DNS verification tokens, application logs, membership or billing information.
func (s *Store) SharedWebsites(ctx context.Context, token string) ([]SharedWebsite, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var user string
	err = tx.QueryRowContext(ctx, `SELECT u.id FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=? AND s.expires_at>? AND u.verified=1 AND u.disabled=0`, digest(token), s.now().Unix()).Scan(&user)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrDenied
	}
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT p.id,p.name,p.kind,
 EXISTS(SELECT 1 FROM publications WHERE project_id=p.id) OR EXISTS(SELECT 1 FROM node_active_deployments WHERE project_id=p.id),
 COALESCE((SELECT hostname FROM project_domains WHERE project_id=p.id AND state='active' ORDER BY hostname LIMIT 1),'')
 FROM project_clients c JOIN projects p ON p.id=c.project_id WHERE c.user_id=? AND p.deletion_requested_at=0 ORDER BY p.name,p.id`, user)
	if err != nil {
		return nil, err
	}
	out := []SharedWebsite{}
	for rows.Next() {
		var p SharedWebsite
		if err = rows.Scan(&p.ID, &p.Name, &p.Kind, &p.Published, &p.Domain); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return out, tx.Commit()
}
