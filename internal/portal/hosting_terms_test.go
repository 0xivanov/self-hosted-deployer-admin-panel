//go:build integration

package portal

import (
	"errors"
	"testing"
)

func TestHostingEnforcementUsesSavedCheckoutAllowances(t *testing.T) {
	original := HostingPlanLimits{Projects: 2, Uploads: 2, UploadBytes: WorkspaceUploadBytes, Node: true}
	s, _, a, session := hostingLimitsFixture(t, original)
	ctx := t.Context()
	changed := HostingPlanLimits{Projects: 1, Uploads: 1, UploadBytes: 1 << 20, Node: false}
	if err := s.ConfigureHostingLimits(ctx, "starter", changed); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"one", "two"} {
		if _, err := s.CreateProject(ctx, session.Token, a.WorkspaceID, name, "node"); err != nil {
			t.Fatal("saved allowance reduced", err)
		}
	}
	if _, err := s.CreateProject(ctx, session.Token, a.WorkspaceID, "three", "static"); !errors.Is(err, ErrHostingPlanLimit) {
		t.Fatal("saved cap bypassed", err)
	}
	access, err := s.WorkspaceHostingAccess(ctx, session.Token, a.WorkspaceID)
	if err != nil || access.Limits == nil || *access.Limits != original {
		t.Fatal("status not saved terms", err)
	}
	other, otherSession := verifiedAccount(t, s, "new-terms@example.test")
	customer, err := s.RequestBillingCustomer(ctx, otherSession.Token, other.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BindBillingCustomer(ctx, customer.RequestID, "cus_new_terms"); err != nil {
		t.Fatal(err)
	}
	checkout, err := s.RequestBillingCheckout(ctx, otherSession.Token, other.WorkspaceID, "starter")
	if err != nil || checkout.Limits == nil || *checkout.Limits != changed {
		t.Fatal("new checkout not current terms", err)
	}
}
func TestHostingCheckoutTermsMigration(t *testing.T) {
	original := HostingPlanLimits{Projects: 3, Uploads: 4, UploadBytes: 5 << 20, Node: true}
	s, path, a, session := hostingLimitsFixture(t, original)
	ctx := t.Context()
	if _, err := s.db.Exec("DROP TABLE project_domains; ALTER TABLE projects DROP COLUMN deletion_requested_at; ALTER TABLE projects DROP COLUMN deletion_error; ALTER TABLE billing_checkouts DROP COLUMN hosting_limits; PRAGMA user_version=37"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	access, err := s.WorkspaceHostingAccess(ctx, session.Token, a.WorkspaceID)
	if err != nil || access.Limits == nil || *access.Limits != original {
		t.Fatal("migration changed allowance", err)
	}
	if err = s.ConfigureHostingLimits(ctx, "starter", HostingPlanLimits{Projects: 1, Uploads: 1, UploadBytes: 1, Node: false}); err != nil {
		t.Fatal(err)
	}
	access, err = s.WorkspaceHostingAccess(ctx, session.Token, a.WorkspaceID)
	if err != nil || access.Limits == nil || *access.Limits != original {
		t.Fatal("migrated allowance not frozen", err)
	}
	if _, err = s.db.Exec("UPDATE billing_checkouts SET hosting_limits=X''"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.WorkspaceHostingAccess(ctx, session.Token, a.WorkspaceID); err == nil {
		t.Fatal("invalid empty blob became default access")
	}
}
