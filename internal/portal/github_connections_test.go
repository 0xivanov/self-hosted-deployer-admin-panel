package portal

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/githubdeploy"
)

func githubConnectionFixture(t *testing.T, s *Store, session, project string) GitHubLinkAttempt {
	t.Helper()
	state, err := s.BeginGitHubLink(t.Context(), session, project)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := s.ConsumeGitHubLink(t.Context(), session, project, state.State)
	if err != nil {
		t.Fatal(err)
	}
	return attempt
}
func githubAccessFixture() githubdeploy.RepositoryAccess {
	return githubdeploy.RepositoryAccess{UserID: 10, Login: "developer", InstallationID: 20, RepositoryID: 30, RepositoryFullName: "developer/website", DefaultBranch: "main"}
}
func TestGitHubConnectionLifecycle(t *testing.T) {
	s, _, session, _, other, p := projectClientFixture(t)
	ctx := context.Background()
	attempt := githubConnectionFixture(t, s, session.Token, p.ID)
	c, err := s.SaveGitHubConnection(ctx, session.Token, attempt, githubAccessFixture(), "main", "site", true)
	if err != nil || !c.Connected || c.Revision != 1 || c.Directory != "site" {
		t.Fatal(c, err)
	}
	if _, err = s.SaveGitHubConnection(ctx, session.Token, attempt, githubAccessFixture(), "main", "", false); !errors.Is(err, ErrGitHubLink) {
		t.Fatal("reused completion", err)
	}
	if _, err = s.ProjectGitHubConnection(ctx, other.Token, p.ID); !errors.Is(err, ErrDenied) {
		t.Fatal("cross-user read", err)
	}
	if err = s.DisconnectGitHub(ctx, other.Token, p.ID); !errors.Is(err, ErrDenied) {
		t.Fatal("cross-user disconnect", err)
	}
	if err = s.DisconnectGitHub(ctx, session.Token, p.ID); err != nil {
		t.Fatal(err)
	}
	c, err = s.ProjectGitHubConnection(ctx, session.Token, p.ID)
	if err != nil || c.Connected || c.DeployOnPush || c.Revision != 2 {
		t.Fatal(c, err)
	}
	attempt = githubConnectionFixture(t, s, session.Token, p.ID)
	c, err = s.SaveGitHubConnection(ctx, session.Token, attempt, githubAccessFixture(), "main", ".", false)
	if err != nil || !c.Connected || c.Revision != 3 || c.Directory != "" {
		t.Fatal(c, err)
	}
}
func TestGitHubConnectionRejectsDelayedCompletion(t *testing.T) {
	for _, reason := range []string{"disconnect", "new attempt", "role removed", "expired", "another session", "forged"} {
		t.Run(reason, func(t *testing.T) {
			s, owner, session, _, _, p := projectClientFixture(t)
			ctx := t.Context()
			attempt := githubConnectionFixture(t, s, session.Token, p.ID)
			token := session.Token
			switch reason {
			case "disconnect":
				if err := s.DisconnectGitHub(ctx, token, p.ID); err != nil {
					t.Fatal(err)
				}
			case "new attempt":
				if _, err := s.BeginGitHubLink(ctx, token, p.ID); err != nil {
					t.Fatal(err)
				}
			case "role removed":
				if _, err := s.db.Exec("UPDATE memberships SET role='viewer' WHERE user_id=?", owner.ID); err != nil {
					t.Fatal(err)
				}
			case "expired":
				now := s.now()
				s.now = func() time.Time { return now.Add(10 * time.Minute) }
			case "another session":
				second, err := s.Login(ctx, owner.Email, testPassword)
				if err != nil {
					t.Fatal(err)
				}
				token = second.Token
			case "forged":
				attempt.completion = randomToken()
			}
			if _, err := s.SaveGitHubConnection(ctx, token, attempt, githubAccessFixture(), "main", "", true); err == nil {
				t.Fatal("stale completion accepted")
			}
			var count int
			if err := s.db.QueryRow("SELECT count(*) FROM github_connections").Scan(&count); err != nil || count != 0 {
				t.Fatal("saved unauthorized connection", err)
			}
		})
	}
}
func TestGitHubConnectionMigration(t *testing.T) {
	s, path := newStore(t)
	owner, session := verifiedAccount(t, s, "github-connection@example.test")
	p, err := s.CreateProject(t.Context(), session.Token, owner.WorkspaceID, "Website", "static")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("DROP TABLE github_connections; DROP TABLE github_link_completions; PRAGMA user_version=47"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	attempt := githubConnectionFixture(t, s, session.Token, p.ID)
	if _, err = s.SaveGitHubConnection(t.Context(), session.Token, attempt, githubAccessFixture(), "main", "", false); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c, err := s.ProjectGitHubConnection(t.Context(), session.Token, p.ID)
	if err != nil || !c.Connected || c.RepositoryID != 30 {
		t.Fatal(c, err)
	}
	if _, err = s.db.Exec("DELETE FROM projects WHERE id=?", p.ID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = s.db.QueryRow("SELECT count(*) FROM github_connections").Scan(&count); err != nil || count != 0 {
		t.Fatal("orphan connection", err)
	}
}
