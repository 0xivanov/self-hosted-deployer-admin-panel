package portal

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var ErrGitHubPushLease = errors.New("GitHub push lease is invalid or expired")

const githubPushLeaseDuration = 120 * time.Second

type GitHubPushClaim struct {
	Event      GitHubPushEvent
	Connection GitHubConnection
	Lease      string `json:"-"`
	LeaseUntil int64  `json:"lease_until"`
}

func (GitHubPushClaim) String() string   { return "[GitHub push claim redacted]" }
func (GitHubPushClaim) GoString() string { return "[GitHub push claim redacted]" }

func (s *Store) migrateGitHubPushProcessing() error {
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err = tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version >= 51 {
		return nil
	}
	if version != 50 {
		return errors.New("GitHub push processing migration requires portal schema 50")
	}
	_, err = tx.Exec(`CREATE TABLE IF NOT EXISTS github_push_processing(
 event_id TEXT PRIMARY KEY REFERENCES github_push_events(id) ON DELETE CASCADE,
 state TEXT NOT NULL CHECK(state IN ('running','imported','skipped','failed')),
 attempts INTEGER NOT NULL DEFAULT 0,
 error TEXT NOT NULL DEFAULT '', lease_hash TEXT NOT NULL DEFAULT '', lease_until INTEGER NOT NULL DEFAULT 0,
 created_at INTEGER NOT NULL);
 CREATE INDEX IF NOT EXISTS github_push_processing_due ON github_push_processing(state,lease_until,created_at);
 PRAGMA user_version=51;`)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ClaimGitHubPush(ctx context.Context) (*GitHubPushClaim, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := s.now().Unix()
	var e GitHubPushEvent
	e, err = scanGitHubPushEvent(tx.QueryRowContext(ctx, `SELECT `+githubPushColumns+` FROM github_push_events e LEFT JOIN github_push_processing q ON q.event_id=e.id WHERE (q.event_id IS NULL OR (q.state='running' AND q.lease_until<=?)) ORDER BY e.created_at,e.id LIMIT 1`, now))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var attempts int
	if err = tx.QueryRowContext(ctx, "SELECT COALESCE(attempts,0) FROM github_push_processing WHERE event_id=?", e.ID).Scan(&attempts); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if attempts >= 5 {
		if _, err = tx.ExecContext(ctx, "INSERT INTO github_push_processing(event_id,state,error,created_at) VALUES(?,?,?,?) ON CONFLICT(event_id) DO UPDATE SET state='failed',error=?,lease_hash='',lease_until=0", e.ID, "failed", GitHubImportErrorUnavailable, now, GitHubImportErrorUnavailable); err != nil {
			return nil, err
		}
		return nil, tx.Commit()
	}
	if e.Deleted {
		if _, err = tx.ExecContext(ctx, "INSERT INTO github_push_processing(event_id,state,error,created_at) VALUES(?,?,?,?) ON CONFLICT(event_id) DO UPDATE SET state='skipped',error='',lease_hash='',lease_until=0", e.ID, "skipped", "", now); err != nil {
			return nil, err
		}
		return nil, tx.Commit()
	}
	var c GitHubConnection
	var workspaceID, kind, deletion string
	err = tx.QueryRowContext(ctx, `SELECT c.project_id,c.revision,c.actor_id,c.github_user_id,c.github_login,c.installation_id,c.repository_id,c.repository,c.branch,c.directory,c.deploy_on_push,c.connected,c.updated_at,p.workspace_id,p.kind,CAST(p.deletion_requested_at AS TEXT)
 FROM github_connections c JOIN projects p ON p.id=c.project_id JOIN users u ON u.id=c.actor_id AND u.verified=1 AND u.disabled=0 JOIN memberships m ON m.user_id=u.id AND m.workspace_id=p.workspace_id AND m.role='owner'
	WHERE c.project_id=? AND c.revision=? AND c.actor_id=? AND c.connected=1 AND c.deploy_on_push=1 AND c.installation_id=? AND c.repository_id=? AND c.repository=? AND ?='refs/heads/'||c.branch`, e.ProjectID, e.ConnectionRevision, e.ActorID, e.InstallationID, e.RepositoryID, e.Repository, e.Ref).Scan(&c.ProjectID, &c.Revision, &c.ActorID, &c.GitHubUserID, &c.GitHubLogin, &c.InstallationID, &c.RepositoryID, &c.Repository, &c.Branch, &c.Directory, &c.DeployOnPush, &c.Connected, &c.UpdatedAt, &workspaceID, &kind, &deletion)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if errors.Is(err, sql.ErrNoRows) || deletion != "0" || (kind != "static" && kind != "node") {
		if _, updateErr := tx.ExecContext(ctx, `INSERT INTO github_push_processing(event_id,state,error,created_at) VALUES(?,?,?,?) ON CONFLICT(event_id) DO UPDATE SET state='skipped',error=?,lease_hash='',lease_until=0`, e.ID, "skipped", GitHubImportErrorUnavailable, now, GitHubImportErrorUnavailable); updateErr != nil {
			return nil, updateErr
		}
		return nil, tx.Commit()
	}
	if err != nil {
		return nil, err
	}
	if err = s.requireHostingKind(ctx, tx, workspaceID, kind); err != nil {
		if errors.Is(err, ErrHostingPayment) || errors.Is(err, ErrHostingPlanLimit) || errors.Is(err, ErrDenied) || errors.Is(err, ErrInvalid) {
			if _, updateErr := tx.ExecContext(ctx, "INSERT INTO github_push_processing(event_id,state,error,created_at) VALUES(?,?,?,?) ON CONFLICT(event_id) DO UPDATE SET state='skipped',error=?,lease_hash='',lease_until=0", e.ID, "skipped", GitHubImportErrorUnavailable, now, GitHubImportErrorUnavailable); updateErr != nil {
				return nil, updateErr
			}
			return nil, tx.Commit()
		}
		return nil, err
	}
	lease := randomToken()
	until := now + int64(githubPushLeaseDuration/time.Second)
	_, err = tx.ExecContext(ctx, `INSERT INTO github_push_processing(event_id,state,attempts,lease_hash,lease_until,created_at) VALUES(?,?,?,?,?,?) ON CONFLICT(event_id) DO UPDATE SET state='running',attempts=attempts+1,lease_hash=excluded.lease_hash,lease_until=excluded.lease_until`, e.ID, "running", 1, digest(lease), until, now)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &GitHubPushClaim{Event: e, Connection: c, Lease: lease, LeaseUntil: until}, nil
}

func (s *Store) CompleteGitHubPush(ctx context.Context, id, lease, currentHead string) (GitHubImport, error) {
	if len(lease) != 64 || !validGitHubImportCommit(currentHead) {
		return GitHubImport{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return GitHubImport{}, err
	}
	defer tx.Rollback()
	now := s.now().Unix()
	var e GitHubPushEvent
	e, err = scanGitHubPushEvent(tx.QueryRowContext(ctx, "SELECT "+githubPushColumns+" FROM github_push_events e JOIN github_push_processing q ON q.event_id=e.id WHERE e.id=? AND q.state='running' AND q.lease_hash=? AND q.lease_until>?", id, digest(lease), now))
	if errors.Is(err, sql.ErrNoRows) {
		return GitHubImport{}, ErrGitHubPushLease
	}
	if err != nil {
		return GitHubImport{}, err
	}
	if e.Deleted || currentHead != e.After {
		_, err = tx.ExecContext(ctx, "UPDATE github_push_processing SET state='skipped',error='',lease_hash='',lease_until=0 WHERE event_id=?", id)
		if err != nil {
			return GitHubImport{}, err
		}
		return GitHubImport{}, tx.Commit()
	}
	var c GitHubConnection
	var workspaceID, kind, deletion string
	err = tx.QueryRowContext(ctx, `SELECT c.project_id,c.revision,c.actor_id,c.github_user_id,c.github_login,c.installation_id,c.repository_id,c.repository,c.branch,c.directory,c.deploy_on_push,c.connected,c.updated_at,p.workspace_id,p.kind,CAST(p.deletion_requested_at AS TEXT) FROM github_connections c JOIN projects p ON p.id=c.project_id JOIN users u ON u.id=c.actor_id AND u.verified=1 AND u.disabled=0 JOIN memberships m ON m.user_id=u.id AND m.workspace_id=p.workspace_id AND m.role='owner' WHERE c.project_id=? AND c.revision=? AND c.actor_id=? AND c.connected=1 AND c.deploy_on_push=1 AND c.installation_id=? AND c.repository_id=? AND c.repository=? AND ?='refs/heads/'||c.branch`, e.ProjectID, e.ConnectionRevision, e.ActorID, e.InstallationID, e.RepositoryID, e.Repository, e.Ref).Scan(&c.ProjectID, &c.Revision, &c.ActorID, &c.GitHubUserID, &c.GitHubLogin, &c.InstallationID, &c.RepositoryID, &c.Repository, &c.Branch, &c.Directory, &c.DeployOnPush, &c.Connected, &c.UpdatedAt, &workspaceID, &kind, &deletion)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return GitHubImport{}, err
	}
	if errors.Is(err, sql.ErrNoRows) || deletion != "0" || (kind != "static" && kind != "node") {
		return GitHubImport{}, ErrGitHubPushLease
	}
	if err = s.requireHostingKind(ctx, tx, workspaceID, kind); err != nil {
		return GitHubImport{}, err
	}
	key := "github-push:" + e.ID
	job, err := scanGitHubImport(tx.QueryRowContext(ctx, "SELECT "+githubImportColumns+" FROM github_imports WHERE project_id=? AND request_key=?", e.ProjectID, key))
	if err == nil {
		_, err = tx.ExecContext(ctx, "UPDATE github_push_processing SET state='imported',lease_hash='',lease_until=0 WHERE event_id=?", id)
		if err != nil {
			return GitHubImport{}, err
		}
		return job, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return GitHubImport{}, err
	}
	var pending, total int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM github_imports WHERE project_id=? AND state IN ('queued','running')", e.ProjectID).Scan(&pending); err != nil {
		return GitHubImport{}, err
	}
	if pending > 0 {
		return GitHubImport{}, ErrConflict
	}
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM github_imports WHERE project_id=?", e.ProjectID).Scan(&total); err != nil {
		return GitHubImport{}, err
	}
	if total >= githubImportHistoryLimit {
		return GitHubImport{}, ErrGitHubImportLimit
	}
	job = GitHubImport{ID: randomToken(), ProjectID: e.ProjectID, Revision: e.ConnectionRevision, Commit: e.After, State: "queued", CreatedAt: now, ActorID: c.ActorID, RequestKey: key}
	if _, err = tx.ExecContext(ctx, "INSERT INTO github_imports(id,project_id,request_key,actor_id,revision,commit_sha,state,created_at) VALUES(?,?,?,?,?,?,?,?)", job.ID, job.ProjectID, key, job.ActorID, job.Revision, job.Commit, job.State, now); err != nil {
		return GitHubImport{}, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE github_push_processing SET state='imported',lease_hash='',lease_until=0 WHERE event_id=?", id); err != nil {
		return GitHubImport{}, err
	}
	if err = audit(ctx, tx, c.ActorID, workspaceID, "project.github-import.requested:"+job.ID, now); err != nil {
		return GitHubImport{}, err
	}
	return job, tx.Commit()
}

func (s *Store) FailGitHubPush(ctx context.Context, id, lease, reason string) error {
	if len(lease) != 64 {
		return ErrInvalid
	}
	if reason != "source_unavailable" && reason != "archive_invalid" && reason != "import_unavailable" {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	r, err := tx.ExecContext(ctx, "UPDATE github_push_processing SET state='failed',error=?,lease_hash='',lease_until=0 WHERE event_id=? AND state='running' AND lease_hash=? AND lease_until>?", reason, id, digest(lease), s.now().Unix())
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrGitHubPushLease
	}
	return tx.Commit()
}
