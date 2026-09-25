package portal

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/projectarchive"
)

var (
	ErrGitHubImportLease = errors.New("GitHub import lease is invalid or expired")
	ErrGitHubImportLimit = errors.New("GitHub import history limit reached")
)

const (
	GitHubImportErrorSourceUnavailable = "source_unavailable"
	GitHubImportErrorArchiveInvalid    = "archive_invalid"
	GitHubImportErrorUnavailable       = "import_unavailable"
)

const (
	githubImportLeaseDuration = 120 * time.Second
	githubImportHistoryLimit  = 100
)

type GitHubImport struct {
	ID         string `json:"id"`
	ProjectID  string `json:"project_id"`
	Revision   int64  `json:"revision"`
	Commit     string `json:"commit"`
	UploadID   string `json:"upload_id,omitempty"`
	State      string `json:"state"`
	Error      string `json:"error,omitempty"`
	CreatedAt  int64  `json:"created_at"`
	ActorID    string `json:"-"`
	RequestKey string `json:"-"`
	Attempts   int    `json:"-"`
}

type GitHubImportClaim struct {
	Job        GitHubImport
	Connection GitHubConnection
	Kind       string
	Lease      string `json:"-"`
	LeaseUntil int64  `json:"lease_until"`
}

func (GitHubImportClaim) String() string   { return "[GitHub import claim redacted]" }
func (GitHubImportClaim) GoString() string { return "[GitHub import claim redacted]" }

func (s *Store) migrateGitHubImports() error {
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err = tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version >= 49 {
		return nil
	}
	if version != 48 {
		return errors.New("GitHub imports require portal schema 48")
	}
	_, err = tx.Exec(`CREATE TABLE IF NOT EXISTS github_imports(
 id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
 request_key TEXT NOT NULL,
 actor_id TEXT NOT NULL REFERENCES users(id),
 revision INTEGER NOT NULL,
 commit_sha TEXT NOT NULL DEFAULT '',
	attempts INTEGER NOT NULL DEFAULT 0,
 upload_id TEXT REFERENCES uploads(id) ON DELETE SET NULL,
 state TEXT NOT NULL CHECK(state IN ('queued','running','succeeded','failed')),
 error TEXT NOT NULL DEFAULT '',
 created_at INTEGER NOT NULL,
 lease_hash TEXT NOT NULL DEFAULT '',
 lease_until INTEGER NOT NULL DEFAULT 0,
 UNIQUE(project_id,request_key));
 CREATE INDEX IF NOT EXISTS github_imports_project ON github_imports(project_id,created_at,id);
 CREATE INDEX IF NOT EXISTS github_imports_queue ON github_imports(state,lease_until,created_at,id);
 PRAGMA user_version=49;`)
	if err != nil {
		return err
	}
	return tx.Commit()
}

const githubImportColumns = "id,project_id,request_key,actor_id,revision,commit_sha,COALESCE(upload_id,''),state,error,created_at,attempts"

func scanGitHubImport(row interface{ Scan(...any) error }) (GitHubImport, error) {
	var j GitHubImport
	err := row.Scan(&j.ID, &j.ProjectID, &j.RequestKey, &j.ActorID, &j.Revision, &j.Commit, &j.UploadID, &j.State, &j.Error, &j.CreatedAt, &j.Attempts)
	return j, err
}

func validGitHubImportRequestKey(key string) bool { return len(key) >= 16 && len(key) <= 128 }

func validGitHubImportCommit(commit string) bool {
	if len(commit) != 40 || commit == strings.Repeat("0", 40) {
		return false
	}
	decoded, err := hex.DecodeString(commit)
	return err == nil && len(decoded) == 20 && hex.EncodeToString(decoded) == commit
}

func (s *Store) RequestGitHubImport(ctx context.Context, session, project, requestKey string) (GitHubImport, error) {
	if !validGitHubImportRequestKey(requestKey) {
		return GitHubImport{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return GitHubImport{}, err
	}
	defer tx.Rollback()
	p, actor, err := s.projectClientOwner(ctx, tx, session, project)
	if err != nil {
		return GitHubImport{}, err
	}
	if p.Kind != "static" && p.Kind != "node" {
		return GitHubImport{}, ErrInvalid
	}
	if err = s.requireHostingKind(ctx, tx, p.WorkspaceID, p.Kind); err != nil {
		return GitHubImport{}, err
	}
	job, err := scanGitHubImport(tx.QueryRowContext(ctx, "SELECT "+githubImportColumns+" FROM github_imports WHERE project_id=? AND request_key=?", project, requestKey))
	if err == nil {
		return job, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return GitHubImport{}, err
	}
	var revision int64
	if err = tx.QueryRowContext(ctx, "SELECT revision FROM github_connections WHERE project_id=? AND connected=1", project).Scan(&revision); errors.Is(err, sql.ErrNoRows) {
		return GitHubImport{}, ErrDenied
	} else if err != nil {
		return GitHubImport{}, err
	}
	var pending int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM github_imports WHERE project_id=? AND state IN ('queued','running')", project).Scan(&pending); err != nil {
		return GitHubImport{}, err
	}
	if pending != 0 {
		return GitHubImport{}, ErrConflict
	}
	var total int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM github_imports WHERE project_id=?", project).Scan(&total); err != nil {
		return GitHubImport{}, err
	}
	if total >= githubImportHistoryLimit {
		return GitHubImport{}, ErrGitHubImportLimit
	}
	now := s.now().Unix()
	job = GitHubImport{ID: randomToken(), ProjectID: project, Revision: revision, State: "queued", CreatedAt: now, ActorID: actor, RequestKey: requestKey}
	if _, err = tx.ExecContext(ctx, "INSERT INTO github_imports(id,project_id,request_key,actor_id,revision,state,created_at) VALUES(?,?,?,?,?,?,?)", job.ID, project, requestKey, actor, revision, job.State, now); err != nil {
		return GitHubImport{}, err
	}
	if err = audit(ctx, tx, actor, p.WorkspaceID, "project.github-import.requested:"+job.ID, now); err != nil {
		return GitHubImport{}, err
	}
	return job, tx.Commit()
}

func githubImportCurrent(ctx context.Context, s *Store, tx *sql.Tx, job GitHubImport) (Project, GitHubConnection, error) {
	var p Project
	var requestedAt int64
	var c GitHubConnection
	err := tx.QueryRowContext(ctx, `SELECT p.id,p.workspace_id,p.name,p.kind,p.deletion_requested_at,p.deletion_error,
 c.project_id,c.revision,c.actor_id,c.github_user_id,c.github_login,c.installation_id,c.repository_id,c.repository,c.branch,c.directory,c.deploy_on_push,c.connected,c.updated_at
 FROM github_imports i JOIN projects p ON p.id=i.project_id
 JOIN users u ON u.id=i.actor_id AND u.verified=1 AND u.disabled=0
 JOIN memberships m ON m.user_id=u.id AND m.workspace_id=p.workspace_id AND m.role='owner'
 JOIN github_connections c ON c.project_id=i.project_id AND c.connected=1 AND c.revision=i.revision
 JOIN users authorizer ON authorizer.id=c.actor_id AND authorizer.verified=1 AND authorizer.disabled=0
 JOIN memberships authorization ON authorization.user_id=authorizer.id AND authorization.workspace_id=p.workspace_id AND authorization.role='owner'
 WHERE i.id=? AND i.state IN ('queued','running') AND p.deletion_requested_at=0`, job.ID).Scan(
		&p.ID, &p.WorkspaceID, &p.Name, &p.Kind, &requestedAt, &p.DeletionError,
		&c.ProjectID, &c.Revision, &c.ActorID, &c.GitHubUserID, &c.GitHubLogin, &c.InstallationID, &c.RepositoryID, &c.Repository, &c.Branch, &c.Directory, &c.DeployOnPush, &c.Connected, &c.UpdatedAt)
	p.Deleting = requestedAt != 0
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, GitHubConnection{}, ErrDenied
	}
	if err != nil {
		return Project{}, GitHubConnection{}, err
	}
	if p.Kind != "static" && p.Kind != "node" {
		return Project{}, GitHubConnection{}, ErrInvalid
	}
	if err = s.requireHostingKind(ctx, tx, p.WorkspaceID, p.Kind); err != nil {
		return Project{}, GitHubConnection{}, err
	}
	return p, c, nil
}

func (s *Store) ClaimGitHubImport(ctx context.Context) (*GitHubImportClaim, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := s.now().Unix()
	job, err := scanGitHubImport(tx.QueryRowContext(ctx, "SELECT "+githubImportColumns+" FROM github_imports WHERE state='queued' OR (state='running' AND lease_until<=?) ORDER BY created_at,id LIMIT 1", now))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if job.Attempts >= 5 {
		if _, err = tx.ExecContext(ctx, "UPDATE github_imports SET state='failed',error=?,lease_hash='',lease_until=0 WHERE id=?", GitHubImportErrorUnavailable, job.ID); err != nil {
			return nil, err
		}
		return nil, tx.Commit()
	}
	p, connection, err := githubImportCurrent(ctx, s, tx, job)
	if err != nil {
		if errors.Is(err, ErrDenied) || errors.Is(err, ErrInvalid) || errors.Is(err, ErrHostingPayment) || errors.Is(err, ErrHostingPlanLimit) {
			if _, updateErr := tx.ExecContext(ctx, "UPDATE github_imports SET state='failed',error=?,lease_hash='',lease_until=0 WHERE id=?", GitHubImportErrorUnavailable, job.ID); updateErr != nil {
				return nil, updateErr
			}
			return nil, tx.Commit()
		}
		return nil, err
	}
	claim := &GitHubImportClaim{Job: job, Connection: connection, Kind: p.Kind, Lease: randomToken(), LeaseUntil: now + int64(githubImportLeaseDuration/time.Second)}
	result, err := tx.ExecContext(ctx, "UPDATE github_imports SET state='running',attempts=attempts+1,lease_hash=?,lease_until=? WHERE id=? AND (state='queued' OR (state='running' AND lease_until<=?))", digest(claim.Lease), claim.LeaseUntil, job.ID, now)
	if err != nil {
		return nil, err
	}
	if n, err := result.RowsAffected(); err != nil {
		return nil, err
	} else if n != 1 {
		return nil, ErrGitHubImportLease
	}
	claim.Job.State = "running"
	return claim, tx.Commit()
}

func (s *Store) PinGitHubImportCommit(ctx context.Context, id, lease, commit string) error {
	if !validGitHubImportCommit(commit) || len(lease) != 64 {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	job, err := scanGitHubImport(tx.QueryRowContext(ctx, "SELECT "+githubImportColumns+" FROM github_imports WHERE id=? AND state='running' AND lease_hash=? AND lease_until>?", id, digest(lease), s.now().Unix()))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrGitHubImportLease
	}
	if err != nil {
		return err
	}
	if _, _, err = githubImportCurrent(ctx, s, tx, job); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, "UPDATE github_imports SET commit_sha=? WHERE id=? AND state='running' AND lease_hash=? AND lease_until>? AND commit_sha=''", commit, id, digest(lease), s.now().Unix())
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrGitHubImportLease
	}
	return tx.Commit()
}

func (s *Store) CompleteGitHubImport(ctx context.Context, id, lease string, preparedZIP []byte) (Upload, error) {
	if len(lease) != 64 {
		return Upload{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Upload{}, err
	}
	defer tx.Rollback()
	var job GitHubImport
	job, err = scanGitHubImport(tx.QueryRowContext(ctx, "SELECT "+githubImportColumns+" FROM github_imports WHERE id=? AND state='running' AND lease_hash=? AND lease_until>?", id, digest(lease), s.now().Unix()))
	if errors.Is(err, sql.ErrNoRows) {
		return Upload{}, ErrGitHubImportLease
	}
	if err != nil {
		return Upload{}, err
	}
	p, _, err := githubImportCurrent(ctx, s, tx, job)
	if err != nil {
		return Upload{}, err
	}
	if job.Commit == "" {
		return Upload{}, ErrInvalid
	}
	manifest, err := projectarchive.Validate(ctx, preparedZIP, p.Kind)
	if err != nil {
		return Upload{}, fmt.Errorf("%w: %s", ErrArchive, err)
	}
	var count, bytes int64
	if err = tx.QueryRowContext(ctx, "SELECT count(*),COALESCE(sum(length(u.archive)),0) FROM uploads u JOIN projects p ON p.id=u.project_id WHERE p.workspace_id=?", p.WorkspaceID).Scan(&count, &bytes); err != nil {
		return Upload{}, err
	}
	if count >= WorkspaceUploadCount || bytes+int64(len(preparedZIP)) > WorkspaceUploadBytes {
		return Upload{}, ErrQuota
	}
	if err = s.requireHostingUpload(ctx, tx, p.WorkspaceID, count, bytes, int64(len(preparedZIP))); err != nil {
		return Upload{}, err
	}
	now := s.now().Unix()
	u := Upload{ID: randomToken(), ProjectID: p.ID, SHA256: manifest.SHA256, Files: manifest.Files, ExpandedBytes: manifest.Bytes, CompressedBytes: int64(len(preparedZIP)), CreatedAt: now}
	if _, err = tx.ExecContext(ctx, "INSERT INTO uploads VALUES(?,?,?,?,?,?,?)", u.ID, p.ID, u.SHA256, u.Files, u.ExpandedBytes, preparedZIP, u.CreatedAt); err != nil {
		return Upload{}, err
	}
	result, err := tx.ExecContext(ctx, "UPDATE github_imports SET upload_id=?,state='succeeded',error='',lease_hash='',lease_until=0 WHERE id=? AND state='running' AND lease_hash=? AND lease_until>?", u.ID, id, digest(lease), now)
	if err != nil {
		return Upload{}, err
	}
	if n, e := result.RowsAffected(); e != nil {
		return Upload{}, e
	} else if n != 1 {
		return Upload{}, ErrGitHubImportLease
	}
	if err = audit(ctx, tx, job.ActorID, p.WorkspaceID, "project.github-import.completed:"+id, now); err != nil {
		return Upload{}, err
	}
	return u, tx.Commit()
}

func (s *Store) FailGitHubImport(ctx context.Context, id, lease, reason string) error {
	if len(lease) != 64 || (reason != GitHubImportErrorSourceUnavailable && reason != GitHubImportErrorArchiveInvalid && reason != GitHubImportErrorUnavailable) {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	job, err := scanGitHubImport(tx.QueryRowContext(ctx, "SELECT "+githubImportColumns+" FROM github_imports WHERE id=? AND state='running' AND lease_hash=? AND lease_until>?", id, digest(lease), s.now().Unix()))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrGitHubImportLease
	}
	if err != nil {
		return err
	}
	var workspaceID string
	if err = tx.QueryRowContext(ctx, "SELECT workspace_id FROM projects WHERE id=?", job.ProjectID).Scan(&workspaceID); err != nil {
		return err
	}
	now := s.now().Unix()
	result, err := tx.ExecContext(ctx, "UPDATE github_imports SET state='failed',error=?,lease_hash='',lease_until=0 WHERE id=? AND state='running' AND lease_hash=? AND lease_until>?", reason, id, digest(lease), now)
	if err != nil {
		return err
	}
	if n, e := result.RowsAffected(); e != nil {
		return e
	} else if n != 1 {
		return ErrGitHubImportLease
	}
	if err = audit(ctx, tx, job.ActorID, workspaceID, "project.github-import.failed:"+id, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GitHubImports(ctx context.Context, session, project string) ([]GitHubImport, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, _, err = s.projectClientOwner(ctx, tx, session, project); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT "+githubImportColumns+" FROM github_imports WHERE project_id=? ORDER BY created_at DESC,id DESC LIMIT 20", project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []GitHubImport{}
	for rows.Next() {
		job, scanErr := scanGitHubImport(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, job)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return result, tx.Commit()
}
