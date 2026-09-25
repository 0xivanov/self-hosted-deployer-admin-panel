package portal

import (
	"context"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/githubdeploy"
)

// GitHubSourceProvider only obtains source; repository code is never executed
// by the portal. The existing isolated build/publication workflows handle uploads.
type GitHubSourceProvider interface {
	ResolveBranch(context.Context, int64, int64, string, string) (string, error)
	FetchCommit(context.Context, int64, int64, string, string) ([]byte, error)
}

// WorkGitHubImport advances one durable import. The resolved commit is saved
// before fetching bytes, so a restart cannot silently import a later branch head.
func (s *Store) WorkGitHubImport(ctx context.Context, provider GitHubSourceProvider) (bool, error) {
	if provider == nil {
		return false, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	claim, err := s.ClaimGitHubImport(ctx)
	if err != nil || claim == nil {
		return false, err
	}
	c := claim.Connection
	fail := func(reason string) (bool, error) {
		if ctx.Err() != nil {
			return true, ctx.Err()
		} // Leave lease for recovery on shutdown/timeout.
		return true, s.FailGitHubImport(ctx, claim.Job.ID, claim.Lease, reason)
	}
	commit := claim.Job.Commit
	if commit == "" {
		commit, err = provider.ResolveBranch(ctx, c.InstallationID, c.RepositoryID, c.Repository, c.Branch)
		if err != nil {
			return fail("source_unavailable")
		}
		if err = s.PinGitHubImportCommit(ctx, claim.Job.ID, claim.Lease, commit); err != nil {
			return fail("import_unavailable")
		}
	}
	archive, err := provider.FetchCommit(ctx, c.InstallationID, c.RepositoryID, c.Repository, commit)
	if err != nil {
		return fail("source_unavailable")
	}
	prepared, _, err := githubdeploy.PrepareArchive(ctx, archive, c.Directory, claim.Kind)
	if err != nil {
		return fail("archive_invalid")
	}
	_, err = s.CompleteGitHubImport(ctx, claim.Job.ID, claim.Lease, prepared)
	if err != nil {
		if ctx.Err() != nil {
			return true, ctx.Err()
		}
		// Completion rechecks access and quota. A failed import never publishes.
		return fail("import_unavailable")
	}
	return true, nil
}
