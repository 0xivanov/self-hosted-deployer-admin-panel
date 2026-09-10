//go:build integration

package main

import (
	"io"
	"path/filepath"
	"testing"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/portal"
)

func TestConfigurePlanCommand(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "private", "portal.db")
	s, err := portal.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a, verify, err := s.Register(t.Context(), "plan-owner@example.test", "long test password for plans", "Plans")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Verify(t.Context(), verify); err != nil {
		t.Fatal(err)
	}
	session, err := s.Login(t.Context(), a.Email, "long test password for plans")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, enabled string
		want          int
	}{
		{"enable", "true", 1}, {"disable", "false", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := run([]string{"--database", path, "--plan", "starter", "--price", "price_fixture", "--enabled", tc.enabled}, io.Discard); err != nil {
				t.Fatal(err)
			}
			plans, err := s.AvailableBillingPlans(t.Context(), session.Token, a.WorkspaceID)
			if err != nil || len(plans) != tc.want {
				t.Fatal(plans, err)
			}
		})
	}
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"missing enabled", []string{"--database", path, "--plan", "starter", "--price", "price_fixture"}},
		{"invalid enabled", []string{"--database", path, "--plan", "starter", "--price", "price_fixture", "--enabled", "yes"}},
		{"missing database", []string{"--database", path + "-typo", "--plan", "starter", "--price", "price_fixture", "--enabled", "true"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := run(tc.args, io.Discard); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}
