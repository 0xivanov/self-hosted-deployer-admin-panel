//go:build integration

package portal

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func deploymentFixture(t *testing.T) (*Store, string, Account, Session, *NodeRelease) {
	t.Helper()
	s, path, a, c, root, bundle := dependencyFixture(t)
	ctx := t.Context()
	session, err := s.Login(ctx, a.Email, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BindNodeBuildDependencies(ctx, c.Job.ID, c.ExecutionID, c.Lease, root, bundle); err != nil {
		t.Fatal(err)
	}
	if err = s.DispatchNodeBuild(ctx, c.Job.ID, c.ExecutionID, c.Lease, root, nodeSubmit(func(context.Context, NodeExecutionRequest) error { return nil })); err != nil {
		t.Fatal(err)
	}
	release, err := s.RetainNodeRelease(ctx, releaseReader(s, c, bundle.ManifestSHA256), c.Job.ProjectID, c.Job.ID)
	if err != nil || release == nil {
		t.Fatal(release, err)
	}
	return s, path, a, session, release
}
func TestNodeDeploymentRequestsRetainReleaseAndFenceRevisions(t *testing.T) {
	t.Parallel()
	s, path, _, session, release := deploymentFixture(t)
	ctx := t.Context()
	runtimeID := strings.Repeat("b", 64)
	j, err := s.RequestNodeDeployment(ctx, session.Token, release.ProjectID, release.BuildID, "deploy-retained-release", runtimeID)
	if err != nil || j.Revision != 1 || j.ArtifactSHA256 != release.ArtifactSHA256 {
		t.Fatal(j, err)
	}
	same, err := s.RequestNodeDeployment(ctx, session.Token, release.ProjectID, release.BuildID, "deploy-retained-release", runtimeID)
	if err != nil || same != j {
		t.Fatal(same, err)
	}
	if _, err = s.RequestNodeDeployment(ctx, session.Token, release.ProjectID, release.BuildID, "deploy-retained-release", strings.Repeat("c", 64)); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if _, err = s.RequestNodeDeployment(ctx, session.Token, release.ProjectID, release.BuildID, "second-deployment-key", runtimeID); !errors.Is(err, ErrPublishing) {
		t.Fatal(err)
	}
	if err = s.DeleteNodeRelease(ctx, session.Token, release.ProjectID, release.BuildID); !errors.Is(err, ErrRetained) {
		t.Fatal("pending archive deleted", err)
	}
	if _, err = s.db.Exec("DELETE FROM node_releases WHERE build_id=?", release.BuildID); err == nil {
		t.Fatal("missing database retention constraint")
	}
	if err = s.CancelNodeDeployment(ctx, session.Token, release.ProjectID, j.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.CancelNodeDeployment(ctx, session.Token, release.ProjectID, j.ID); err != nil {
		t.Fatal(err)
	}
	next, err := s.RequestNodeDeployment(ctx, session.Token, release.ProjectID, release.BuildID, "second-deployment-key", runtimeID)
	if err != nil || next.Revision != 2 || next.ReleaseID != j.ReleaseID {
		t.Fatal(next, err)
	}
	if err = s.CancelNodeDeployment(ctx, session.Token, release.ProjectID, next.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteNodeRelease(ctx, session.Token, release.ProjectID, release.BuildID); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	history, err := reopened.NodeDeployments(ctx, session.Token, release.ProjectID)
	if err != nil || len(history) != 2 || history[0].Revision != 2 || history[0].State != "cancelled" {
		t.Fatal(history, err)
	}
	// Idempotency history survives archive deletion, without recreating an operation.
	same, err = reopened.RequestNodeDeployment(ctx, session.Token, release.ProjectID, release.BuildID, "second-deployment-key", runtimeID)
	if err != nil || same.ID != next.ID || same.State != "cancelled" {
		t.Fatal(same, err)
	}
	if _, err = reopened.RequestNodeDeployment(ctx, session.Token, release.ProjectID, release.BuildID, "missing-release-key", runtimeID); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
}
func TestNodeDeploymentClaimChecksAssignmentAndDoesNotReclaim(t *testing.T) {
	t.Parallel()
	s, path, _, session, release := deploymentFixture(t)
	ctx := t.Context()
	runtimeID := strings.Repeat("b", 64)
	j, err := s.RequestNodeDeployment(ctx, session.Token, release.ProjectID, release.BuildID, "node-runtime-claim-key", runtimeID)
	if err != nil {
		t.Fatal(err)
	}
	for _, assignment := range []struct{ name, runtime, pin, arch string }{{"runtime", "wrong", release.ToolchainSHA256, "arm64"}, {"pin", runtimeID, "wrong", "arm64"}, {"architecture", runtimeID, release.ToolchainSHA256, "amd64"}} {
		t.Run(assignment.name, func(t *testing.T) {
			if claim, err := s.ClaimNodeDeployment(ctx, release.ProjectID, assignment.runtime, assignment.pin, assignment.arch); !errors.Is(err, ErrConflict) || claim != nil {
				t.Fatal(claim, err)
			}
		})
	}
	archive, err := s.NodeReleaseArchive(ctx, session.Token, release.ProjectID, release.BuildID)
	if err != nil {
		t.Fatal(err)
	}
	changed := bytes.Clone(archive)
	changed[0] ^= 1
	if _, err = s.db.Exec("UPDATE node_releases SET archive=? WHERE build_id=?", changed, release.BuildID); err != nil {
		t.Fatal(err)
	}
	if claim, err := s.ClaimNodeDeployment(ctx, release.ProjectID, runtimeID, release.ToolchainSHA256, "arm64"); err == nil || claim != nil {
		t.Fatal("corrupt release claimed", claim, err)
	}
	if _, err = s.db.Exec("UPDATE node_releases SET archive=? WHERE build_id=?", archive, release.BuildID); err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimNodeDeployment(ctx, release.ProjectID, runtimeID, release.ToolchainSHA256, "arm64")
	if err != nil || claim == nil || claim.Job.ID != j.ID || !bytes.Equal(claim.Archive, archive) || claim.OperationID == "" {
		t.Fatal(claim, err)
	}
	if _, err = s.RenewNodeDeploymentLease(ctx, j.ID, "wrong", claim.Lease); !errors.Is(err, ErrBuildLease) {
		t.Fatal(err)
	}
	if _, err = s.RenewNodeDeploymentLease(ctx, j.ID, claim.OperationID, claim.Lease); err != nil {
		t.Fatal(err)
	}
	if err = s.CancelNodeDeployment(ctx, session.Token, release.ProjectID, j.ID); !errors.Is(err, ErrPublishing) {
		t.Fatal("running job cancelled without runtime evidence", err)
	}
	if _, err = s.db.Exec("UPDATE node_deployments SET lease_until=0 WHERE id=?", j.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	next, err := reopened.ClaimNodeDeployment(ctx, release.ProjectID, runtimeID, release.ToolchainSHA256, "arm64")
	if err != nil || next != nil {
		t.Fatal("expired operation reclaimed", next, err)
	}
	if _, err = reopened.RenewNodeDeploymentLease(ctx, j.ID, claim.OperationID, claim.Lease); !errors.Is(err, ErrBuildLease) {
		t.Fatal(err)
	}
	if err = reopened.DeleteNodeRelease(ctx, session.Token, release.ProjectID, release.BuildID); !errors.Is(err, ErrRetained) {
		t.Fatal(err)
	}
}
func TestNodeDeploymentCurrentPermissionsAndMigration(t *testing.T) {
	t.Parallel()
	s, path, a, session, release := deploymentFixture(t)
	ctx := t.Context()
	runtimeID := strings.Repeat("b", 64)
	if _, err := s.db.Exec("DROP TABLE node_deployment_releases; DROP TABLE node_deployments; PRAGMA user_version=21"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, other := verifiedAccount(t, s, "other-deployment@example.test")
	if _, err = s.RequestNodeDeployment(ctx, other.Token, release.ProjectID, release.BuildID, "other-tenant-deploy-key", runtimeID); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	if _, err = s.NodeDeployments(ctx, other.Token, release.ProjectID); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	j, err := s.RequestNodeDeployment(ctx, session.Token, release.ProjectID, release.BuildID, "owner-node-deploy-key", runtimeID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("UPDATE memberships SET role='viewer' WHERE user_id=?", a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ClaimNodeDeployment(ctx, release.ProjectID, runtimeID, release.ToolchainSHA256, "arm64"); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	if err = s.CancelNodeDeployment(ctx, session.Token, release.ProjectID, j.ID); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("UPDATE memberships SET role='owner' WHERE user_id=?", a.ID); err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimNodeDeployment(ctx, release.ProjectID, runtimeID, release.ToolchainSHA256, "arm64")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("UPDATE users SET disabled=1 WHERE id=?", a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RenewNodeDeploymentLease(ctx, j.ID, claim.OperationID, claim.Lease); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	// Permission loss leaves uncertain running work and its release retained.
	now := s.now().Add(2 * time.Minute)
	s.now = func() time.Time { return now }
	if next, err := s.ClaimNodeDeployment(ctx, release.ProjectID, runtimeID, release.ToolchainSHA256, "arm64"); err != nil || next != nil {
		t.Fatal(next, err)
	}
}
