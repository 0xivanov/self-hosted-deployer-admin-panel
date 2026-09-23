package portal

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var ErrClientInviteRate = errors.New("client invitation limit reached")
var ErrClientSignupApproval = errors.New("client registration requires operator approval")

type ClientInvitation struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	ExpiresAt int64  `json:"expires_at"`
}

// This predicate is used both before mail delivery and before acceptance. A
// queued invitation cannot outlive its owner's authority or a deleted project.
const liveClientInvitation = ` FROM client_invitations i
 JOIN projects p ON p.id=i.project_id
 JOIN users u ON u.id=i.inviter_id
 JOIN memberships m ON m.user_id=u.id AND m.workspace_id=p.workspace_id
 WHERE i.token_hash=? AND i.state='pending' AND i.expires_at>?
 AND p.deletion_requested_at=0 AND u.disabled=0 AND u.verified=1 AND m.role='owner'`

// InviteClient queues an email only. Acceptance requires a verified matching
// account and never adds workspace membership or bypasses the signup policy.
func (m *AccountMail) InviteClient(ctx context.Context, session, project, email string, signupAllowed func(string) bool) (ClientInvitation, error) {
	email, err := normalizeEmail(email)
	if err != nil {
		return ClientInvitation{}, ErrInvalid
	}
	tx, err := m.store.db.BeginTx(ctx, nil)
	if err != nil {
		return ClientInvitation{}, err
	}
	defer tx.Rollback()
	p, actor, err := m.store.projectClientOwner(ctx, tx, session, project)
	if err != nil {
		return ClientInvitation{}, err
	}
	var existing, verified int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM users u WHERE u.email=? AND (EXISTS(SELECT 1 FROM memberships WHERE user_id=u.id AND workspace_id=?) OR EXISTS(SELECT 1 FROM project_clients WHERE user_id=u.id AND project_id=?))`, email, p.WorkspaceID, project).Scan(&existing)
	if err != nil {
		return ClientInvitation{}, err
	}
	if existing > 0 {
		return ClientInvitation{}, ErrExists
	}
	err = tx.QueryRowContext(ctx, "SELECT count(*) FROM users WHERE email=? AND verified=1 AND disabled=0", email).Scan(&verified)
	if err != nil {
		return ClientInvitation{}, err
	}
	if verified == 0 && (signupAllowed == nil || !signupAllowed(email)) {
		return ClientInvitation{}, ErrClientSignupApproval
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM project_clients WHERE project_id=?", project).Scan(&count); err != nil {
		return ClientInvitation{}, err
	}
	if count >= 20 {
		return ClientInvitation{}, ErrClientLimit
	}
	now := m.store.now().Unix()
	var recent, daily, pending int
	err = tx.QueryRowContext(ctx, `SELECT
 COALESCE(sum(CASE WHEN email=? AND created_at>? THEN 1 ELSE 0 END),0),
 COALESCE(sum(CASE WHEN created_at>? THEN 1 ELSE 0 END),0),
 COALESCE(sum(CASE WHEN email<>? AND state='pending' AND expires_at>? THEN 1 ELSE 0 END),0)
 FROM client_invitations WHERE project_id=?`, email, now-60, now-86400, email, now, project).Scan(&recent, &daily, &pending)
	if err != nil {
		return ClientInvitation{}, err
	}
	if recent > 0 || daily >= 50 || pending >= 20 {
		return ClientInvitation{}, ErrClientInviteRate
	}
	if _, err = tx.ExecContext(ctx, "UPDATE client_invitations SET state='revoked' WHERE project_id=? AND email=? AND state='pending'", project, email); err != nil {
		return ClientInvitation{}, err
	}
	invite := ClientInvitation{ID: randomToken(), Email: email, ExpiresAt: m.store.now().Add(7 * 24 * time.Hour).Unix()}
	token := randomToken()
	if _, err = tx.ExecContext(ctx, `INSERT INTO client_invitations(id,token_hash,project_id,email,inviter_id,expires_at,state,created_at) VALUES(?,?,?,?,?,?,'pending',?)`, invite.ID, digest(token), project, email, actor, invite.ExpiresAt, now); err != nil {
		return ClientInvitation{}, err
	}
	if err = m.enqueue(ctx, tx, actor, email, token, "client-invite"); err != nil {
		return ClientInvitation{}, err
	}
	if err = audit(ctx, tx, actor, p.WorkspaceID, "project.client-invited:"+project+":"+invite.ID, now); err != nil {
		return ClientInvitation{}, err
	}
	return invite, tx.Commit()
}

func (s *Store) ClientInvitations(ctx context.Context, session, project string) ([]ClientInvitation, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, _, err = s.projectClientOwner(ctx, tx, session, project); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT id,email,expires_at FROM client_invitations WHERE project_id=? AND state='pending' AND expires_at>? ORDER BY created_at,id", project, s.now().Unix())
	if err != nil {
		return nil, err
	}
	result := []ClientInvitation{}
	for rows.Next() {
		var i ClientInvitation
		if err = rows.Scan(&i.ID, &i.Email, &i.ExpiresAt); err != nil {
			rows.Close()
			return nil, err
		}
		result = append(result, i)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return result, tx.Commit()
}

func (s *Store) RevokeClientInvitation(ctx context.Context, session, project, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, actor, err := s.projectClientOwner(ctx, tx, session, project)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, "UPDATE client_invitations SET state='revoked' WHERE id=? AND project_id=? AND state='pending'", id, project)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrDenied
	}
	if err = audit(ctx, tx, actor, p.WorkspaceID, "project.client-invite-revoked:"+project+":"+id, s.now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) AcceptClientInvitation(ctx context.Context, session, token string) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var user, email string
	err = tx.QueryRowContext(ctx, `SELECT u.id,u.email FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=? AND s.expires_at>? AND u.verified=1 AND u.disabled=0`, digest(session), s.now().Unix()).Scan(&user, &email)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrDenied
	}
	if err != nil {
		return "", err
	}
	var id, project, workspace string
	err = tx.QueryRowContext(ctx, "SELECT i.id,i.project_id,p.workspace_id"+liveClientInvitation+" AND i.email=?", digest(token), s.now().Unix(), email).Scan(&id, &project, &workspace)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrDenied
	}
	if err != nil {
		return "", err
	}
	var existing, count int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM memberships WHERE user_id=? AND workspace_id=?`, user, workspace).Scan(&existing)
	if err != nil {
		return "", err
	}
	if existing > 0 {
		return "", ErrExists
	}
	err = tx.QueryRowContext(ctx, "SELECT count(*) FROM project_clients WHERE project_id=?", project).Scan(&count)
	if err != nil {
		return "", err
	}
	if count >= 20 {
		return "", ErrClientLimit
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO project_clients(project_id,user_id,created_at) VALUES(?,?,?) ON CONFLICT(project_id,user_id) DO NOTHING", project, user, s.now().Unix()); err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE client_invitations SET state='accepted' WHERE id=?", id); err != nil {
		return "", err
	}
	if err = audit(ctx, tx, user, workspace, "project.client-invite-accepted:"+project+":"+id, s.now().Unix()); err != nil {
		return "", err
	}
	return project, tx.Commit()
}
