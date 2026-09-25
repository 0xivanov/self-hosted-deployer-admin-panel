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
}, mode, token string, now int64) error {
	if !validMerchantOrderToken(token) {
		return ErrDenied
	}
	var expires int64
	err := q.QueryRowContext(ctx, "SELECT expires_at FROM merchant_buyer_sessions WHERE mode=? AND token_hash=?", mode, digest(token)).Scan(&expires)
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
}, mode, sessionHash, orderID string, now int64) error {
	var exists int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM merchant_orders o WHERE o.mode=? AND o.id=? AND (o.buyer_hash=? OR EXISTS (SELECT 1 FROM merchant_order_recovery_grants g JOIN merchant_buyer_sessions s ON s.mode=g.mode AND s.token_hash=g.session_hash WHERE g.mode=? AND g.order_id=o.id AND g.session_hash=? AND s.expires_at>?))`, mode, orderID, sessionHash, mode, sessionHash, now).Scan(&exists)
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
	mode := s.merchantModeValue()
	now := s.now()
	if _, err = tx.ExecContext(ctx, "DELETE FROM merchant_buyer_sessions WHERE mode=? AND expires_at<=?", mode, now.Unix()); err != nil {
		return MerchantOrderRecoveryCode{}, err
	}
	if err = merchantBuyerSessionActive(ctx, tx, mode, sessionToken, now.Unix()); err != nil {
		return MerchantOrderRecoveryCode{}, err
	}
	if err = merchantBuyerOrderAccess(ctx, tx, mode, digest(sessionToken), orderID, now.Unix()); err != nil {
		return MerchantOrderRecoveryCode{}, err
	}
	code := randomToken()
	recovery := MerchantOrderRecoveryCode{Code: code, ExpiresAt: now.AddDate(1, 0, 0)}
	if _, err = tx.ExecContext(ctx, "INSERT INTO merchant_order_recovery_codes(mode,order_id,code_hash,expires_at) VALUES(?,?,?,?) ON CONFLICT(mode,order_id) DO UPDATE SET code_hash=excluded.code_hash,expires_at=excluded.expires_at", mode, orderID, digest(code), recovery.ExpiresAt.Unix()); err != nil {
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
	mode := s.merchantModeValue()
	now := s.now().Unix()
	if _, err = tx.ExecContext(ctx, "DELETE FROM merchant_buyer_sessions WHERE mode=? AND expires_at<=?", mode, now); err != nil {
		return MerchantOrder{}, err
	}
	if err = merchantBuyerSessionActive(ctx, tx, mode, sessionToken, now); err != nil {
		return MerchantOrder{}, err
	}
	var orderID string
	if err = tx.QueryRowContext(ctx, "SELECT order_id FROM merchant_order_recovery_codes WHERE mode=? AND code_hash=? AND expires_at>?", mode, digest(code), now).Scan(&orderID); errors.Is(err, sql.ErrNoRows) {
		return MerchantOrder{}, ErrDenied
	} else if err != nil {
		return MerchantOrder{}, err
	}
	sessionHash := digest(sessionToken)
	var granted int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM merchant_order_recovery_grants WHERE mode=? AND order_id=?", mode, orderID).Scan(&granted); err != nil {
		return MerchantOrder{}, err
	}
	if granted >= merchantOrderRecoveryGrantLimit {
		var existing int
		if err = tx.QueryRowContext(ctx, "SELECT 1 FROM merchant_order_recovery_grants WHERE mode=? AND order_id=? AND session_hash=?", mode, orderID, sessionHash).Scan(&existing); errors.Is(err, sql.ErrNoRows) {
			return MerchantOrder{}, ErrDenied
		} else if err != nil {
			return MerchantOrder{}, err
		}
	}
	if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO merchant_order_recovery_grants(mode,order_id,session_hash,created_at) VALUES(?,?,?,?)", mode, orderID, sessionHash, now); err != nil {
		return MerchantOrder{}, err
	}
	order, _, _, _, err := scanMerchantOrder(tx.QueryRowContext(ctx, "SELECT "+merchantOrderColumns+" FROM merchant_orders WHERE mode=? AND id=?", mode, orderID))
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
