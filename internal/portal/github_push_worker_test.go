package portal

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type githubPushSource struct {
	githubWorkerSource
	resolveError  error
	duringResolve func()
}

func (p *githubPushSource) ResolveBranch(ctx context.Context, installation, repository int64, name, branch string) (string, error) {
	if p.duringResolve != nil {
		p.duringResolve()
	}
	if p.resolveError != nil {
		return "", p.resolveError
	}
	return p.githubWorkerSource.ResolveBranch(ctx, installation, repository, name, branch)
}

func TestGitHubPushWorkerImportsPinnedHead(t *testing.T) {
	s, _, session, _, _, p := projectClientFixture(t)
	defer s.Close()
	if _, err := s.SaveGitHubConnection(t.Context(), session.Token, githubConnectionFixture(t, s, session.Token, p.ID), githubAccessFixture(), "main", "", true); err != nil {
		t.Fatal(err)
	}
	push := githubPushFixture()
	push.After = strings.Repeat("a", 40)
	push.Before = strings.Repeat("c", 40)
	if err := s.AcceptGitHubPush(t.Context(), push, githubPushHash(70)); err != nil {
		t.Fatal(err)
	}
	provider := &githubPushSource{githubWorkerSource: githubWorkerSource{archive: githubWorkerArchive(t)}}
	if worked, err := s.WorkGitHubPush(t.Context(), provider); err != nil || !worked {
		t.Fatal(worked, err)
	}
	if _, err := s.WorkGitHubPush(t.Context(), provider); err != nil {
		t.Fatal(err)
	}
	if worked, err := s.WorkGitHubImport(t.Context(), provider); err != nil || !worked {
		t.Fatal(worked, err)
	}
	jobs, err := s.GitHubImports(t.Context(), session.Token, p.ID)
	if err != nil || len(jobs) != 1 || jobs[0].State != "succeeded" || jobs[0].Commit != push.After {
		t.Fatal(jobs, err)
	}
	if provider.resolves != 1 || len(provider.commits) != 1 || provider.commits[0] != push.After {
		t.Fatal("source was not pinned", provider.resolves, provider.commits)
	}
	var publications int
	if err = s.db.QueryRow("SELECT count(*) FROM publication_jobs WHERE project_id=?", p.ID).Scan(&publications); err != nil || publications != 0 {
		t.Fatal(publications, err)
	}
}

func TestGitHubPushWorkerProviderFailureRecovers(t *testing.T) {
	s, _, session, _, _, p := projectClientFixture(t)
	defer s.Close()
	if _, err := s.SaveGitHubConnection(t.Context(), session.Token, githubConnectionFixture(t, s, session.Token, p.ID), githubAccessFixture(), "main", "", true); err != nil {
		t.Fatal(err)
	}
	push := githubPushFixture()
	push.After = strings.Repeat("a", 40)
	if err := s.AcceptGitHubPush(t.Context(), push, githubPushHash(71)); err != nil {
		t.Fatal(err)
	}
	provider := &githubPushSource{resolveError: errors.New("private provider diagnostic")}
	if worked, err := s.WorkGitHubPush(t.Context(), provider); !worked || err == nil || strings.Contains(err.Error(), "private") {
		t.Fatal(worked, err)
	}
	jobs, err := s.GitHubImports(t.Context(), session.Token, p.ID)
	if err != nil || len(jobs) != 0 {
		t.Fatal(jobs, err)
	}
	now := s.now()
	s.now = func() time.Time { return now.Add(3 * time.Minute) }
	provider.resolveError = nil
	if worked, err := s.WorkGitHubPush(t.Context(), provider); err != nil || !worked {
		t.Fatal(worked, err)
	}
	jobs, err = s.GitHubImports(t.Context(), session.Token, p.ID)
	if err != nil || len(jobs) != 1 || jobs[0].Commit != push.After {
		t.Fatal(jobs, err)
	}
}

func TestGitHubPushWorkerDisconnectDuringResolution(t *testing.T) {
	s, _, session, _, _, p := projectClientFixture(t)
	defer s.Close()
	if _, err := s.SaveGitHubConnection(t.Context(), session.Token, githubConnectionFixture(t, s, session.Token, p.ID), githubAccessFixture(), "main", "", true); err != nil {
		t.Fatal(err)
	}
	push := githubPushFixture()
	push.After = strings.Repeat("a", 40)
	if err := s.AcceptGitHubPush(t.Context(), push, githubPushHash(72)); err != nil {
		t.Fatal(err)
	}
	provider := &githubPushSource{duringResolve: func() {
		if err := s.DisconnectGitHub(t.Context(), session.Token, p.ID); err != nil {
			t.Fatal(err)
		}
	}}
	s.WorkGitHubPush(t.Context(), provider)
	jobs, err := s.GitHubImports(t.Context(), session.Token, p.ID)
	if err != nil || len(jobs) != 0 {
		t.Fatal(jobs, err)
	}
}
