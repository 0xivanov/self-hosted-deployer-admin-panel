package portal

import (
	"errors"
	"strings"
	"testing"
)

func setupPushProcessing(t *testing.T) (*Store, Session, Project) {
	t.Helper()
	s, _, session, _, _, p := projectClientFixture(t)
	if _, err := s.SaveGitHubConnection(t.Context(), session.Token, githubConnectionFixture(t, s, session.Token, p.ID), githubAccessFixture(), "main", "", true); err != nil {
		t.Fatal(err)
	}
	if err := s.AcceptGitHubPush(t.Context(), githubPushFixture(), githubPushHash(31)); err != nil {
		t.Fatal(err)
	}
	return s, session, p
}

func TestGitHubPushProcessingLeaseRecoveryAndAttemptLimit(t *testing.T) {
	s, _, _ := setupPushProcessing(t)
	defer s.Close()
	first, err := s.ClaimGitHubPush(t.Context())
	if err != nil || first == nil {
		t.Fatal(first, err)
	}
	if _, err = s.db.Exec("UPDATE github_push_processing SET lease_until=0 WHERE event_id=?", first.Event.ID); err != nil {
		t.Fatal(err)
	}
	second, err := s.ClaimGitHubPush(t.Context())
	if err != nil || second == nil || second.Lease == first.Lease {
		t.Fatal(second, err)
	}
	if _, err = s.CompleteGitHubPush(t.Context(), second.Event.ID, first.Lease, second.Event.After); !errors.Is(err, ErrGitHubPushLease) {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("UPDATE github_push_processing SET attempts=5,lease_until=0 WHERE event_id=?", second.Event.ID); err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimGitHubPush(t.Context())
	if err != nil || claim != nil {
		t.Fatal(claim, err)
	}
	var state string
	if err = s.db.QueryRow("SELECT state FROM github_push_processing WHERE event_id=?", second.Event.ID).Scan(&state); err != nil || state != "failed" {
		t.Fatal(state, err)
	}
}

func TestGitHubPushProcessingMismatchAndDeletedSkip(t *testing.T) {
	s, session, p := setupPushProcessing(t)
	defer s.Close()
	claim, err := s.ClaimGitHubPush(t.Context())
	if err != nil || claim == nil {
		t.Fatal(claim, err)
	}
	if _, err = s.CompleteGitHubPush(t.Context(), claim.Event.ID, claim.Lease, strings.Repeat("c", 40)); err != nil {
		t.Fatal(err)
	}
	var imports int
	if err = s.db.QueryRow("SELECT count(*) FROM github_imports WHERE project_id=?", p.ID).Scan(&imports); err != nil || imports != 0 {
		t.Fatal(imports, err)
	}
	deleted := githubPushFixture()
	deleted.Deleted = true
	deleted.After = strings.Repeat("0", 40)
	if err = s.AcceptGitHubPush(t.Context(), deleted, githubPushHash(32)); err != nil {
		t.Fatal(err)
	}
	if claim, err = s.ClaimGitHubPush(t.Context()); err != nil || claim != nil {
		t.Fatal(claim, err)
	}
	_ = session
}

func TestGitHubPushProcessingPendingManualImportAndMigration(t *testing.T) {
	s, session, p := setupPushProcessing(t)
	manual, err := s.RequestGitHubImport(t.Context(), session.Token, p.ID, "manual-pending-0001")
	if err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimGitHubPush(t.Context())
	if err != nil || claim == nil {
		t.Fatal(claim, err)
	}
	if _, err = s.CompleteGitHubPush(t.Context(), claim.Event.ID, claim.Lease, claim.Event.After); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("DELETE FROM github_imports WHERE id=?", manual.ID); err != nil {
		t.Fatal(err)
	}
	job, err := s.CompleteGitHubPush(t.Context(), claim.Event.ID, claim.Lease, claim.Event.After)
	if err != nil || job.Commit != claim.Event.After {
		t.Fatal(job, err)
	}
	var path string
	if err = s.db.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&path); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("DROP TABLE github_push_processing; PRAGMA user_version=50"); err != nil {
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
	var count int
	if err = s.db.QueryRow("SELECT count(*) FROM github_push_events WHERE id=?", claim.Event.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
}

func TestGitHubPushProcessingReconnectFencesClaim(t *testing.T) {
	s, session, p := setupPushProcessing(t)
	defer s.Close()
	claim, err := s.ClaimGitHubPush(t.Context())
	if err != nil || claim == nil {
		t.Fatal(claim, err)
	}
	if _, err = s.SaveGitHubConnection(t.Context(), session.Token, githubConnectionFixture(t, s, session.Token, p.ID), githubAccessFixture(), "main", "", true); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CompleteGitHubPush(t.Context(), claim.Event.ID, claim.Lease, claim.Event.After); !errors.Is(err, ErrGitHubPushLease) {
		t.Fatal(err)
	}
	var count int
	if err = s.db.QueryRow("SELECT count(*) FROM github_imports WHERE project_id=?", p.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}
