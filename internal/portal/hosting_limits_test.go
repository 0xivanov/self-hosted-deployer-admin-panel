//go:build integration

package portal

import (
	"errors"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodebuild"
	"strings"
	"testing"
	"time"
)

func hostingLimitsFixture(t *testing.T) (*Store, string, Account, Session) {
	t.Helper()
	s, path, a, session := billingCheckoutFixture(t)
	checkout, err := s.RequestBillingCheckout(t.Context(), session.Token, a.WorkspaceID, "starter")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BindBillingCheckout(t.Context(), checkout.ID, "cs_test_limits", "https://checkout.stripe.com/c/pay/cs_test_limits"); err != nil {
		t.Fatal(err)
	}
	checkoutEvent(t, s, "evt_limits", "checkout.session.completed", "cs_test_limits", checkout.CustomerID, checkout.ID, "sub_hosting")
	if _, err = s.ProcessBillingCheckoutEvent(t.Context(), "evt_limits"); err != nil {
		t.Fatal(err)
	}
	putHostingEvidence(t, s, checkout.CustomerID, time.Now())
	if err = s.ConfigureHostingPolicy(t.Context(), a.WorkspaceID, true); err != nil {
		t.Fatal(err)
	}
	return s, path, a, session
}
func TestHostingLimitsBoundariesUsageAndRestart(t *testing.T) {
	s, path, a, session := hostingLimitsFixture(t)
	ctx := t.Context()
	archive := testArchive(t)
	limits := HostingPlanLimits{Projects: 1, Uploads: 2, UploadBytes: int64(len(archive)), Node: false}
	if err := s.ConfigureHostingLimits(ctx, "starter", limits); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateProject(ctx, session.Token, a.WorkspaceID, "node", "node"); !errors.Is(err, ErrHostingPlanLimit) {
		t.Fatal("node included", err)
	}
	p, err := s.CreateProject(ctx, session.Token, a.WorkspaceID, "site", "static")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateProject(ctx, session.Token, a.WorkspaceID, "second", "static"); !errors.Is(err, ErrHostingPlanLimit) {
		t.Fatal("project cap", err)
	}
	u, err := s.SaveUpload(ctx, session.Token, p.ID, archive)
	if err != nil {
		t.Fatal("exact storage boundary", err)
	}
	if _, err = s.SaveUpload(ctx, session.Token, p.ID, archive); !errors.Is(err, ErrHostingPlanLimit) {
		t.Fatal("storage cap", err)
	}
	limits.Uploads = 1
	limits.UploadBytes = 2 * int64(len(archive))
	if err = s.ConfigureHostingLimits(ctx, "starter", limits); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveUpload(ctx, session.Token, p.ID, archive); !errors.Is(err, ErrHostingPlanLimit) {
		t.Fatal("archive count cap", err)
	}
	offers, err := s.BillingPlanOffers(ctx, session.Token, a.WorkspaceID)
	if err != nil || len(offers) != 1 || offers[0].Limits == nil || *offers[0].Limits != limits {
		t.Fatal("catalog limits missing", err)
	}
	access, err := s.WorkspaceHostingAccess(ctx, session.Token, a.WorkspaceID)
	if err != nil || access.Plan != "starter" || access.Limits == nil || *access.Limits != limits || access.Usage.Projects != 1 || access.Usage.Uploads != 1 || access.Usage.UploadBytes != int64(len(archive)) {
		t.Fatal("usage mismatch", access, err)
	}
	if err = s.DeleteUpload(ctx, session.Token, p.ID, u.ID); err != nil {
		t.Fatal("cleanup denied", err)
	}
	if _, err = s.SaveUpload(ctx, session.Token, p.ID, archive); err != nil {
		t.Fatal("freed quota unusable", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	access, err = s.WorkspaceHostingAccess(ctx, session.Token, a.WorkspaceID)
	if err != nil || access.Limits == nil || *access.Limits != limits {
		t.Fatal("lost limits", err)
	}
	other, otherSession := verifiedAccount(t, s, "limits-legacy@example.test")
	if _, err = s.CreateProject(ctx, otherSession.Token, other.WorkspaceID, "legacy-node", "node"); err != nil {
		t.Fatal("legacy changed", err)
	}
	if _, err = s.WorkspaceHostingAccess(ctx, otherSession.Token, a.WorkspaceID); !errors.Is(err, ErrDenied) {
		t.Fatal("foreign usage exposed", err)
	}
}
func TestHostingNodeLimitRecheckedBeforeClaim(t *testing.T) {
	s, _, a, session := hostingLimitsFixture(t)
	ctx := t.Context()
	p, err := s.CreateProject(ctx, session.Token, a.WorkspaceID, "node", "node")
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.SaveUpload(ctx, session.Token, p.ID, nodeUploadFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	assignment := NodeBuildAssignment{Settings: nodebuild.Settings{Architecture: "arm64"}, ToolchainSHA256: strings.Repeat("a", 64)}
	job, err := s.RequestNodeBuild(ctx, session.Token, p.ID, u.ID, "before-plan-change", assignment)
	if err != nil {
		t.Fatal(err)
	}
	limits := HostingPlanLimits{Projects: 2, Uploads: 2, UploadBytes: WorkspaceUploadBytes, Node: false}
	if err = s.ConfigureHostingLimits(ctx, "starter", limits); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ClaimNodeBuild(ctx, p.ID, assignment.ToolchainSHA256, "arm64"); !errors.Is(err, ErrHostingPlanLimit) {
		t.Fatal("node claim allowed", err)
	}
	if _, err = s.SaveUpload(ctx, session.Token, p.ID, nodeUploadFixture(t)); !errors.Is(err, ErrHostingPlanLimit) {
		t.Fatal("node upload allowed", err)
	}
	repeated, err := s.RequestNodeBuild(ctx, session.Token, p.ID, u.ID, "before-plan-change", assignment)
	// Replaying a saved request retains its job even after plan limits change.
	if err != nil || repeated.ID != job.ID {
		t.Fatal("request identity changed")
	}
	if err = s.CancelNodeBuild(ctx, session.Token, p.ID, job.ID); err != nil {
		t.Fatal("cancel denied", err)
	}
}
