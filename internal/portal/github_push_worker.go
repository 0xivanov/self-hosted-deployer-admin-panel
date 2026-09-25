package portal

import (
	"context"
	"errors"
	"time"
)

// WorkGitHubPush imports only the current branch head. It never executes source
// code or publishes a site. Import/build/publication remain separate durable jobs.
func (s *Store) WorkGitHubPush(ctx context.Context, provider GitHubSourceProvider) (bool, error) {
	if provider == nil {
		return false, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	claim, err := s.ClaimGitHubPush(ctx)
	if err != nil || claim == nil {
		return false, err
	}
	c := claim.Connection
	head, err := provider.ResolveBranch(ctx, c.InstallationID, c.RepositoryID, c.Repository, c.Branch)
	if err != nil {
		// Retain the lease for bounded recovery after provider outages or shutdown.
		// Do not persist or return provider errors that may contain credentials.
		if ctx.Err() != nil {
			return true, ctx.Err()
		}
		return true, errors.New("GitHub branch could not be resolved")
	}
	_, err = s.CompleteGitHubPush(ctx, claim.Event.ID, claim.Lease, head)
	return true, err
}
