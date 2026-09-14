package portal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// PendingNodeBuildExecution is trusted worker access for a pinned project. It
// returns the saved dispatch identity for observation, never a new execution or
// permission to resubmit. A preparation whose lease expired before dispatch is
// fenced and failed using the same atomic transition as WorkNodeBuild.
func (s *Store) PendingNodeBuildExecution(ctx context.Context, project, toolchain, architecture string) (*NodeExecutionRequest, error) {
	job, err := scanNodeBuild(s.db.QueryRowContext(ctx, "SELECT "+nodeBuildColumns+" FROM node_builds WHERE project_id=? AND state='running'", project))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if job.ToolchainSHA256 != toolchain || job.Plan.Architecture != architecture {
		return nil, ErrBuildConflict
	}
	var execution string
	var until int64
	var intent []byte
	if err = s.db.QueryRowContext(ctx, "SELECT execution_id,lease_until,dispatch_intent FROM node_builds WHERE id=? AND state='running'", job.ID).Scan(&execution, &until, &intent); errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	if len(intent) == 0 {
		if until <= s.now().Unix() {
			_, err = s.failUnsubmittedNodeBuild(ctx, project, job.ID, execution)
		}
		return nil, err
	}
	var request NodeExecutionRequest
	if json.Unmarshal(intent, &request) != nil || request.ExecutionID != execution || request.BuildID != job.ID || request.ProjectID != project || request.ToolchainSHA256 != toolchain || request.Plan.Architecture != architecture || request.Plan.SourceSHA256 != job.Plan.SourceSHA256 || request.Bundle.ManifestSHA256 == "" {
		return nil, ErrBuildConflict
	}
	return &request, nil
}
