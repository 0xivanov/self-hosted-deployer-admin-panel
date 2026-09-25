package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/portal"
)

const policyPassword = "A-secure-password-123!"

func policyDatabase(t *testing.T) (string, portal.Account, portal.Session) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "portal.sqlite")
	s, err := portal.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	account, verification, err := s.Register(context.Background(), "policy-test@example.test", policyPassword, "Policy workspace")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Verify(context.Background(), verification); err != nil {
		t.Fatal(err)
	}
	session, err := s.Login(context.Background(), "policy-test@example.test", policyPassword)
	if err != nil {
		t.Fatal(err)
	}
	return path, account, session
}

func TestHostingPolicyLegacyFlagRemainsSupportedInTestMode(t *testing.T) {
	path, account, session := policyDatabase(t)
	var out bytes.Buffer
	if err := run([]string{"--database", path, "--workspace", account.WorkspaceID, "--require-test-subscription", "true"}, &out); err != nil {
		t.Fatalf("legacy policy: %v", err)
	}
	s, err := portal.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	access, err := s.WorkspaceHostingAccess(context.Background(), session.Token, account.WorkspaceID)
	if err != nil || access.Mode != "test_subscription" || access.Allowed {
		t.Fatalf("unexpected test policy access: %+v %v", access, err)
	}
}

func TestHostingPolicyLiveRejectsLegacyAndConflictingFlags(t *testing.T) {
	for name, args := range map[string][]string{
		"legacy":   {"--billing-mode", "live", "--database", "/missing", "--workspace", "w", "--require-test-subscription", "true"},
		"conflict": {"--database", "/missing", "--workspace", "w", "--require-subscription", "false", "--require-test-subscription", "true"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := run(args, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "require-") {
				t.Fatalf("expected argument rejection before database access, got %v", err)
			}
		})
	}
}

func TestHostingPolicyLiveModeWritesUnpaidPolicy(t *testing.T) {
	path, account, session := policyDatabase(t)
	if err := run([]string{"--billing-mode", "live", "--database", path, "--workspace", account.WorkspaceID, "--require-subscription", "true"}, &bytes.Buffer{}); err != nil {
		t.Fatalf("live policy: %v", err)
	}
	s, err := portal.OpenWithBillingMode(path, "live")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// The original workspace has no live subscription, so live policy must
	// report denied access rather than silently using test evidence.
	access, err := s.WorkspaceHostingAccess(context.Background(), session.Token, account.WorkspaceID)
	if err != nil || access.Mode != "live_subscription" || access.Allowed {
		t.Fatalf("unexpected live policy access: %+v %v", access, err)
	}
}
