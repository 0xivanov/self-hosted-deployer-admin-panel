package portal

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestGitHubImportDedupAndLeaseFencing(t *testing.T) {
	s, _, session, _, _, p := projectClientFixture(t)
	defer s.Close()
	if _, err := s.SaveGitHubConnection(t.Context(), session.Token, githubConnectionFixture(t, s, session.Token, p.ID), githubAccessFixture(), "main", "", false); err != nil {
		t.Fatal(err)
	}
	first, err := s.RequestGitHubImport(t.Context(), session.Token, p.ID, "lease-fencing-0001")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.RequestGitHubImport(t.Context(), session.Token, p.ID, "lease-fencing-0001")
	if err != nil || second.ID != first.ID {
		t.Fatal(second, err)
	}
	claim, err := s.ClaimGitHubImport(t.Context())
	if err != nil || claim == nil {
		t.Fatal(claim, err)
	}
	if err = s.PinGitHubImportCommit(t.Context(), claim.Job.ID, claim.Lease, strings.Repeat("a", 40)); err != nil {
		t.Fatal(err)
	}
	if err = s.PinGitHubImportCommit(t.Context(), claim.Job.ID, claim.Lease, strings.Repeat("b", 40)); !errors.Is(err, ErrGitHubImportLease) {
		t.Fatal("commit was repinned", err)
	}
	oldLease := claim.Lease
	if _, err = s.db.Exec("UPDATE github_imports SET lease_until=0 WHERE id=?", claim.Job.ID); err != nil {
		t.Fatal(err)
	}
	replacement, err := s.ClaimGitHubImport(t.Context())
	if err != nil || replacement == nil || replacement.Lease == oldLease {
		t.Fatal(replacement, err)
	}
	if err = s.PinGitHubImportCommit(t.Context(), replacement.Job.ID, oldLease, strings.Repeat("c", 40)); !errors.Is(err, ErrGitHubImportLease) {
		t.Fatal("expired lease remained valid", err)
	}
}

func TestGitHubImportClaimRetiresRevokedRequester(t *testing.T) {
	s, owner, session, _, _, p := projectClientFixture(t)
	defer s.Close()
	if _, err := s.SaveGitHubConnection(t.Context(), session.Token, githubConnectionFixture(t, s, session.Token, p.ID), githubAccessFixture(), "main", "", false); err != nil {
		t.Fatal(err)
	}
	job, err := s.RequestGitHubImport(t.Context(), session.Token, p.ID, "revoked-owner-0001")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("UPDATE memberships SET role='viewer' WHERE user_id=?", owner.ID); err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimGitHubImport(t.Context())
	if err != nil || claim != nil {
		t.Fatal(claim, err)
	}
	var state, reason string
	if err = s.db.QueryRow("SELECT state,error FROM github_imports WHERE id=?", job.ID).Scan(&state, &reason); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || reason != GitHubImportErrorUnavailable {
		t.Fatal(state, reason)
	}
}

func TestGitHubImportSchema49MigrationPreservesConnectionAndUploadFK(t *testing.T) {
	s, path := newStore(t)
	owner, session := verifiedAccount(t, s, "github-import-migration@example.test")
	p, err := s.CreateProject(t.Context(), session.Token, owner.WorkspaceID, "Website", "static")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveGitHubConnection(t.Context(), session.Token, githubConnectionFixture(t, s, session.Token, p.ID), githubAccessFixture(), "main", "", false); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	if _, err = s.db.Exec("INSERT INTO uploads VALUES(?,?,?,?,?,?,?)", "upload-migration", p.ID, "sha", 1, 1, []byte("zip"), now); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("DROP TABLE github_imports; PRAGMA user_version=48"); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	connection, err := s.ProjectGitHubConnection(t.Context(), session.Token, p.ID)
	if err != nil || !connection.Connected || connection.Revision != 1 {
		t.Fatal(connection, err)
	}
	job, err := s.RequestGitHubImport(t.Context(), session.Token, p.ID, "migration-preserve-0001")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("UPDATE github_imports SET state='succeeded',upload_id=? WHERE id=?", "upload-migration", job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("DELETE FROM uploads WHERE id=?", "upload-migration"); err != nil {
		t.Fatal(err)
	}
	var uploadID string
	if err = s.db.QueryRow("SELECT COALESCE(upload_id,'') FROM github_imports WHERE id=?", job.ID).Scan(&uploadID); err != nil {
		t.Fatal(err)
	}
	if uploadID != "" {
		t.Fatal("upload foreign key was not cleared", uploadID)
	}
}

func TestGitHubImportRequiresConnectionAuthorizerAsWellAsRequester(t *testing.T) {
	s, owner, session, _, _, p := projectClientFixture(t)
	if _, err := s.SaveGitHubConnection(t.Context(), session.Token, githubConnectionFixture(t, s, session.Token, p.ID), githubAccessFixture(), "main", "", false); err != nil {
		t.Fatal(err)
	}
	other, otherSession := verifiedAccount(t, s, "other-github-owner@example.test")
	if _, err := s.db.Exec("INSERT INTO memberships(user_id,workspace_id,role) VALUES(?,?,'owner')", other.ID, p.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	job, err := s.RequestGitHubImport(t.Context(), otherSession.Token, p.ID, "other-owner-import-01")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("UPDATE memberships SET role='viewer' WHERE user_id=? AND workspace_id=?", owner.ID, p.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimGitHubImport(t.Context())
	if err != nil || claim != nil {
		t.Fatal("revoked connection authorizer accepted", claim, err)
	}
	var state string
	if err = s.db.QueryRow("SELECT state FROM github_imports WHERE id=?", job.ID).Scan(&state); err != nil || state != "failed" {
		t.Fatal(state, err)
	}
}
