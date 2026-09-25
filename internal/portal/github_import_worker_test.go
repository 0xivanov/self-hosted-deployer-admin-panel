package portal

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type githubWorkerSource struct {
	archive     []byte
	resolves    int
	commits     []string
	duringFetch func()
	fetchError  error
}

func (p *githubWorkerSource) ResolveBranch(context.Context, int64, int64, string, string) (string, error) {
	p.resolves++
	return strings.Repeat("a", 40), nil
}
func (p *githubWorkerSource) FetchCommit(_ context.Context, _ int64, _ int64, _ string, commit string) ([]byte, error) {
	p.commits = append(p.commits, commit)
	if p.duringFetch != nil {
		p.duringFetch()
	}
	return p.archive, p.fetchError
}
func githubWorkerArchive(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	f, err := z.Create("website-commit/index.html")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write([]byte("<h1>GitHub source</h1>")); err != nil {
		t.Fatal(err)
	}
	if err = z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func TestGitHubWorkerImportsValidatedUpload(t *testing.T) {
	s, _, session, _, _, p := projectClientFixture(t)
	attempt := githubConnectionFixture(t, s, session.Token, p.ID)
	if _, err := s.SaveGitHubConnection(t.Context(), session.Token, attempt, githubAccessFixture(), "main", "", false); err != nil {
		t.Fatal(err)
	}
	job, err := s.RequestGitHubImport(t.Context(), session.Token, p.ID, "manual-import-0001")
	if err != nil {
		t.Fatal(err)
	}
	provider := &githubWorkerSource{archive: githubWorkerArchive(t)}
	if worked, err := s.WorkGitHubImport(t.Context(), provider); err != nil || !worked {
		t.Fatal(worked, err)
	}
	jobs, err := s.GitHubImports(t.Context(), session.Token, p.ID)
	if err != nil || len(jobs) != 1 || jobs[0].ID != job.ID || jobs[0].State != "succeeded" || jobs[0].Commit != strings.Repeat("a", 40) {
		t.Fatal(jobs, err)
	}
	uploads, err := s.Uploads(t.Context(), session.Token, p.ID)
	if err != nil || len(uploads) != 1 || uploads[0].ID != jobs[0].UploadID {
		t.Fatal(uploads, err)
	}
	var publications int
	if err = s.db.QueryRow("SELECT count(*) FROM publication_jobs WHERE project_id=?", p.ID).Scan(&publications); err != nil || publications != 0 {
		t.Fatal("import unexpectedly published", err)
	}
}
func TestGitHubWorkerRecoveryKeepsPinnedCommit(t *testing.T) {
	s, _, session, _, _, p := projectClientFixture(t)
	attempt := githubConnectionFixture(t, s, session.Token, p.ID)
	if _, err := s.SaveGitHubConnection(t.Context(), session.Token, attempt, githubAccessFixture(), "main", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RequestGitHubImport(t.Context(), session.Token, p.ID, "manual-import-0002"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	provider := &githubWorkerSource{archive: githubWorkerArchive(t), fetchError: context.Canceled, duringFetch: cancel}
	if _, err := s.WorkGitHubImport(ctx, provider); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	now := s.now()
	s.now = func() time.Time { return now.Add(3 * time.Minute) }
	provider.duringFetch = nil
	provider.fetchError = nil
	if worked, err := s.WorkGitHubImport(t.Context(), provider); err != nil || !worked {
		t.Fatal(worked, err)
	}
	if provider.resolves != 1 || len(provider.commits) != 2 || provider.commits[0] != provider.commits[1] {
		t.Fatal("retry changed source", provider.resolves, provider.commits)
	}
}
func TestGitHubWorkerDisconnectDuringFetchNeverSavesUpload(t *testing.T) {
	s, _, session, _, _, p := projectClientFixture(t)
	attempt := githubConnectionFixture(t, s, session.Token, p.ID)
	if _, err := s.SaveGitHubConnection(t.Context(), session.Token, attempt, githubAccessFixture(), "main", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RequestGitHubImport(t.Context(), session.Token, p.ID, "manual-import-0003"); err != nil {
		t.Fatal(err)
	}
	provider := &githubWorkerSource{archive: githubWorkerArchive(t), duringFetch: func() {
		if err := s.DisconnectGitHub(t.Context(), session.Token, p.ID); err != nil {
			t.Fatal(err)
		}
	}}
	if _, err := s.WorkGitHubImport(t.Context(), provider); err != nil {
		t.Fatal(err)
	}
	uploads, err := s.Uploads(t.Context(), session.Token, p.ID)
	if err != nil || len(uploads) != 0 {
		t.Fatal("disconnected source stored", uploads, err)
	}
}
