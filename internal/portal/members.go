package portal

import (
	"context"
	"database/sql"
	"errors"
)

var ErrLastOwner = errors.New("workspace must retain an active owner")

type Member struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Role  string `json:"role"`
}

func (s *Store) authorizeOwner(ctx context.Context, tx *sql.Tx, token, workspace string) (string, error) {
	id, err := s.authorize(ctx, tx, token, workspace, true)
	if err != nil {
		return "", err
	}
	var role string
	if err = tx.QueryRowContext(ctx, "SELECT role FROM memberships WHERE user_id=? AND workspace_id=?", id, workspace).Scan(&role); err != nil {
		return "", err
	}
	if role != "owner" {
		return "", ErrDenied
	}
	return id, nil
}
func (s *Store) Members(ctx context.Context, token, workspace string) ([]Member, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = s.authorizeOwner(ctx, tx, token, workspace); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT u.id,u.email,m.role FROM memberships m JOIN users u ON u.id=m.user_id WHERE m.workspace_id=? ORDER BY u.email`, workspace)
	if err != nil {
		return nil, err
	}
	out := []Member{}
	for rows.Next() {
		var m Member
		if err = rows.Scan(&m.ID, &m.Email, &m.Role); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, m)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return out, tx.Commit()
}

// ChangeMember updates only an existing membership. Empty role removes it.
// New membership must be established by an accepted invitation, not this API.
func (s *Store) ChangeMember(ctx context.Context, token, workspace, target, role string) error {
	if role != "" && role != "owner" && role != "developer" && role != "viewer" {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	actor, err := s.authorizeOwner(ctx, tx, token, workspace)
	if err != nil {
		return err
	}
	var old string
	err = tx.QueryRowContext(ctx, "SELECT role FROM memberships WHERE user_id=? AND workspace_id=?", target, workspace).Scan(&old)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDenied
	}
	if err != nil {
		return err
	}
	if old == role {
		return tx.Commit()
	}
	if old == "owner" && role != "owner" {
		var others int
		err = tx.QueryRowContext(ctx, `SELECT count(*) FROM memberships m JOIN users u ON u.id=m.user_id WHERE m.workspace_id=? AND m.user_id<>? AND m.role='owner' AND u.verified=1 AND u.disabled=0`, workspace, target).Scan(&others)
		if err != nil {
			return err
		}
		if others == 0 {
			return ErrLastOwner
		}
	}
	if role == "" {
		_, err = tx.ExecContext(ctx, "DELETE FROM memberships WHERE workspace_id=? AND user_id=?", workspace, target)
	} else {
		_, err = tx.ExecContext(ctx, "UPDATE memberships SET role=? WHERE workspace_id=? AND user_id=?", role, workspace, target)
	}
	if err != nil {
		return err
	}
	// Revocation also invalidates other open tabs; the next login sees fresh roles.
	if _, err = tx.ExecContext(ctx, "DELETE FROM sessions WHERE user_id=?", target); err != nil {
		return err
	}
	action := "member.role." + role
	if role == "" {
		action = "member.removed"
	}
	if err = audit(ctx, tx, actor, workspace, action+":"+target, s.now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}
