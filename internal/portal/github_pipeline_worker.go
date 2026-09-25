package portal

import (
	"context"
	"errors"
	"time"
)

// WorkGitHubPipeline advances existing worker queues; customer code is never
// executed in the portal. Runtime assignments come only from operator config.
func (s *Store) WorkGitHubPipeline(ctx context.Context, provider GitHubSourceProvider, nodes map[string]NodeProjectConfig, sites map[string]string) (bool, error) {
	if provider == nil {
		return false, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	pipeline, err := s.NextGitHubPipeline(ctx)
	if err != nil || pipeline == nil {
		return false, err
	}
	c := pipeline.Connection
	if !c.Connected || pipeline.PublicationID != "" || pipeline.DeploymentID != "" {
		return true, s.AdvanceGitHubPipeline(ctx, pipeline.ID, "", nil, false)
	}
	head, err := provider.ResolveBranch(ctx, c.InstallationID, c.RepositoryID, c.Repository, c.Branch)
	if err != nil {
		if ctx.Err() != nil {
			return true, ctx.Err()
		}
		return true, errors.New("GitHub branch could not be resolved for deployment")
	}
	var assignment *NodeProjectConfig
	if config, ok := nodes[pipeline.ProjectID]; ok {
		assignment = &config
	}
	err = s.AdvanceGitHubPipeline(ctx, pipeline.ID, head, assignment, sites[pipeline.ProjectID] != "")
	return true, err
}

func (h *HTTP) WorkGitHubPipeline(ctx context.Context) (bool, error) {
	if !h.githubAutoDeploy {
		return false, nil
	}
	provider, ok := h.githubApp.(GitHubSourceProvider)
	if !ok {
		return false, ErrInvalid
	}
	return h.store.WorkGitHubPipeline(ctx, provider, h.nodeProjectSnapshot(), h.publicationSiteSnapshot())
}
