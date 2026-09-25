package portal

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

type GitHubPipeline struct {
	ID            string           `json:"id"`
	ProjectID     string           `json:"project_id"`
	State         string           `json:"state"`
	Error         string           `json:"error,omitempty"`
	ImportID      string           `json:"import_id,omitempty"`
	UploadID      string           `json:"upload_id,omitempty"`
	BuildID       string           `json:"build_id,omitempty"`
	PublicationID string           `json:"publication_id,omitempty"`
	DeploymentID  string           `json:"deployment_id,omitempty"`
	UpdatedAt     int64            `json:"updated_at"`
	Connection    GitHubConnection `json:"-"`
	Commit        string           `json:"commit"`
}

// githubPipelineJobAuthorized keeps automatically created jobs tied to the
// connection and owner that authorized their source event. Ordinary jobs do
// not have a pipeline row and retain their existing authorization path.
func githubPipelineJobAuthorized(ctx context.Context, tx *sql.Tx, kind, id string) (bool, error) {
	column := ""
	switch kind {
	case "publication":
		column = "p.publication_id"
	case "build":
		column = "p.build_id"
	case "deployment":
		column = "p.deployment_id"
	default:
		return false, ErrInvalid
	}
	table := map[string]string{"publication": "publication_jobs", "build": "node_builds", "deployment": "node_deployments"}[kind]
	var key, state string
	if err := tx.QueryRowContext(ctx, "SELECT request_key,state FROM "+table+" WHERE id=?", id).Scan(&key, &state); err != nil {
		return false, err
	}
	if !strings.HasPrefix(key, "github-auto:") {
		return true, nil
	}
	// A claimed operation must finish/reconcile its actual runtime result even
	// if future automatic deployment is disabled. Original account checks still
	// apply. Connection fencing applies before the queued job starts.
	if state == "running" {
		return true, nil
	}
	var n int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM github_pipelines p
 JOIN github_push_events e ON e.id=p.event_id
 JOIN github_connections c ON c.project_id=p.project_id AND c.revision=e.connection_revision
 JOIN projects pr ON pr.id=p.project_id
 JOIN users u ON u.id=c.actor_id AND u.verified=1 AND u.disabled=0
 JOIN memberships m ON m.user_id=u.id AND m.workspace_id=pr.workspace_id AND m.role='owner'
	 WHERE `+column+`=? AND c.connected=1 AND c.deploy_on_push=1 AND c.actor_id=e.actor_id AND c.repository_id=e.repository_id AND c.installation_id=e.installation_id AND c.repository=e.repository AND 'refs/heads/'||c.branch=e.ref AND pr.deletion_requested_at=0`, id).Scan(&n)
	return n == 1, err
}

func (s *Store) migrateGitHubPipeline() error {
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var v int
	if err = tx.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return err
	}
	if v >= 52 {
		return nil
	}
	if v != 51 {
		return errors.New("GitHub pipeline migration requires portal schema 51")
	}
	_, err = tx.Exec(`CREATE TABLE IF NOT EXISTS github_pipelines(id TEXT PRIMARY KEY,event_id TEXT NOT NULL UNIQUE REFERENCES github_push_events(id) ON DELETE CASCADE,project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,import_id TEXT NOT NULL,upload_id TEXT NOT NULL DEFAULT '',state TEXT NOT NULL CHECK(state IN ('waiting','building','publishing','succeeded','failed','cancelled','superseded')),error TEXT NOT NULL DEFAULT '',build_id TEXT NOT NULL DEFAULT '',publication_id TEXT NOT NULL DEFAULT '',deployment_id TEXT NOT NULL DEFAULT '',updated_at INTEGER NOT NULL,checked_at INTEGER NOT NULL DEFAULT 0);CREATE UNIQUE INDEX IF NOT EXISTS github_pipeline_active ON github_pipelines(project_id) WHERE state IN ('waiting','building','publishing');PRAGMA user_version=52`)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) NextGitHubPipeline(ctx context.Context) (*GitHubPipeline, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Materialize work for another project even while an existing build is waiting.
	_, err = tx.ExecContext(ctx, `INSERT INTO github_pipelines(id,event_id,project_id,import_id,upload_id,state,updated_at)
 SELECT e.id,e.id,e.project_id,i.id,i.upload_id,'waiting',? FROM github_push_events e
 JOIN github_imports i ON i.request_key='github-push:'||e.id AND i.project_id=e.project_id AND i.state='succeeded' AND i.upload_id IS NOT NULL
 WHERE NOT EXISTS(SELECT 1 FROM github_pipelines q WHERE q.event_id=e.id)
 AND NOT EXISTS(SELECT 1 FROM github_pipelines q WHERE q.project_id=e.project_id AND q.state IN ('waiting','building','publishing'))
 ORDER BY e.created_at,e.id LIMIT 1`, s.now().Unix())
	if err != nil {
		return nil, err
	}
	var p GitHubPipeline
	var eventID string
	err = tx.QueryRowContext(ctx, `SELECT p.id,p.project_id,p.state,p.error,p.import_id,p.upload_id,p.build_id,p.publication_id,p.deployment_id,p.updated_at,e.after_sha,p.event_id FROM github_pipelines p JOIN github_push_events e ON e.id=p.event_id WHERE p.state IN ('waiting','building','publishing') ORDER BY p.checked_at,p.id LIMIT 1`).Scan(&p.ID, &p.ProjectID, &p.State, &p.Error, &p.ImportID, &p.UploadID, &p.BuildID, &p.PublicationID, &p.DeploymentID, &p.UpdatedAt, &p.Commit, &eventID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, tx.Commit()
	}
	if err != nil {
		return nil, err
	}
	// Logical sequence gives fair rotation even within the same clock second and
	// survives a failed provider call after this transaction commits.
	if _, err = tx.ExecContext(ctx, "UPDATE github_pipelines SET checked_at=(SELECT COALESCE(max(checked_at),0)+1 FROM github_pipelines) WHERE id=?", p.ID); err != nil {
		return nil, err
	}
	err = tx.QueryRowContext(ctx, `SELECT c.project_id,c.revision,c.actor_id,c.github_user_id,c.github_login,c.installation_id,c.repository_id,c.repository,c.branch,c.directory,c.deploy_on_push,c.connected,c.updated_at
 FROM github_connections c JOIN github_push_events e ON e.project_id=c.project_id AND e.connection_revision=c.revision AND e.actor_id=c.actor_id
 JOIN projects pr ON pr.id=c.project_id AND pr.deletion_requested_at=0
 JOIN users u ON u.id=c.actor_id AND u.verified=1 AND u.disabled=0
 JOIN memberships m ON m.user_id=u.id AND m.workspace_id=pr.workspace_id AND m.role='owner'
 WHERE e.id=? AND c.connected=1 AND c.deploy_on_push=1`, eventID).Scan(&p.Connection.ProjectID, &p.Connection.Revision, &p.Connection.ActorID, &p.Connection.GitHubUserID, &p.Connection.GitHubLogin, &p.Connection.InstallationID, &p.Connection.RepositoryID, &p.Connection.Repository, &p.Connection.Branch, &p.Connection.Directory, &p.Connection.DeployOnPush, &p.Connection.Connected, &p.Connection.UpdatedAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	return &p, tx.Commit()
}

func (s *Store) AdvanceGitHubPipeline(ctx context.Context, id, currentHead string, assignment *NodeProjectConfig, staticReady bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var p GitHubPipeline
	var eventID, commit string
	err = tx.QueryRowContext(ctx, `SELECT p.id,p.event_id,p.project_id,p.state,p.import_id,p.upload_id,p.build_id,p.publication_id,p.deployment_id,e.after_sha FROM github_pipelines p JOIN github_push_events e ON e.id=p.event_id WHERE p.id=?`, id).Scan(&p.ID, &eventID, &p.ProjectID, &p.State, &p.ImportID, &p.UploadID, &p.BuildID, &p.PublicationID, &p.DeploymentID, &commit)
	if err != nil {
		return err
	}
	if p.State != "waiting" && p.State != "building" && p.State != "publishing" {
		return tx.Commit()
	}
	finish := func(state, reason string) error {
		_, e := tx.ExecContext(ctx, "UPDATE github_pipelines SET state=?,error=?,updated_at=? WHERE id=?", state, reason, s.now().Unix(), id)
		if e != nil {
			return e
		}
		return tx.Commit()
	}
	// Submitted work has an authoritative worker result. Never call a still-running
	// publication superseded and allow a later pipeline to overtake it.
	table, job := "publication_jobs", p.PublicationID
	if p.DeploymentID != "" {
		table, job = "node_deployments", p.DeploymentID
	}
	if job != "" {
		var state string
		if err = tx.QueryRowContext(ctx, "SELECT state FROM "+table+" WHERE id=? AND project_id=?", job, p.ProjectID).Scan(&state); err != nil {
			return err
		}
		switch state {
		case "succeeded":
			return finish("succeeded", "")
		case "failed", "cancelled":
			return finish(state, "publication_failed")
		default:
			return tx.Commit()
		}
	}
	var info Project
	var actor string
	err = tx.QueryRowContext(ctx, `SELECT pr.id,pr.workspace_id,pr.name,pr.kind,c.actor_id FROM github_push_events e
 JOIN projects pr ON pr.id=e.project_id AND pr.deletion_requested_at=0
 JOIN github_connections c ON c.project_id=pr.id AND c.revision=e.connection_revision AND c.actor_id=e.actor_id AND c.connected=1 AND c.deploy_on_push=1
 AND c.repository_id=e.repository_id AND c.installation_id=e.installation_id AND c.repository=e.repository AND 'refs/heads/'||c.branch=e.ref
 JOIN users u ON u.id=c.actor_id AND u.verified=1 AND u.disabled=0
 JOIN memberships m ON m.user_id=u.id AND m.workspace_id=pr.workspace_id AND m.role='owner'
 JOIN github_imports i ON i.id=? AND i.project_id=pr.id AND i.actor_id=c.actor_id AND i.revision=c.revision AND i.state='succeeded' AND i.upload_id=? AND i.commit_sha=e.after_sha
 WHERE e.id=?`, p.ImportID, p.UploadID, eventID).Scan(&info.ID, &info.WorkspaceID, &info.Name, &info.Kind, &actor)
	if errors.Is(err, sql.ErrNoRows) {
		return finish("cancelled", "connection_changed")
	}
	if err != nil {
		return err
	}
	if !validGitHubImportCommit(currentHead) {
		return ErrInvalid
	}
	if currentHead != commit {
		return finish("superseded", "")
	}
	if err = s.requireHostingKind(ctx, tx, info.WorkspaceID, info.Kind); err != nil {
		if errors.Is(err, ErrHostingPayment) || errors.Is(err, ErrHostingPlanLimit) || errors.Is(err, ErrDenied) || errors.Is(err, ErrInvalid) {
			return finish("cancelled", "hosting_unavailable")
		}
		return err
	}
	enqueueError := func(e error) error {
		if errors.Is(e, ErrPublishing) || errors.Is(e, ErrConflict) || errors.Is(e, ErrBuildConflict) {
			return tx.Commit()
		}
		if errors.Is(e, ErrBuildQuota) || errors.Is(e, ErrInvalid) || errors.Is(e, ErrDenied) {
			return finish("failed", "deployment_unavailable")
		}
		return e
	}
	key := "github-auto:" + eventID
	if info.Kind == "static" {
		if !staticReady {
			return tx.Commit()
		}
		j, e := s.requestPublicationTx(ctx, tx, info, actor, p.UploadID, key)
		if e != nil {
			return enqueueError(e)
		}
		if _, err = tx.ExecContext(ctx, "UPDATE github_pipelines SET publication_id=? WHERE id=?", j.ID, id); err != nil {
			return err
		}
		return finish("publishing", "")
	}
	if info.Kind != "node" {
		return finish("failed", "deployment_unavailable")
	}
	if p.BuildID != "" {
		var state string
		if err = tx.QueryRowContext(ctx, "SELECT state FROM node_builds WHERE id=? AND project_id=?", p.BuildID, p.ProjectID).Scan(&state); err != nil {
			return err
		}
		if state == "failed" || state == "cancelled" {
			return finish(state, "build_failed")
		}
		if state != "succeeded" || assignment == nil {
			return tx.Commit()
		}
		j, e := s.requestNodeDeploymentTx(ctx, tx, info, actor, p.BuildID, key, assignment.RuntimeID)
		if e != nil {
			return enqueueError(e)
		}
		if _, err = tx.ExecContext(ctx, "UPDATE github_pipelines SET deployment_id=? WHERE id=?", j.ID, id); err != nil {
			return err
		}
		return finish("publishing", "")
	}
	if assignment == nil {
		return tx.Commit()
	}
	j, e := s.requestNodeBuildTx(ctx, tx, info, actor, p.UploadID, key, assignment.Build)
	if e != nil {
		return enqueueError(e)
	}
	if _, err = tx.ExecContext(ctx, "UPDATE github_pipelines SET build_id=? WHERE id=?", j.ID, id); err != nil {
		return err
	}
	return finish("building", "")
}

func (s *Store) GitHubPipelines(ctx context.Context, session, project string) ([]GitHubPipeline, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, _, err = s.projectClientOwner(ctx, tx, session, project); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT p.id,p.project_id,p.state,p.error,p.import_id,p.upload_id,p.build_id,p.publication_id,p.deployment_id,p.updated_at,e.after_sha FROM github_pipelines p JOIN github_push_events e ON e.id=p.event_id WHERE p.project_id=? ORDER BY p.updated_at DESC,p.id DESC LIMIT 20", project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GitHubPipeline{}
	for rows.Next() {
		var p GitHubPipeline
		if err = rows.Scan(&p.ID, &p.ProjectID, &p.State, &p.Error, &p.ImportID, &p.UploadID, &p.BuildID, &p.PublicationID, &p.DeploymentID, &p.UpdatedAt, &p.Commit); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return out, tx.Commit()
}
