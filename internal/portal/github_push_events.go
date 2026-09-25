package portal

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/githubdeploy"
)

const githubPushHistoryLimit = 10000

var ErrGitHubPushCapacity = errors.New("GitHub push inbox capacity reached")

type GitHubPushEvent struct {
	ID                 string `json:"id"`
	ProjectID          string `json:"project_id"`
	ConnectionRevision int64  `json:"connection_revision"`
	ActorID            string `json:"-"`
	InstallationID     int64  `json:"installation_id"`
	RepositoryID       int64  `json:"repository_id"`
	Repository         string `json:"repository"`
	Ref                string `json:"ref"`
	Before             string `json:"before"`
	After              string `json:"after"`
	Deleted            bool   `json:"deleted"`
	State              string `json:"state"`
	CreatedAt          int64  `json:"created_at"`
}

func (s *Store) migrateGitHubPushEvents() error {
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err = tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version >= 50 {
		return nil
	}
	if version != 49 {
		return errors.New("GitHub push migration requires portal schema 49")
	}
	_, err = tx.Exec(`CREATE TABLE IF NOT EXISTS github_push_receipts(
 payload_hash TEXT PRIMARY KEY, created_at INTEGER NOT NULL);
 CREATE TABLE IF NOT EXISTS github_push_events(
 id TEXT PRIMARY KEY,
 payload_hash TEXT NOT NULL REFERENCES github_push_receipts(payload_hash) ON DELETE CASCADE,
 project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
 connection_revision INTEGER NOT NULL,
 actor_id TEXT NOT NULL REFERENCES users(id),
 installation_id INTEGER NOT NULL,
 repository_id INTEGER NOT NULL,
 repository TEXT NOT NULL,
 ref TEXT NOT NULL,
 before_sha TEXT NOT NULL,
 after_sha TEXT NOT NULL,
 deleted INTEGER NOT NULL CHECK(deleted IN (0,1)),
 state TEXT NOT NULL CHECK(state='pending'),
 created_at INTEGER NOT NULL,
 UNIQUE(project_id,connection_revision,ref,before_sha,after_sha,deleted));
 CREATE INDEX IF NOT EXISTS github_push_events_payload ON github_push_events(payload_hash);
 CREATE INDEX IF NOT EXISTS github_push_events_project ON github_push_events(project_id,created_at,id);
 PRAGMA user_version=50;`)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func validGitHubPushHash(v string) bool {
	if len(v) != 64 {
		return false
	}
	b, err := hex.DecodeString(v)
	return err == nil && len(b) == 32 && strings.ToLower(v) == v
}

const githubPushColumns = "e.id,e.project_id,e.connection_revision,e.actor_id,e.installation_id,e.repository_id,e.repository,e.ref,e.before_sha,e.after_sha,e.deleted,e.state,e.created_at"

func scanGitHubPushEvent(row interface{ Scan(...any) error }) (GitHubPushEvent, error) {
	var e GitHubPushEvent
	err := row.Scan(&e.ID, &e.ProjectID, &e.ConnectionRevision, &e.ActorID, &e.InstallationID, &e.RepositoryID, &e.Repository, &e.Ref, &e.Before, &e.After, &e.Deleted, &e.State, &e.CreatedAt)
	return e, err
}

// AcceptGitHubPush stores a verified push only when it matches a current,
// connected deploy-on-push binding. Signature verification happens upstream.
func (s *Store) AcceptGitHubPush(ctx context.Context, push githubdeploy.Push, payloadHash string) error {
	if !validGitHubPushHash(payloadHash) {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var existing string
	err = tx.QueryRowContext(ctx, "SELECT payload_hash FROM github_push_receipts WHERE payload_hash=?", payloadHash).Scan(&existing)
	if err == nil {
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT c.project_id,c.revision,c.actor_id,c.installation_id,c.repository_id,c.repository,c.branch,p.workspace_id,p.kind
 FROM github_connections c JOIN projects p ON p.id=c.project_id
 JOIN users u ON u.id=c.actor_id AND u.verified=1 AND u.disabled=0
 JOIN memberships m ON m.user_id=u.id AND m.workspace_id=p.workspace_id AND m.role='owner'
 WHERE c.connected=1 AND c.deploy_on_push=1 AND c.installation_id=? AND c.repository_id=? AND c.repository=? AND ?='refs/heads/'||c.branch AND p.deletion_requested_at=0`, push.InstallationID, push.RepositoryID, push.RepositoryFullName, push.Ref)
	if err != nil {
		return err
	}
	defer rows.Close()
	type binding struct {
		projectID, actorID, repository, workspaceID, kind string
		revision, installationID, repositoryID            int64
	}
	bindings := []binding{}
	for rows.Next() {
		var projectID, actorID, repository, branch, workspaceID, kind string
		var revision, installationID, repositoryID int64
		if err = rows.Scan(&projectID, &revision, &actorID, &installationID, &repositoryID, &repository, &branch, &workspaceID, &kind); err != nil {
			return err
		}
		if kind != "static" && kind != "node" {
			continue
		}
		if len(bindings) >= 1000 {
			return ErrGitHubPushCapacity
		}
		bindings = append(bindings, binding{projectID, actorID, repository, workspaceID, kind, revision, installationID, repositoryID})
	}
	if err = rows.Close(); err != nil {
		return err
	}
	if err = rows.Err(); err != nil {
		return err
	}
	authorized := bindings[:0]
	for _, b := range bindings {
		if err = s.requireHostingKind(ctx, tx, b.workspaceID, b.kind); err != nil {
			if errors.Is(err, ErrDenied) || errors.Is(err, ErrInvalid) || errors.Is(err, ErrHostingPayment) || errors.Is(err, ErrHostingPlanLimit) {
				continue
			}
			return err
		}
		authorized = append(authorized, b)
	}
	bindings = authorized
	if len(bindings) == 0 {
		return tx.Commit()
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO github_push_receipts(payload_hash,created_at) VALUES(?,?)", payloadHash, s.now().Unix()); err != nil {
		return err
	}
	var receiptCount int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM github_push_receipts").Scan(&receiptCount); err != nil {
		return err
	}
	if receiptCount > githubPushHistoryLimit {
		return ErrGitHubPushCapacity
	}
	for _, b := range bindings {
		var count int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM github_push_events WHERE project_id=?", b.projectID).Scan(&count); err != nil {
			return err
		}
		if count >= githubPushHistoryLimit {
			return ErrGitHubPushCapacity
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO github_push_events(id,payload_hash,project_id,connection_revision,actor_id,installation_id,repository_id,repository,ref,before_sha,after_sha,deleted,state,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, randomToken(), payloadHash, b.projectID, b.revision, b.actorID, b.installationID, b.repositoryID, b.repository, push.Ref, push.Before, push.After, push.Deleted, "pending", s.now().Unix())
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}
