//go:build integration

package portal

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBillingModeOpenPreservesSeparatePlans(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "portal.sqlite")
	s, err := OpenWithBillingMode(path, "live")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ConfigureBillingPlan(t.Context(), "starter", "price_live", true); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ConfigureBillingPlan(t.Context(), "starter", "price_test", true); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = OpenWithBillingMode(path, "live")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var price string
	if err = s.db.QueryRow("SELECT price_id FROM billing_plans WHERE mode=? AND id='starter'", s.billingModeValue()).Scan(&price); err != nil || price != "price_live" {
		t.Fatalf("live price=%s error=%v", price, err)
	}
}

func TestBillingModeOpenRejectsUnknownBeforeCreatingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "portal.sqlite")
	for _, mode := range []string{"", "LIVE", "production"} {
		if _, err := OpenWithBillingMode(path, mode); err == nil {
			t.Fatalf("accepted mode %q", mode)
		}
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("invalid mode mutated filesystem: %v", err)
	}
}

func TestLiveRegistrationRequiresHostingPayment(t *testing.T) {
	s, path := newStore(t)
	existing, existingSession := verifiedAccount(t, s, "legacy-mode@example.test")
	s.Close()
	live, err := OpenWithBillingMode(path, "live")
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	created, session := verifiedAccount(t, live, "live-mode@example.test")
	access, err := live.WorkspaceHostingAccess(t.Context(), session.Token, created.WorkspaceID)
	if err != nil || access.Allowed || access.Mode != "live_subscription" {
		t.Fatalf("new live workspace lacks payment requirement: %+v %v", access, err)
	}
	access, err = live.WorkspaceHostingAccess(t.Context(), existingSession.Token, existing.WorkspaceID)
	if err != nil || !access.Allowed || access.Mode != "legacy" {
		t.Fatalf("existing workspace policy changed: %+v %v", access, err)
	}
}
