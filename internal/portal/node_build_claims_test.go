//go:build integration

package portal

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodebuild"
)

func buildClaimFixture(t *testing.T) (*Store, string, Account, Session, NodeBuild) {
	t.Helper()
	s, path := newStore(t)
	a, session := verifiedAccount(t, s, "node-claim@example.test")
	p, err := s.CreateProject(t.Context(), session.Token, a.WorkspaceID, "app", "node")
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.SaveUpload(t.Context(), session.Token, p.ID, nodeUploadFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	j, err := s.RequestNodeBuild(t.Context(), session.Token, p.ID, u.ID, strings.Repeat("k", 16), NodeBuildAssignment{Settings: nodebuild.Settings{Architecture: "arm64"}, ToolchainSHA256: strings.Repeat("a", 64)})
	if err != nil {
		t.Fatal(err)
	}
	return s, path, a, session, j
}
func TestNodeBuildClaimBindingAndNoAutomaticReclaim(t *testing.T) {
	t.Parallel()
	s, path, _, session, j := buildClaimFixture(t)
	ctx := t.Context()
	if _, err := s.ClaimNodeBuild(ctx, j.ProjectID, strings.Repeat("b", 64), "arm64"); !errors.Is(err, ErrBuildConflict) {
		t.Fatal(err)
	}
	if _, err := s.ClaimNodeBuild(ctx, j.ProjectID, j.ToolchainSHA256, "amd64"); !errors.Is(err, ErrBuildConflict) {
		t.Fatal(err)
	}
	claim, err := s.ClaimNodeBuild(ctx, j.ProjectID, j.ToolchainSHA256, "arm64")
	if err != nil || claim == nil {
		t.Fatal(claim, err)
	}
	if claim.Job.ID != j.ID || claim.Job.State != "running" || !bytes.Equal(claim.Archive, nodeUploadFixture(t)) {
		t.Fatal("claim source mismatch")
	}
	raw, err := json.Marshal(claim)
	if err != nil || bytes.Contains(raw, []byte(claim.Lease)) || bytes.Contains(raw, []byte("Archive")) {
		t.Fatal("claim exposes private payload", err)
	}
	second, err := s.ClaimNodeBuild(ctx, j.ProjectID, j.ToolchainSHA256, "arm64")
	if err != nil || second != nil {
		t.Fatal(second, err)
	}
	if _, err = s.RenewNodeBuildLease(ctx, j.ID, claim.ExecutionID, strings.Repeat("x", 64)); !errors.Is(err, ErrBuildLease) {
		t.Fatal(err)
	}
	now := s.now()
	s.now = func() time.Time { return now.Add(30 * time.Second) }
	until, err := s.RenewNodeBuildLease(ctx, j.ID, claim.ExecutionID, claim.Lease)
	if err != nil || until <= claim.LeaseUntil {
		t.Fatal(until, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopened.now = func() time.Time { return time.Unix(until, 0) }
	if _, err = reopened.RenewNodeBuildLease(ctx, j.ID, claim.ExecutionID, claim.Lease); !errors.Is(err, ErrBuildLease) {
		t.Fatal(err)
	}
	second, err = reopened.ClaimNodeBuild(ctx, j.ProjectID, j.ToolchainSHA256, "arm64")
	if err != nil || second != nil {
		t.Fatal("expired VM job reclaimed", second, err)
	}
	jobs, err := reopened.NodeBuilds(ctx, session.Token, j.ProjectID)
	if err != nil || len(jobs) != 1 || jobs[0].State != "running" {
		t.Fatal(jobs, err)
	}
	var execution, hash string
	if err = reopened.db.QueryRow("SELECT execution_id,lease_hash FROM node_builds WHERE id=?", j.ID).Scan(&execution, &hash); err != nil || execution != claim.ExecutionID || hash == claim.Lease {
		t.Fatal(execution, err)
	}
}
func TestNodeBuildClaimRechecksPermissionAndSource(t *testing.T) {
	t.Parallel()
	s, _, a, _, j := buildClaimFixture(t)
	ctx := t.Context()
	if _, err := s.db.Exec("UPDATE memberships SET role='viewer' WHERE user_id=? AND workspace_id=?", a.ID, a.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimNodeBuild(ctx, j.ProjectID, j.ToolchainSHA256, "arm64"); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE memberships SET role='owner' WHERE user_id=? AND workspace_id=?", a.ID, a.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	var original []byte
	if err := s.db.QueryRow("SELECT archive FROM uploads WHERE id=?", j.UploadID).Scan(&original); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE uploads SET archive=? WHERE id=?", []byte("corrupt"), j.UploadID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimNodeBuild(ctx, j.ProjectID, j.ToolchainSHA256, "arm64"); !errors.Is(err, ErrBuildConflict) {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE uploads SET archive=? WHERE id=?", original, j.UploadID); err != nil {
		t.Fatal(err)
	}
	c, err := s.ClaimNodeBuild(ctx, j.ProjectID, j.ToolchainSHA256, "arm64")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("UPDATE users SET disabled=1 WHERE id=?", a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RenewNodeBuildLease(ctx, j.ID, c.ExecutionID, c.Lease); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
}

func TestConcurrentNodeBuildClaimsHaveOneWinner(t *testing.T) {
	t.Parallel()
	s, _, _, _, j := buildClaimFixture(t)
	type result struct {
		claim *NodeBuildClaim
		err   error
	}
	results := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() {
			c, err := s.ClaimNodeBuild(t.Context(), j.ProjectID, j.ToolchainSHA256, "arm64")
			results <- result{c, err}
		}()
	}
	count := 0
	for i := 0; i < 2; i++ {
		r := <-results
		if r.err != nil {
			t.Fatal(r.err)
		}
		if r.claim != nil {
			count++
		}
	}
	if count != 1 {
		t.Fatal("duplicate build dispatch claims", count)
	}
}
