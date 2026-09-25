package portal

import (
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/githubdeploy"
)

func githubPushFixture() githubdeploy.Push {
	return githubdeploy.Push{
		InstallationID: 20, RepositoryID: 30, RepositoryFullName: "developer/website",
		Ref: "refs/heads/main", Before: strings.Repeat("a", 40), After: strings.Repeat("b", 40),
	}
}

func githubPushHash(n byte) string { return hex.EncodeToString(append([]byte{n}, make([]byte, 31)...)) }

func TestAcceptGitHubPushDeduplicatesExactPayloadAcrossProjects(t *testing.T) {
	s, owner, session, _, _, p := projectClientFixture(t)
	defer s.Close()
	if _, err := s.SaveGitHubConnection(t.Context(), session.Token, githubConnectionFixture(t, s, session.Token, p.ID), githubAccessFixture(), "main", "", true); err != nil {
		t.Fatal(err)
	}
	secondProject, err := s.CreateProject(t.Context(), session.Token, owner.WorkspaceID, "Website Two", "static")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveGitHubConnection(t.Context(), session.Token, githubConnectionFixture(t, s, session.Token, secondProject.ID), githubAccessFixture(), "main", "", true); err != nil {
		t.Fatal(err)
	}
	push := githubPushFixture()
	if err := s.AcceptGitHubPush(t.Context(), push, githubPushHash(1)); err != nil {
		t.Fatal(err)
	}
	if err := s.AcceptGitHubPush(t.Context(), push, githubPushHash(1)); err != nil {
		t.Fatal(err)
	}
	if err := s.AcceptGitHubPush(t.Context(), push, githubPushHash(2)); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRow("SELECT count(*) FROM github_push_events").Scan(&count); err != nil || count != 4 {
		t.Fatal(count, err)
	}
	_ = p
}

func TestAcceptGitHubPushSkipsWrongBindingAndRevokedConnection(t *testing.T) {
	s, _, session, _, _, p := projectClientFixture(t)
	defer s.Close()
	if _, err := s.SaveGitHubConnection(t.Context(), session.Token, githubConnectionFixture(t, s, session.Token, p.ID), githubAccessFixture(), "main", "", true); err != nil {
		t.Fatal(err)
	}
	wrong := githubPushFixture()
	wrong.RepositoryID++
	if err := s.AcceptGitHubPush(t.Context(), wrong, githubPushHash(3)); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRow("SELECT count(*) FROM github_push_events").Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	if err := s.DisconnectGitHub(t.Context(), session.Token, p.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.AcceptGitHubPush(t.Context(), githubPushFixture(), githubPushHash(4)); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("SELECT count(*) FROM github_push_events").Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}

func TestAcceptGitHubPushMigrationAndRestart(t *testing.T) {
	s, path := newStore(t)
	owner, session := verifiedAccount(t, s, "github-push-restart@example.test")
	p, err := s.CreateProject(t.Context(), session.Token, owner.WorkspaceID, "Website", "static")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveGitHubConnection(t.Context(), session.Token, githubConnectionFixture(t, s, session.Token, p.ID), githubAccessFixture(), "main", "", true); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("DROP TABLE github_push_events; DROP TABLE github_push_receipts; PRAGMA user_version=49"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.AcceptGitHubPush(t.Context(), githubPushFixture(), githubPushHash(5)); err != nil {
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
	if err = s.db.QueryRow("SELECT count(*) FROM github_push_events WHERE payload_hash=?", githubPushHash(5)).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
}

func TestAcceptGitHubPushCapacityRollsBackAndReplayStillWorks(t *testing.T) {
	s, _, session, _, _, p := projectClientFixture(t)
	if _, err := s.SaveGitHubConnection(t.Context(), session.Token, githubConnectionFixture(t, s, session.Token, p.ID), githubAccessFixture(), "main", "", true); err != nil {
		t.Fatal(err)
	}
	if err := s.AcceptGitHubPush(t.Context(), githubPushFixture(), githubPushHash(8)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<9999) INSERT INTO github_push_receipts SELECT printf('%064x',x),1 FROM n`); err != nil {
		t.Fatal(err)
	}
	push := githubPushFixture()
	push.After = strings.Repeat("c", 40)
	if err := s.AcceptGitHubPush(t.Context(), push, githubPushHash(9)); !errors.Is(err, ErrGitHubPushCapacity) {
		t.Fatal(err)
	}
	if err := s.AcceptGitHubPush(t.Context(), githubPushFixture(), githubPushHash(8)); err != nil {
		t.Fatal("replay rejected at capacity", err)
	}
	var receipts, events int
	if err := s.db.QueryRow("SELECT count(*) FROM github_push_receipts").Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("SELECT count(*) FROM github_push_events").Scan(&events); err != nil {
		t.Fatal(err)
	}
	if receipts != githubPushHistoryLimit || events != 1 {
		t.Fatal("partial intake remained", receipts, events)
	}
}
