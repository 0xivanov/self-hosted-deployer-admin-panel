package portal

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var ErrGitHubLink = errors.New("GitHub connection request expired or is no longer valid; start connecting again")
var ErrGitHubLinkRate = errors.New("wait a few seconds before starting another GitHub connection")

type GitHubLinkState struct {
	State     string `json:"-"`
	ExpiresAt int64  `json:"expires_at"`
}

func (GitHubLinkState) String() string   { return "[GitHub link state redacted]" }
func (GitHubLinkState) GoString() string { return "[GitHub link state redacted]" }

type GitHubLinkAttempt struct {
	ProjectID   string
	WorkspaceID string
	ActorID     string
	completion  string
}

func (GitHubLinkAttempt) String() string   { return "[GitHub link completion redacted]" }
func (GitHubLinkAttempt) GoString() string { return "[GitHub link completion redacted]" }

func (s *Store) migrateGitHubLinks() error {
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err = tx.QueryRowContext(context.Background(), "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version >= 47 {
		return nil
	}
	if version != 46 {
		return errors.New("GitHub link migration requires portal schema 46")
	}
	_, err = tx.ExecContext(context.Background(), `CREATE TABLE IF NOT EXISTS github_link_states(
 project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
 state_hash TEXT NOT NULL UNIQUE,
 actor_id TEXT NOT NULL REFERENCES users(id),
 session_hash TEXT NOT NULL,
 expires_at INTEGER NOT NULL,
 created_at INTEGER NOT NULL);
 PRAGMA user_version=47;`)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// BeginGitHubLink authorizes the owner before creating OAuth state. The HTTP
// caller must additionally enforce its ordinary CSRF and feature-availability
// checks. State is hashed at rest and supersedes an older attempt for this project.
func (s *Store) BeginGitHubLink(ctx context.Context, session, project string) (GitHubLinkState, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return GitHubLinkState{}, err
	}
	defer tx.Rollback()
	p, actor, err := s.projectClientOwner(ctx, tx, session, project)
	if err != nil {
		return GitHubLinkState{}, err
	}
	if p.Kind != "static" && p.Kind != "node" {
		return GitHubLinkState{}, ErrInvalid
	}
	if err = s.requireHostingKind(ctx, tx, p.WorkspaceID, p.Kind); err != nil {
		return GitHubLinkState{}, err
	}
	now := s.now().Unix()
	var created int64
	err = tx.QueryRowContext(ctx, "SELECT created_at FROM github_link_states WHERE project_id=?", project).Scan(&created)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return GitHubLinkState{}, err
	}
	if err == nil && created > now-10 {
		return GitHubLinkState{}, ErrGitHubLinkRate
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM github_link_completions WHERE project_id=?", project); err != nil {
		return GitHubLinkState{}, err
	}
	result := GitHubLinkState{State: randomToken(), ExpiresAt: now + int64(10*time.Minute/time.Second)}
	_, err = tx.ExecContext(ctx, `INSERT INTO github_link_states(project_id,state_hash,actor_id,session_hash,expires_at,created_at) VALUES(?,?,?,?,?,?)
 ON CONFLICT(project_id) DO UPDATE SET state_hash=excluded.state_hash,actor_id=excluded.actor_id,session_hash=excluded.session_hash,expires_at=excluded.expires_at,created_at=excluded.created_at`, project, digest(result.State), actor, digest(session), result.ExpiresAt, now)
	if err != nil {
		return GitHubLinkState{}, err
	}
	if err = audit(ctx, tx, actor, p.WorkspaceID, "project.github-link-started:"+project, now); err != nil {
		return GitHubLinkState{}, err
	}
	if err = tx.Commit(); err != nil {
		return GitHubLinkState{}, err
	}
	return result, nil
}

// ConsumeGitHubLink is called before exchanging the OAuth code. It atomically
// consumes state only for the initiating session/actor/project and checks current
// owner and hosting access. Provider failures require a new connection attempt.
// This receipt is not repository authorization: verify GitHub access separately
// and recheck current portal authority when persisting the eventual connection.
func (s *Store) ConsumeGitHubLink(ctx context.Context, session, project, state string) (GitHubLinkAttempt, error) {
	empty := GitHubLinkAttempt{}
	if len(state) != 64 {
		return empty, ErrGitHubLink
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	p, actor, err := s.projectClientOwner(ctx, tx, session, project)
	if err != nil {
		return empty, err
	}
	if p.Kind != "static" && p.Kind != "node" {
		return empty, ErrInvalid
	}
	if err = s.requireHostingKind(ctx, tx, p.WorkspaceID, p.Kind); err != nil {
		return empty, err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM github_link_states WHERE project_id=? AND state_hash=? AND actor_id=? AND session_hash=? AND expires_at>?`, project, digest(state), actor, digest(session), s.now().Unix())
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
	completion := randomToken()
	_, err = tx.ExecContext(ctx, `INSERT INTO github_link_completions(project_id,proof_hash,actor_id,session_hash,expires_at) VALUES(?,?,?,?,?) ON CONFLICT(project_id) DO UPDATE SET proof_hash=excluded.proof_hash,actor_id=excluded.actor_id,session_hash=excluded.session_hash,expires_at=excluded.expires_at`, project, digest(completion), actor, digest(session), s.now().Unix()+600)
	if err != nil {
		return empty, err
	}
	if err = tx.Commit(); err != nil {
		return empty, err
	}
	return GitHubLinkAttempt{ProjectID: project, WorkspaceID: p.WorkspaceID, ActorID: actor, completion: completion}, nil
}
