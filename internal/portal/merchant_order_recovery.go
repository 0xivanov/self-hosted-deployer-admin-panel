package portal

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const merchantOrderRecoveryGrantLimit = 100

type MerchantOrderRecoveryCode struct {
	Code      string
	ExpiresAt time.Time
}

func merchantBuyerSessionActive(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, token string, now int64) error {
	if !validMerchantOrderToken(token) {
		return ErrDenied
	}
	var expires int64
	err := q.QueryRowContext(ctx, "SELECT expires_at FROM merchant_buyer_sessions WHERE token_hash=?", digest(token)).Scan(&expires)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDenied
	}
	if err != nil {
		return err
	}
	if expires <= now {
		return ErrDenied
	}
	return nil
}

func merchantBuyerOrderAccess(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, sessionHash, orderID string, now int64) error {
	var exists int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM merchant_orders o WHERE o.id=? AND (o.buyer_hash=? OR EXISTS (SELECT 1 FROM merchant_order_recovery_grants g JOIN merchant_buyer_sessions s ON s.token_hash=g.session_hash WHERE g.order_id=o.id AND g.session_hash=? AND s.expires_at>?))`, orderID, sessionHash, sessionHash, now).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDenied
	}
	return err
}

func (s *Store) IssueMerchantOrderRecoveryCode(ctx context.Context, sessionToken, orderID string) (MerchantOrderRecoveryCode, error) {
	if !validMerchantOrderToken(sessionToken) || !validMerchantOrderToken(orderID) {
		return MerchantOrderRecoveryCode{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MerchantOrderRecoveryCode{}, err
	}
	defer tx.Rollback()
	now := s.now()
	if _, err = tx.ExecContext(ctx, "DELETE FROM merchant_buyer_sessions WHERE expires_at<=?", now.Unix()); err != nil {
		return MerchantOrderRecoveryCode{}, err
	}
	if err = merchantBuyerSessionActive(ctx, tx, sessionToken, now.Unix()); err != nil {
		return MerchantOrderRecoveryCode{}, err
	}
	if err = merchantBuyerOrderAccess(ctx, tx, digest(sessionToken), orderID, now.Unix()); err != nil {
		return MerchantOrderRecoveryCode{}, err
	}
	code := randomToken()
	recovery := MerchantOrderRecoveryCode{Code: code, ExpiresAt: now.AddDate(1, 0, 0)}
	if _, err = tx.ExecContext(ctx, "INSERT INTO merchant_order_recovery_codes(order_id,code_hash,expires_at) VALUES(?,?,?) ON CONFLICT(order_id) DO UPDATE SET code_hash=excluded.code_hash,expires_at=excluded.expires_at", orderID, digest(code), recovery.ExpiresAt.Unix()); err != nil {
		return MerchantOrderRecoveryCode{}, err
	}
	return recovery, tx.Commit()
}

func (s *Store) RedeemMerchantOrderRecoveryCode(ctx context.Context, sessionToken, code string) (MerchantOrder, error) {
	if !validMerchantOrderToken(sessionToken) || !validMerchantOrderToken(code) {
		return MerchantOrder{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MerchantOrder{}, err
	}
	defer tx.Rollback()
	now := s.now().Unix()
	if _, err = tx.ExecContext(ctx, "DELETE FROM merchant_buyer_sessions WHERE expires_at<=?", now); err != nil {
		return MerchantOrder{}, err
	}
	if err = merchantBuyerSessionActive(ctx, tx, sessionToken, now); err != nil {
		return MerchantOrder{}, err
	}
	var orderID string
	if err = tx.QueryRowContext(ctx, "SELECT order_id FROM merchant_order_recovery_codes WHERE code_hash=? AND expires_at>?", digest(code), now).Scan(&orderID); errors.Is(err, sql.ErrNoRows) {
		return MerchantOrder{}, ErrDenied
	} else if err != nil {
		return MerchantOrder{}, err
	}
	sessionHash := digest(sessionToken)
	var granted int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM merchant_order_recovery_grants WHERE order_id=?", orderID).Scan(&granted); err != nil {
		return MerchantOrder{}, err
	}
	if granted >= merchantOrderRecoveryGrantLimit {
		var existing int
		if err = tx.QueryRowContext(ctx, "SELECT 1 FROM merchant_order_recovery_grants WHERE order_id=? AND session_hash=?", orderID, sessionHash).Scan(&existing); errors.Is(err, sql.ErrNoRows) {
			return MerchantOrder{}, ErrDenied
		} else if err != nil {
			return MerchantOrder{}, err
		}
	}
	if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO merchant_order_recovery_grants(order_id,session_hash,created_at) VALUES(?,?,?)", orderID, sessionHash, now); err != nil {
		return MerchantOrder{}, err
	}
	order, _, _, _, err := scanMerchantOrder(tx.QueryRowContext(ctx, "SELECT "+merchantOrderColumns+" FROM merchant_orders WHERE id=?", orderID))
	if errors.Is(err, sql.ErrNoRows) {
		return MerchantOrder{}, ErrDenied
	}
	if err != nil {
		return MerchantOrder{}, err
	}
	if err = tx.Commit(); err != nil {
		return MerchantOrder{}, err
	}
	return order, nil
}
