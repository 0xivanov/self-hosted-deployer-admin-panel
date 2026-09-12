package portal

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/npmfetch"
)

type NodeBuildExecutor interface {
	NodeExecutionSubmitter
	NodeExecutionReader
	NodeArtifactReader
}

// WorkNodeBuild advances one assigned project's build. A running execution is
// observed, never resubmitted, even when the previous worker lost its reply.
// The executor must isolate customer code in a VM and retain output/evidence.
func (s *Store) WorkNodeBuild(ctx context.Context, project, toolchain, architecture string, jobs *os.Root, downloader npmfetch.Downloader, executor NodeBuildExecutor) (bool, error) {
	if jobs == nil || downloader == nil || executor == nil {
		return false, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	job, err := scanNodeBuild(s.db.QueryRowContext(ctx, "SELECT "+nodeBuildColumns+" FROM node_builds WHERE project_id=? AND state='running'", project))
	if err == nil {
		if job.ToolchainSHA256 != toolchain || job.Plan.Architecture != architecture {
			return false, ErrBuildConflict
		}
		return s.observeNodeBuild(ctx, job, executor)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	prepared, err := s.PrepareNodeBuild(ctx, project, toolchain, architecture, jobs, downloader)
	if err != nil || prepared == nil {
		return prepared != nil, err
	}
	c := prepared.Claim
	if err = s.DispatchNodeBuild(ctx, c.Job.ID, c.ExecutionID, c.Lease, jobs, executor); err != nil {
		return true, err
	}
	_, err = s.observeNodeBuild(ctx, c.Job, executor)
	return true, err
}

func (s *Store) observeNodeBuild(ctx context.Context, job NodeBuild, executor NodeBuildExecutor) (bool, error) {
	var execution string
	var until int64
	var intent []byte
	if err := s.db.QueryRowContext(ctx, "SELECT execution_id,lease_until,dispatch_intent FROM node_builds WHERE id=?", job.ID).Scan(&execution, &until, &intent); err != nil {
		return false, err
	}
	if len(intent) == 0 {
		// Another worker may still be preparing dependencies. Once its lease
		// expires, atomically fence dispatch and fail this unsubmitted build.
		if until > s.now().Unix() {
			return false, nil
		}
		return s.failUnsubmittedNodeBuild(ctx, job.ProjectID, job.ID, execution)
	}
	started := s.now()
	call, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	o, err := executor.InspectNodeExecution(call, execution)
	if err != nil {
		return false, err
	}
	if o.ExecutionID != execution || o.SourceSHA256 != job.Plan.SourceSHA256 || o.ToolchainSHA256 != job.ToolchainSHA256 || o.Architecture != job.Plan.Architecture || o.ObservedAt.Before(started) || o.ObservedAt.After(s.now()) {
		return false, ErrBuildConflict
	}
	if !o.Retired {
		return false, nil
	}
	switch o.Outcome {
	case "succeeded":
		_, err = s.RetainNodeRelease(ctx, executor, job.ProjectID, job.ID)
		return err == nil, err
	case "failed", "cancelled":
		if until > s.now().Unix() {
			return false, nil
		}
		return s.ReconcileNodeBuildFailure(ctx, executor, job.ProjectID, job.ID)
	default:
		return false, ErrBuildConflict
	}
}

func (s *Store) failUnsubmittedNodeBuild(ctx context.Context, project, id, execution string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	// Dispatch checks the same state/lease in its write transaction. Whichever
	// commits first decides whether executor retirement evidence is required.
	result, err := tx.ExecContext(ctx, `UPDATE node_builds SET state='failed',lease_hash='',lease_until=0,result=? WHERE id=? AND project_id=? AND execution_id=? AND state='running' AND lease_until<=? AND (dispatch_intent IS NULL OR length(dispatch_intent)=0)`, []byte(`{"outcome":"failed","reason":"preparation_expired_before_dispatch"}`), id, project, execution, s.now().Unix())
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if n == 0 {
		return false, tx.Commit()
	}
	var actor, workspace string
	if err = tx.QueryRowContext(ctx, "SELECT j.actor_id,p.workspace_id FROM node_builds j JOIN projects p ON p.id=j.project_id WHERE j.id=?", id).Scan(&actor, &workspace); err != nil {
		return false, err
	}
	if err = audit(ctx, tx, actor, workspace, "node_build.preparation_failed:"+id, s.now().Unix()); err != nil {
		return false, err
	}
	return true, tx.Commit()
}
