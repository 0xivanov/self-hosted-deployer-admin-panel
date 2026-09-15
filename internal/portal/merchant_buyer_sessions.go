package portal

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type MerchantBuyerSession struct {
	Token     string
	ExpiresAt time.Time
}

func (s *Store) CreateMerchantBuyerSession(ctx context.Context) (MerchantBuyerSession, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MerchantBuyerSession{}, err
	}
	defer tx.Rollback()
	now := s.now()
	if _, err = tx.ExecContext(ctx, "DELETE FROM merchant_buyer_sessions WHERE expires_at<=?", now.Unix()); err != nil {
		return MerchantBuyerSession{}, err
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM merchant_buyer_sessions").Scan(&count); err != nil {
		return MerchantBuyerSession{}, err
	}
	if count >= 100000 {
		return MerchantBuyerSession{}, ErrDenied
	}
	session := MerchantBuyerSession{Token: randomToken(), ExpiresAt: now.Add(30 * 24 * time.Hour)}
	if _, err = tx.ExecContext(ctx, "INSERT INTO merchant_buyer_sessions(token_hash,expires_at) VALUES(?,?)", digest(session.Token), session.ExpiresAt.Unix()); err != nil {
		return MerchantBuyerSession{}, err
	}
	return session, tx.Commit()
}
func (s *Store) AuthenticateMerchantBuyer(ctx context.Context, token string) error {
	if !validMerchantOrderToken(token) {
		return ErrDenied
	}
	var expires int64
	err := s.db.QueryRowContext(ctx, "SELECT expires_at FROM merchant_buyer_sessions WHERE token_hash=?", digest(token)).Scan(&expires)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDenied
	}
	if err != nil {
		return err
	}
	if expires <= s.now().Unix() {
		return ErrDenied
	}
	return nil
}
func (s *Store) RevokeMerchantBuyer(ctx context.Context, token string) error {
	if !validMerchantOrderToken(token) {
		return ErrDenied
	}
	_, err := s.db.ExecContext(ctx, "DELETE FROM merchant_buyer_sessions WHERE token_hash=?", digest(token))
	return err
}
