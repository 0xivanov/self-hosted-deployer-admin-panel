package portal

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type Invitation struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	Email       string `json:"email"`
	Role        string `json:"role"`
	ExpiresAt   int64  `json:"expires_at"`
}

// Invite queues a link atomically with the invitation. Issuing another invitation
// for the same recipient/workspace revokes earlier links without exposing tokens.
func (m *AccountMail) Invite(ctx context.Context, session, workspace, email, role string) (Invitation, error) {
	email, err := normalizeEmail(email)
	if err != nil || (role != "owner" && role != "developer" && role != "viewer") {
		return Invitation{}, ErrInvalid
	}
	tx, err := m.store.db.BeginTx(ctx, nil)
	if err != nil {
		return Invitation{}, err
	}
	defer tx.Rollback()
	actor, err := m.store.authorizeOwner(ctx, tx, session, workspace)
	if err != nil {
		return Invitation{}, err
	}
	var exists int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM memberships m JOIN users u ON u.id=m.user_id WHERE m.workspace_id=? AND u.email=?`, workspace, email).Scan(&exists)
	if err != nil {
		return Invitation{}, err
	}
	if exists > 0 {
		return Invitation{}, ErrExists
	}
	if _, err = tx.ExecContext(ctx, "UPDATE invitations SET state='revoked' WHERE workspace_id=? AND email=? AND state='pending'", workspace, email); err != nil {
		return Invitation{}, err
	}
	invite := Invitation{ID: randomToken(), WorkspaceID: workspace, Email: email, Role: role, ExpiresAt: m.store.now().Add(7 * 24 * time.Hour).Unix()}
	token := randomToken()
	if _, err = tx.ExecContext(ctx, "INSERT INTO invitations VALUES(?,?,?,?,?,?,?,'pending',?)", invite.ID, digest(token), workspace, email, role, actor, invite.ExpiresAt, m.store.now().Unix()); err != nil {
		return Invitation{}, err
	}
	if err = m.enqueue(ctx, tx, actor, email, token, "invite"); err != nil {
		return Invitation{}, err
	}
	if err = audit(ctx, tx, actor, workspace, "invitation.created:"+invite.ID, m.store.now().Unix()); err != nil {
		return Invitation{}, err
	}
	return invite, tx.Commit()
}
func (s *Store) Invitations(ctx context.Context, session, workspace string) ([]Invitation, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = s.authorizeOwner(ctx, tx, session, workspace); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT id,workspace_id,email,role,expires_at FROM invitations WHERE workspace_id=? AND state='pending' AND expires_at>? ORDER BY created_at,id", workspace, s.now().Unix())
	if err != nil {
		return nil, err
	}
	out := []Invitation{}
	for rows.Next() {
		var i Invitation
		if err = rows.Scan(&i.ID, &i.WorkspaceID, &i.Email, &i.Role, &i.ExpiresAt); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, i)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return out, tx.Commit()
}
func (s *Store) RevokeInvitation(ctx context.Context, session, workspace, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	actor, err := s.authorizeOwner(ctx, tx, session, workspace)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, "UPDATE invitations SET state='revoked' WHERE id=? AND workspace_id=? AND state='pending'", id, workspace)
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
	if err = audit(ctx, tx, actor, workspace, "invitation.revoked:"+id, s.now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

// AcceptInvitation requires a verified signed-in account matching the recipient.
// Inviter ownership is rechecked now, not only at the time the email was sent.
func (s *Store) AcceptInvitation(ctx context.Context, session, token string) (string, error) {
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
	var id, workspace, role string
	err = tx.QueryRowContext(ctx, `SELECT i.id,i.workspace_id,i.role FROM invitations i JOIN users u ON u.id=i.inviter_id JOIN memberships m ON m.user_id=u.id AND m.workspace_id=i.workspace_id WHERE i.token_hash=? AND i.email=? AND i.state='pending' AND i.expires_at>? AND u.disabled=0 AND u.verified=1 AND m.role='owner'`, digest(token), email, s.now().Unix()).Scan(&id, &workspace, &role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrDenied
	}
	if err != nil {
		return "", err
	}
	// An old invitation cannot overwrite a subsequently assigned membership.
	var existing int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM memberships WHERE workspace_id=? AND user_id=?", workspace, user).Scan(&existing); err != nil {
		return "", err
	}
	if existing != 0 {
		return "", ErrExists
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO memberships VALUES(?,?,?)", user, workspace, role); err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE invitations SET state='accepted' WHERE id=?", id); err != nil {
		return "", err
	}
	if err = audit(ctx, tx, user, workspace, "invitation.accepted:"+id, s.now().Unix()); err != nil {
		return "", err
	}
	return workspace, tx.Commit()
}
