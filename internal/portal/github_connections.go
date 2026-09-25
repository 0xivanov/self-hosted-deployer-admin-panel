package portal

import (
	"context"
	"database/sql"
	"errors"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/githubdeploy"
)

// GitHubConnection stores provenance, never provider or portal credentials.
// Revision changes on replacement/disconnect and must be checked by import workers.
type GitHubConnection struct {
	ProjectID      string `json:"project_id"`
	Revision       int64  `json:"revision"`
	ActorID        string `json:"actor_id"`
	GitHubUserID   int64  `json:"github_user_id"`
	GitHubLogin    string `json:"github_login"`
	InstallationID int64  `json:"installation_id"`
	RepositoryID   int64  `json:"repository_id"`
	Repository     string `json:"repository"`
	Branch         string `json:"branch"`
	Directory      string `json:"directory"`
	DeployOnPush   bool   `json:"deploy_on_push"`
	Connected      bool   `json:"connected"`
	UpdatedAt      int64  `json:"updated_at"`
}

func (s *Store) migrateGitHubConnections() error {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err = tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version >= 48 {
		return nil
	}
	if version != 47 {
		return errors.New("GitHub connections require portal schema 47")
	}
	_, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS github_link_completions(
 project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
 proof_hash TEXT NOT NULL UNIQUE, actor_id TEXT NOT NULL REFERENCES users(id),
 session_hash TEXT NOT NULL, expires_at INTEGER NOT NULL);
 CREATE TABLE IF NOT EXISTS github_connections(
 project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
 revision INTEGER NOT NULL, actor_id TEXT NOT NULL REFERENCES users(id),
 github_user_id INTEGER NOT NULL, github_login TEXT NOT NULL,
 installation_id INTEGER NOT NULL, repository_id INTEGER NOT NULL,
 repository TEXT NOT NULL, branch TEXT NOT NULL, directory TEXT NOT NULL,
 deploy_on_push INTEGER NOT NULL, connected INTEGER NOT NULL, updated_at INTEGER NOT NULL);
 PRAGMA user_version=48;`)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// SaveGitHubConnection must receive access verified by the provider client, not
// decoded customer input. The receipt is issued only after OAuth state consumption.
// All local authorization is repeated after external provider requests finish.
func (s *Store) SaveGitHubConnection(ctx context.Context, session string, attempt GitHubLinkAttempt, access githubdeploy.RepositoryAccess, branch, directory string, deployOnPush bool) (GitHubConnection, error) {
	var empty GitHubConnection
	if !githubdeploy.ValidSelection(access, branch, directory) || attempt.completion == "" {
		return empty, ErrInvalid
	}
	if directory == "." {
		directory = ""
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	p, actor, err := s.projectClientOwner(ctx, tx, session, attempt.ProjectID)
	if err != nil {
		return empty, err
	}
	if actor != attempt.ActorID || p.WorkspaceID != attempt.WorkspaceID {
		return empty, ErrDenied
	}
	if p.Kind != "static" && p.Kind != "node" {
		return empty, ErrInvalid
	}
	if err = s.requireHostingKind(ctx, tx, p.WorkspaceID, p.Kind); err != nil {
		return empty, err
	}
	now := s.now().Unix()
	result, err := tx.ExecContext(ctx, `DELETE FROM github_link_completions WHERE project_id=? AND proof_hash=? AND actor_id=? AND session_hash=? AND expires_at>?`, p.ID, digest(attempt.completion), actor, digest(session), now)
	if err != nil {
		return empty, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return empty, err
	}
	if count != 1 {
		return empty, ErrGitHubLink
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO github_connections VALUES(?,1,?,?,?,?,?,?,?,?,?,?,?)
 ON CONFLICT(project_id) DO UPDATE SET revision=github_connections.revision+1,actor_id=excluded.actor_id,github_user_id=excluded.github_user_id,github_login=excluded.github_login,installation_id=excluded.installation_id,repository_id=excluded.repository_id,repository=excluded.repository,branch=excluded.branch,directory=excluded.directory,deploy_on_push=excluded.deploy_on_push,connected=1,updated_at=excluded.updated_at`, p.ID, actor, access.UserID, access.Login, access.InstallationID, access.RepositoryID, access.RepositoryFullName, branch, directory, deployOnPush, true, now)
	if err != nil {
		return empty, err
	}
	connection, err := readGitHubConnection(ctx, tx, p.ID)
	if err != nil {
		return empty, err
	}
	if err = audit(ctx, tx, actor, p.WorkspaceID, "project.github-connected:"+p.ID, now); err != nil {
		return empty, err
	}
	if err = tx.Commit(); err != nil {
		return empty, err
	}
	return connection, nil
}

func readGitHubConnection(ctx context.Context, tx *sql.Tx, project string) (GitHubConnection, error) {
	var c GitHubConnection
	err := tx.QueryRowContext(ctx, `SELECT project_id,revision,actor_id,github_user_id,github_login,installation_id,repository_id,repository,branch,directory,deploy_on_push,connected,updated_at FROM github_connections WHERE project_id=?`, project).Scan(&c.ProjectID, &c.Revision, &c.ActorID, &c.GitHubUserID, &c.GitHubLogin, &c.InstallationID, &c.RepositoryID, &c.Repository, &c.Branch, &c.Directory, &c.DeployOnPush, &c.Connected, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return GitHubConnection{ProjectID: project}, nil
	}
	return c, err
}

func (s *Store) ProjectGitHubConnection(ctx context.Context, session, project string) (GitHubConnection, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return GitHubConnection{}, err
	}
	defer tx.Rollback()
	if _, _, err = s.projectClientOwner(ctx, tx, session, project); err != nil {
		return GitHubConnection{}, err
	}
	return readGitHubConnection(ctx, tx, project)
}

// Disconnect invalidates both unfinished callbacks and future imports, without
// deleting uploads, releases or the last published site. Keep the revision tombstone.
func (s *Store) DisconnectGitHub(ctx context.Context, session, project string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, actor, err := s.projectClientOwner(ctx, tx, session, project)
	if err != nil {
		return err
	}
	for _, table := range []string{"github_link_states", "github_link_completions"} {
		if _, err = tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE project_id=?", project); err != nil {
			return err
		}
	}
	now := s.now().Unix()
	if _, err = tx.ExecContext(ctx, "UPDATE github_connections SET connected=0,deploy_on_push=0,revision=revision+1,updated_at=? WHERE project_id=? AND connected=1", now, project); err != nil {
		return err
	}
	if err = audit(ctx, tx, actor, p.WorkspaceID, "project.github-disconnected:"+project, now); err != nil {
		return err
	}
	return tx.Commit()
}

// githubCompletionCurrent keeps a late OAuth response from replacing a newer
// browser selection. SaveGitHubConnection still repeats authorization at commit.
func (s *Store) githubCompletionCurrent(ctx context.Context, session string, attempt GitHubLinkAttempt) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, actor, err := s.projectClientOwner(ctx, tx, session, attempt.ProjectID)
	if err != nil {
		return err
	}
	if actor != attempt.ActorID || p.WorkspaceID != attempt.WorkspaceID {
		return ErrDenied
	}
	var count int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM github_link_completions WHERE project_id=? AND proof_hash=? AND actor_id=? AND session_hash=? AND expires_at>?`, p.ID, digest(attempt.completion), actor, digest(session), s.now().Unix()).Scan(&count)
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrGitHubLink
	}
	return nil
}
