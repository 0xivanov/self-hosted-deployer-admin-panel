//go:build integration

package portal

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPaymentModesReopenPreservesIndependentMerchantAccounts(t *testing.T) {
	s, path, owner, session, _ := orderFixture(t)
	s.Close()
	live, err := OpenWithPaymentModes(path, "test", "live")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = live.MerchantAccount(t.Context(), session.Token, owner.WorkspaceID); !errors.Is(err, ErrDenied) {
		t.Fatalf("sandbox account leaked: %v", err)
	}
	account, err := live.RequestMerchantAccount(t.Context(), session.Token, owner.WorkspaceID, "BG")
	if err != nil {
		t.Fatal(err)
	}
	live.Close()
	sandbox, err := OpenWithBillingMode(path, "live")
	if err != nil {
		t.Fatal(err)
	}
	original, err := sandbox.MerchantAccount(t.Context(), session.Token, owner.WorkspaceID)
	if err != nil || original.AccountID != "acct_orders" || original.RequestID == account.RequestID {
		t.Fatalf("sandbox account changed: %+v %v", original, err)
	}
	sandbox.Close()
	reopened, err := OpenWithPaymentModes(path, "live", "live")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	current, err := reopened.MerchantAccount(t.Context(), session.Token, owner.WorkspaceID)
	if err != nil || current.RequestID != account.RequestID {
		t.Fatalf("live account lost: %+v %v", current, err)
	}
}

func TestPaymentModesRejectInvalidBeforeFilesystem(t *testing.T) {
	for _, tc := range []struct{ name, hosting, merchant string }{
		{"empty merchant", "test", ""}, {"unknown merchant", "test", "production"}, {"uppercase merchant", "test", "LIVE"}, {"invalid hosting", "wrong", "live"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "missing", "portal.sqlite")
			if _, err := OpenWithPaymentModes(path, tc.hosting, tc.merchant); err == nil {
				t.Fatal("invalid modes accepted")
			}
			if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
				t.Fatalf("filesystem modified: %v", err)
			}
		})
	}
}
