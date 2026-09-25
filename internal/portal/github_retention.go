package portal

import (
	"context"
	"time"
)

// GitHubHistoryPruned counts metadata records only. Uploads, artifacts, runtime
// jobs and live publication pointers are never removed by this operation.
type GitHubHistoryPruned struct{ Events, Imports, Receipts int64 }

// PruneGitHubHistory keeps at least the latest 20 events/imports per project,
// retains completed activity for 30 days, and exact payload receipts for 90 days.
// Active and not-yet-consumed work takes precedence over age/count retention.
// Each pass is bounded and runs in one transaction with normal portal writes.
func (s *Store) PruneGitHubHistory(ctx context.Context) (GitHubHistoryPruned, error) {
	var result GitHubHistoryPruned
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	cutoff := s.now().Add(-30 * 24 * time.Hour).Unix()
	r, err := tx.ExecContext(ctx, `DELETE FROM github_push_events WHERE id IN (
 SELECT e.id FROM github_push_events e JOIN github_push_processing q ON q.event_id=e.id
 LEFT JOIN github_imports i ON i.project_id=e.project_id AND i.request_key='github-push:'||e.id
 LEFT JOIN github_pipelines p ON p.event_id=e.id
 WHERE e.created_at<?
 AND (SELECT count(*) FROM github_push_events newer WHERE newer.project_id=e.project_id AND (newer.created_at>e.created_at OR (newer.created_at=e.created_at AND newer.id>e.id)))>=20
 AND (q.state IN ('skipped','failed') OR (q.state='imported' AND (i.state='failed' OR (p.state IN ('succeeded','failed','cancelled','superseded') AND p.updated_at<?))))
 AND (i.id IS NULL OR i.state NOT IN ('queued','running'))
 AND (p.id IS NULL OR p.state NOT IN ('waiting','building','publishing'))
 AND NOT EXISTS(SELECT 1 FROM publication_jobs j WHERE j.request_key='github-auto:'||e.id AND j.project_id=e.project_id AND j.state IN ('queued','running'))
 AND NOT EXISTS(SELECT 1 FROM node_builds j WHERE j.request_key='github-auto:'||e.id AND j.project_id=e.project_id AND j.state IN ('queued','running'))
 AND NOT EXISTS(SELECT 1 FROM node_deployments j WHERE j.request_key='github-auto:'||e.id AND j.project_id=e.project_id AND j.state IN ('queued','running'))
 AND NOT EXISTS(SELECT 1 FROM publications a JOIN publication_jobs j ON j.id=a.job_id WHERE j.project_id=e.project_id AND (j.request_key='github-auto:'||e.id OR j.upload_id=i.upload_id))
 AND NOT EXISTS(SELECT 1 FROM node_active_deployments a JOIN node_deployments j ON j.id=a.deployment_id JOIN node_builds b ON b.id=a.release_id WHERE j.project_id=e.project_id AND (j.request_key='github-auto:'||e.id OR b.request_key='github-auto:'||e.id OR b.upload_id=i.upload_id))
 ORDER BY e.created_at,e.id LIMIT 250)`, cutoff, cutoff)
	if err != nil {
		return result, err
	}
	if result.Events, err = r.RowsAffected(); err != nil {
		return GitHubHistoryPruned{}, err
	}
	r, err = tx.ExecContext(ctx, `DELETE FROM github_imports WHERE id IN (
 SELECT i.id FROM github_imports i WHERE i.created_at<? AND i.state IN ('succeeded','failed')
 AND (SELECT count(*) FROM github_imports newer WHERE newer.project_id=i.project_id AND (newer.created_at>i.created_at OR (newer.created_at=i.created_at AND newer.id>i.id)))>=20
 AND NOT EXISTS(SELECT 1 FROM github_push_events e WHERE e.project_id=i.project_id AND i.request_key='github-push:'||e.id)
 AND NOT EXISTS(SELECT 1 FROM github_pipelines p WHERE p.import_id=i.id)
 ORDER BY i.created_at,i.id LIMIT 250)`, cutoff)
	if err != nil {
		return GitHubHistoryPruned{}, err
	}
	if result.Imports, err = r.RowsAffected(); err != nil {
		return GitHubHistoryPruned{}, err
	}
	r, err = tx.ExecContext(ctx, `DELETE FROM github_push_receipts WHERE payload_hash IN (
 SELECT r.payload_hash FROM github_push_receipts r WHERE r.created_at<?
 AND NOT EXISTS(SELECT 1 FROM github_push_events e WHERE e.payload_hash=r.payload_hash)
 ORDER BY r.created_at,r.payload_hash LIMIT 250)`, s.now().Add(-90*24*time.Hour).Unix())
	if err != nil {
		return GitHubHistoryPruned{}, err
	}
	if result.Receipts, err = r.RowsAffected(); err != nil {
		return GitHubHistoryPruned{}, err
	}
	if err = tx.Commit(); err != nil {
		return GitHubHistoryPruned{}, err
	}
	return result, nil
}
