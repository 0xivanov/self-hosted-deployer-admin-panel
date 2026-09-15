//go:build integration

package portal

import (
	"errors"
	"testing"
	"time"
)

func TestMerchantOrderRecoveryStorageLifecycle(t *testing.T) {
	s, path, _, _, product := orderFixture(t)
	ctx := t.Context()
	defer s.Close()

	buyer := newBuyerToken(t, s)
	order, err := s.RequestMerchantOrder(ctx, buyer, product.ID, product.Revision, randomToken())
	if err != nil {
		t.Fatal(err)
	}
	originalBuyerHash, originalRequestKey := order.BuyerHash, order.RequestKey
	foreign := newBuyerToken(t, s)
	if _, err = s.IssueMerchantOrderRecoveryCode(ctx, foreign, order.ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("foreign issuance: %v", err)
	}

	first, err := s.IssueMerchantOrderRecoveryCode(ctx, buyer, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first.Code == "" || !first.ExpiresAt.After(time.Now()) {
		t.Fatal("invalid recovery code")
	}
	var storedHash string
	if err = s.db.QueryRow("SELECT code_hash FROM merchant_order_recovery_codes WHERE order_id=?", order.ID).Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	if storedHash == first.Code || storedHash == digest(buyer) {
		t.Fatal("recovery secret stored in plaintext")
	}
	var saved MerchantOrder
	if err = s.db.QueryRow("SELECT buyer_hash,request_key FROM merchant_orders WHERE id=?", order.ID).Scan(&saved.BuyerHash, &saved.RequestKey); err != nil {
		t.Fatal(err)
	}
	if saved.BuyerHash != originalBuyerHash || saved.RequestKey != originalRequestKey {
		t.Fatal("order identity fields changed")
	}

	second := newBuyerToken(t, s)
	if _, err = s.RedeemMerchantOrderRecoveryCode(ctx, second, first.Code); err != nil {
		t.Fatal(err)
	}
	if _, err = s.BuyerMerchantOrder(ctx, second, order.ID); err != nil {
		t.Fatal("recovered access: ", err)
	}
	otherOrder, err := s.RequestMerchantOrder(ctx, buyer, product.ID, product.Revision, randomToken())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.BuyerMerchantOrder(ctx, second, otherOrder.ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("recovery crossed orders: %v", err)
	}

	rotated, err := s.IssueMerchantOrderRecoveryCode(ctx, buyer, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.Code == first.Code {
		t.Fatal("code did not rotate")
	}
	third := newBuyerToken(t, s)
	if _, err = s.RedeemMerchantOrderRecoveryCode(ctx, third, first.Code); !errors.Is(err, ErrDenied) {
		t.Fatalf("old code redeemed: %v", err)
	}
	if _, err = s.RedeemMerchantOrderRecoveryCode(ctx, third, rotated.Code); err != nil {
		t.Fatal("rotated code: ", err)
	}

	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.BuyerMerchantOrder(ctx, third, order.ID); err != nil {
		t.Fatal("recovered access after restart: ", err)
	}
	if err = s.RevokeMerchantBuyer(ctx, third); err != nil {
		t.Fatal(err)
	}
	if _, err = s.BuyerMerchantOrder(ctx, third, order.ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("revoked grant remained: %v", err)
	}
	var grants int
	if err = s.db.QueryRow("SELECT count(*) FROM merchant_order_recovery_grants WHERE session_hash=?", digest(third)).Scan(&grants); err != nil {
		t.Fatal(err)
	}
	if grants != 0 {
		t.Fatalf("revoked grants: %d", grants)
	}
	if _, err = s.IssueMerchantOrderRecoveryCode(ctx, third, order.ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("revoked session issued code: %v", err)
	}
	if _, err = s.RedeemMerchantOrderRecoveryCode(ctx, third, rotated.Code); !errors.Is(err, ErrDenied) {
		t.Fatalf("revoked session redeemed code: %v", err)
	}
}

func TestMerchantOrderRecoveryCodeExpiry(t *testing.T) {
	s, _, _, _, product := orderFixture(t)
	defer s.Close()
	ctx := t.Context()
	buyer := newBuyerToken(t, s)
	order, err := s.RequestMerchantOrder(ctx, buyer, product.ID, product.Revision, randomToken())
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := s.IssueMerchantOrderRecoveryCode(ctx, buyer, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	other := newBuyerToken(t, s)
	if _, err = s.db.Exec("UPDATE merchant_buyer_sessions SET expires_at=? WHERE token_hash=?", s.now().Unix()-1, digest(other)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RedeemMerchantOrderRecoveryCode(ctx, other, recovery.Code); !errors.Is(err, ErrDenied) {
		t.Fatalf("expired session redeemed code: %v", err)
	}
	other = newBuyerToken(t, s)
	if _, err = s.db.Exec("UPDATE merchant_order_recovery_codes SET expires_at=? WHERE order_id=?", s.now().Unix(), order.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RedeemMerchantOrderRecoveryCode(ctx, other, recovery.Code); !errors.Is(err, ErrDenied) {
		t.Fatalf("expired code redeemed: %v", err)
	}
	if _, err = s.BuyerMerchantOrder(ctx, other, order.ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("expired code granted access: %v", err)
	}
}
