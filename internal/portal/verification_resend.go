package portal

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// RequestVerification queues a fresh verification link without revealing account
// existence or invalidating a still-valid link already in the user's inbox.
// The account-level cooldown supplements HTTP peer rate limits.
func (m *AccountMail) RequestVerification(ctx context.Context, email string) error {
	email, err := normalizeEmail(email)
	if err != nil {
		return nil
	}
	tx, err := m.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var user string
	err = tx.QueryRowContext(ctx, "SELECT id FROM users WHERE email=? AND verified=0 AND disabled=0", email).Scan(&user)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	now := m.store.now()
	var recent, total int
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(sum(CASE WHEN created_at>? THEN 1 ELSE 0 END),0),count(*) FROM mail_outbox WHERE user_id=? AND purpose='verify' AND created_at>?`, now.Add(-time.Minute).Unix(), user, now.Add(-24*time.Hour).Unix()).Scan(&recent, &total)
	if err != nil {
		return err
	}
	if recent > 0 || total >= 5 {
		return nil
	}
	token := randomToken()
	if _, err = tx.ExecContext(ctx, "DELETE FROM account_tokens WHERE user_id=? AND purpose='verify' AND expires_at<=?", user, now.Unix()); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO account_tokens VALUES(?,?,'verify',?)", digest(token), user, now.Add(24*time.Hour).Unix()); err != nil {
		return err
	}
	if err = m.enqueue(ctx, tx, user, email, token, "verify"); err != nil {
		return err
	}
	if err = audit(ctx, tx, user, "", "account.verification_requested", now.Unix()); err != nil {
		return err
	}
	return tx.Commit()
}
