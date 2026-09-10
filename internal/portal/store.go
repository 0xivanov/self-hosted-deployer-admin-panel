// Package portal owns customer identities and workspace data, independently of
// the operator console and the deployer control-plane database.
package portal

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
	_ "modernc.org/sqlite"
)

var (
	ErrDenied      = errors.New("access denied")
	ErrInvalid     = errors.New("invalid input")
	ErrCredentials = errors.New("invalid credentials or unverified account")
	ErrExists      = errors.New("account already exists")
)

type Store struct {
	db     *sql.DB
	now    func() time.Time
	hashes chan struct{}
}
type Account struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	WorkspaceID string `json:"workspace_id,omitempty"`
}
type Session struct {
	Token     string    `json:"-"`
	Account   Account   `json:"account"`
	ExpiresAt time.Time `json:"expires_at"`
}
type Project struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
}

// Open requires a private directory because SQLite can create journal files.
// The schema version is checked before any migration; newer schemas fail closed.
func Open(path string) (*Store, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	directory := filepath.Dir(absolute)
	if err = os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("portal database directory must be private (0700)")
	}
	file, err := os.OpenFile(absolute, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err == nil {
		if err = file.Close(); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	info, err = os.Lstat(absolute)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("portal database must be a private regular file (0600)")
	}
	u := url.URL{Scheme: "file", Path: absolute}
	q := url.Values{}
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "busy_timeout(5000)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, now: time.Now, hashes: make(chan struct{}, 2)}
	if err = s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}
func (s *Store) Close() error { return s.db.Close() }
func (s *Store) migrate() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err = tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > 18 {
		return errors.New("portal database schema is newer than this binary")
	}
	if version == 0 {
		_, err = tx.Exec(`
CREATE TABLE users(id TEXT PRIMARY KEY,email TEXT NOT NULL UNIQUE,salt BLOB NOT NULL,password_hash BLOB NOT NULL,verified INTEGER NOT NULL DEFAULT 0,disabled INTEGER NOT NULL DEFAULT 0,created_at INTEGER NOT NULL);
CREATE TABLE workspaces(id TEXT PRIMARY KEY,name TEXT NOT NULL);
CREATE TABLE memberships(user_id TEXT NOT NULL REFERENCES users(id),workspace_id TEXT NOT NULL REFERENCES workspaces(id),role TEXT NOT NULL CHECK(role IN ('owner','developer','viewer')),PRIMARY KEY(user_id,workspace_id));
CREATE TABLE sessions(token_hash TEXT PRIMARY KEY,user_id TEXT NOT NULL REFERENCES users(id),expires_at INTEGER NOT NULL);
CREATE INDEX sessions_user ON sessions(user_id);
CREATE TABLE account_tokens(token_hash TEXT PRIMARY KEY,user_id TEXT NOT NULL REFERENCES users(id),purpose TEXT NOT NULL CHECK(purpose IN ('verify','reset')),expires_at INTEGER NOT NULL);
CREATE TABLE projects(id TEXT PRIMARY KEY,workspace_id TEXT NOT NULL REFERENCES workspaces(id),name TEXT NOT NULL,kind TEXT NOT NULL CHECK(kind IN ('static','node')),UNIQUE(workspace_id,name));
CREATE TABLE audit_events(id TEXT PRIMARY KEY,actor_id TEXT NOT NULL,workspace_id TEXT NOT NULL DEFAULT '',action TEXT NOT NULL,created_at INTEGER NOT NULL);
PRAGMA user_version=1;`)
		if err != nil {
			return err
		}
	}
	if version < 2 {
		if _, err = tx.Exec(`CREATE TABLE mail_outbox(id TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id), token_hash TEXT NOT NULL, purpose TEXT NOT NULL, payload BLOB NOT NULL, state TEXT NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','sent','discarded','failed')), attempts INTEGER NOT NULL DEFAULT 0, next_attempt INTEGER NOT NULL, lease_id TEXT NOT NULL DEFAULT '', lease_until INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL); CREATE INDEX mail_outbox_due ON mail_outbox(state,next_attempt,lease_until); PRAGMA user_version=2;`); err != nil {
			return err
		}
	}
	if version < 3 {
		if _, err = tx.Exec(`CREATE TABLE invitations(id TEXT PRIMARY KEY,token_hash TEXT NOT NULL UNIQUE,workspace_id TEXT NOT NULL REFERENCES workspaces(id),email TEXT NOT NULL,role TEXT NOT NULL CHECK(role IN ('owner','developer','viewer')),inviter_id TEXT NOT NULL REFERENCES users(id),expires_at INTEGER NOT NULL,state TEXT NOT NULL CHECK(state IN ('pending','accepted','revoked')),created_at INTEGER NOT NULL); CREATE INDEX invitations_workspace ON invitations(workspace_id,state); PRAGMA user_version=3;`); err != nil {
			return err
		}
	}
	if version < 4 {
		if _, err = tx.Exec(`CREATE TABLE uploads(id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id), sha256 TEXT NOT NULL, files INTEGER NOT NULL, expanded_bytes INTEGER NOT NULL, archive BLOB NOT NULL, created_at INTEGER NOT NULL); CREATE INDEX uploads_project ON uploads(project_id,created_at); PRAGMA user_version=4;`); err != nil {
			return err
		}
	}
	if version < 5 {
		if _, err = tx.Exec(`CREATE TABLE publication_jobs(id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id), upload_id TEXT NOT NULL REFERENCES uploads(id), actor_id TEXT NOT NULL REFERENCES users(id), request_key TEXT NOT NULL, revision INTEGER NOT NULL, state TEXT NOT NULL CHECK(state IN ('queued','running','succeeded','failed')), lease_hash TEXT NOT NULL DEFAULT '', lease_until INTEGER NOT NULL DEFAULT 0, attempts INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL, UNIQUE(project_id,request_key), UNIQUE(project_id,revision)); CREATE UNIQUE INDEX publication_pending ON publication_jobs(project_id) WHERE state IN ('queued','running'); CREATE TABLE publications(project_id TEXT PRIMARY KEY REFERENCES projects(id), job_id TEXT NOT NULL REFERENCES publication_jobs(id)); PRAGMA user_version=5;`); err != nil {
			return err
		}
	}
	if version < 6 {
		if _, err = tx.Exec(`CREATE TABLE billing_events(id TEXT PRIMARY KEY,event_type TEXT NOT NULL,provider_created INTEGER NOT NULL,fingerprint TEXT NOT NULL,payload BLOB NOT NULL,state TEXT NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','processed','ignored')),received_at INTEGER NOT NULL); CREATE INDEX billing_events_pending ON billing_events(state,received_at); PRAGMA user_version=6;`); err != nil {
			return err
		}
	}
	if version < 7 {
		if _, err = tx.Exec(`CREATE TABLE billing_customers(workspace_id TEXT PRIMARY KEY REFERENCES workspaces(id),request_id TEXT NOT NULL UNIQUE,actor_id TEXT NOT NULL REFERENCES users(id),email TEXT NOT NULL,customer_id TEXT UNIQUE,created_at INTEGER NOT NULL); PRAGMA user_version=7;`); err != nil {
			return err
		}
	}
	if version < 8 {
		if _, err = tx.Exec(`CREATE TABLE billing_plans(id TEXT PRIMARY KEY,price_id TEXT NOT NULL,enabled INTEGER NOT NULL CHECK(enabled IN (0,1))); CREATE TABLE billing_checkouts(id TEXT PRIMARY KEY,workspace_id TEXT NOT NULL REFERENCES workspaces(id),actor_id TEXT NOT NULL REFERENCES users(id),customer_id TEXT NOT NULL REFERENCES billing_customers(customer_id),plan_id TEXT NOT NULL REFERENCES billing_plans(id),price_id TEXT NOT NULL,session_id TEXT UNIQUE,checkout_url TEXT NOT NULL DEFAULT '',state TEXT NOT NULL CHECK(state IN ('pending','open','completed','expired')),created_at INTEGER NOT NULL); CREATE UNIQUE INDEX billing_checkout_active ON billing_checkouts(workspace_id) WHERE state IN ('pending','open','completed'); PRAGMA user_version=8;`); err != nil {
			return err
		}
	}
	if version < 9 {
		if _, err = tx.Exec(`CREATE TABLE billing_subscriptions(id TEXT PRIMARY KEY,checkout_id TEXT NOT NULL UNIQUE REFERENCES billing_checkouts(id),workspace_id TEXT NOT NULL REFERENCES workspaces(id),customer_id TEXT NOT NULL REFERENCES billing_customers(customer_id),plan_id TEXT NOT NULL REFERENCES billing_plans(id),price_id TEXT NOT NULL,state TEXT NOT NULL DEFAULT 'awaiting_reconciliation',first_event TEXT NOT NULL REFERENCES billing_events(id)); PRAGMA user_version=9;`); err != nil {
			return err
		}
	}
	if version < 10 {
		if _, err = tx.Exec(`ALTER TABLE billing_subscriptions ADD COLUMN reconciliation_generation INTEGER NOT NULL DEFAULT 0; ALTER TABLE billing_subscriptions ADD COLUMN snapshot BLOB; PRAGMA user_version=10;`); err != nil {
			return err
		}
	}
	if version < 11 {
		if _, err = tx.Exec(`CREATE TABLE billing_work(kind TEXT NOT NULL CHECK(kind IN ('customer','checkout','event','subscription')),reference TEXT NOT NULL,next_attempt INTEGER NOT NULL DEFAULT 0,attempts INTEGER NOT NULL DEFAULT 0,lease_hash TEXT NOT NULL DEFAULT '',lease_until INTEGER NOT NULL DEFAULT 0,done INTEGER NOT NULL DEFAULT 0 CHECK(done IN (0,1)),PRIMARY KEY(kind,reference)); CREATE INDEX billing_work_due ON billing_work(done,next_attempt,lease_until); PRAGMA user_version=11;`); err != nil {
			return err
		}
	}
	if version < 12 {
		if _, err = tx.Exec(`ALTER TABLE billing_plans ADD COLUMN price_snapshot BLOB; ALTER TABLE billing_plans ADD COLUMN price_observed INTEGER NOT NULL DEFAULT 0; ALTER TABLE billing_plans ADD COLUMN next_refresh INTEGER NOT NULL DEFAULT 0; ALTER TABLE billing_plans ADD COLUMN price_generation INTEGER NOT NULL DEFAULT 0; PRAGMA user_version=12;`); err != nil {
			return err
		}
	}
	if version < 13 {
		if _, err = tx.Exec(`CREATE TABLE billing_charges(id TEXT PRIMARY KEY,generation INTEGER NOT NULL DEFAULT 0,subscription_id TEXT REFERENCES billing_subscriptions(id),customer_id TEXT REFERENCES billing_customers(customer_id),invoice_id TEXT NOT NULL DEFAULT '',payment_intent_id TEXT NOT NULL DEFAULT '',snapshot BLOB); CREATE INDEX billing_charges_subscription ON billing_charges(subscription_id); PRAGMA user_version=13;`); err != nil {
			return err
		}
	}
	if version < 14 {
		if _, err = tx.Exec(`ALTER TABLE billing_charges ADD COLUMN next_refresh INTEGER NOT NULL DEFAULT 0; CREATE INDEX billing_charges_refresh ON billing_charges(next_refresh); PRAGMA user_version=14;`); err != nil {
			return err
		}
	}
	if version < 15 {
		if _, err = tx.Exec(`CREATE TABLE domain_quotes(id TEXT PRIMARY KEY,workspace_id TEXT NOT NULL REFERENCES workspaces(id),actor_id TEXT NOT NULL REFERENCES users(id),evidence BLOB NOT NULL,offer BLOB NOT NULL,created_at INTEGER NOT NULL); CREATE INDEX domain_quotes_workspace ON domain_quotes(workspace_id); PRAGMA user_version=15;`); err != nil {
			return err
		}
	}

	if version < 16 {
		if _, err = tx.Exec(`CREATE TABLE node_builds(id TEXT PRIMARY KEY,project_id TEXT NOT NULL REFERENCES projects(id),upload_id TEXT NOT NULL REFERENCES uploads(id),actor_id TEXT NOT NULL REFERENCES users(id),request_key TEXT NOT NULL,plan BLOB NOT NULL,toolchain_sha256 TEXT NOT NULL,state TEXT NOT NULL CHECK(state IN ('queued','running','succeeded','failed','cancelled')),created_at INTEGER NOT NULL,UNIQUE(project_id,request_key)); CREATE UNIQUE INDEX node_build_pending ON node_builds(project_id) WHERE state IN ('queued','running'); CREATE INDEX node_build_upload ON node_builds(upload_id); PRAGMA user_version=16;`); err != nil {
			return err
		}
	}

	if version < 17 {
		if _, err = tx.Exec(`ALTER TABLE node_builds ADD COLUMN execution_id TEXT NOT NULL DEFAULT ''; ALTER TABLE node_builds ADD COLUMN lease_hash TEXT NOT NULL DEFAULT ''; ALTER TABLE node_builds ADD COLUMN lease_until INTEGER NOT NULL DEFAULT 0; CREATE UNIQUE INDEX node_build_execution ON node_builds(execution_id) WHERE execution_id<>''; PRAGMA user_version=17;`); err != nil {
			return err
		}
	}

	if version < 18 {
		if _, err = tx.Exec(`ALTER TABLE node_builds ADD COLUMN result BLOB; PRAGMA user_version=18;`); err != nil {
			return err
		}
	}

	return tx.Commit()
}
func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func digest(raw string) string { sum := sha256.Sum256([]byte(raw)); return hex.EncodeToString(sum[:]) }
func normalizeEmail(raw string) (string, error) {
	v := strings.ToLower(strings.TrimSpace(raw))
	a, e := mail.ParseAddress(v)
	if e != nil || a.Address != v || len(v) > 254 || !strings.Contains(v, "@") {
		return "", ErrInvalid
	}
	return v, nil
}
func validPassword(v string) bool { return len(v) >= 12 && len(v) <= 1024 }
func (s *Store) hash(ctx context.Context, password string, salt []byte) ([]byte, error) {
	select {
	case s.hashes <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-s.hashes }()
	return argon2.IDKey([]byte(password), salt, 2, 19*1024, 1, 32), nil
}
func audit(ctx context.Context, tx *sql.Tx, actor, workspace, action string, now int64) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO audit_events VALUES(?,?,?,?,?)", randomToken(), actor, workspace, action, now)
	return err
}

// Register creates an unverified customer and private workspace atomically.
// The returned verification token is for a trusted mail outbox, never a signup response.
// No administrator role can be requested here.
func (s *Store) Register(ctx context.Context, email, password, workspace string) (Account, string, error) {
	return s.register(ctx, email, password, workspace, nil)
}

type enqueueAccountMail func(context.Context, *sql.Tx, string, string, string, string) error

func (s *Store) register(ctx context.Context, email, password, workspace string, queue enqueueAccountMail) (Account, string, error) {
	email, err := normalizeEmail(email)
	if err != nil || !validPassword(password) || len(strings.TrimSpace(workspace)) == 0 || len(workspace) > 100 {
		return Account{}, "", ErrInvalid
	}
	salt := make([]byte, 16)
	if _, err = rand.Read(salt); err != nil {
		return Account{}, "", err
	}
	hash, err := s.hash(ctx, password, salt)
	if err != nil {
		return Account{}, "", err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Account{}, "", err
	}
	defer tx.Rollback()
	var existing string
	err = tx.QueryRowContext(ctx, "SELECT id FROM users WHERE email=?", email).Scan(&existing)
	if err == nil {
		return Account{}, "", ErrExists
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Account{}, "", err
	}
	a := Account{ID: randomToken(), Email: email, WorkspaceID: randomToken()}
	now := s.now().Unix()
	token := randomToken()
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{"INSERT INTO users(id,email,salt,password_hash,created_at) VALUES(?,?,?,?,?)", []any{a.ID, email, salt, hash, now}},
		{"INSERT INTO workspaces VALUES(?,?)", []any{a.WorkspaceID, strings.TrimSpace(workspace)}},
		{"INSERT INTO memberships VALUES(?,?,'owner')", []any{a.ID, a.WorkspaceID}},
		{"INSERT INTO account_tokens VALUES(?,?,'verify',?)", []any{digest(token), a.ID, now + 86400}},
	} {
		if _, err = tx.ExecContext(ctx, statement.sql, statement.args...); err != nil {
			return Account{}, "", err
		}
	}
	if queue != nil {
		if err = queue(ctx, tx, a.ID, email, token, "verify"); err != nil {
			return Account{}, "", err
		}
	}
	if err = audit(ctx, tx, a.ID, a.WorkspaceID, "account.registered", now); err != nil {
		return Account{}, "", err
	}
	if err = tx.Commit(); err != nil {
		return Account{}, "", err
	}
	return a, token, nil
}
func (s *Store) Verify(ctx context.Context, token string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id string
	err = tx.QueryRowContext(ctx, "SELECT user_id FROM account_tokens WHERE token_hash=? AND purpose='verify' AND expires_at>?", digest(token), s.now().Unix()).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDenied
	}
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE users SET verified=1 WHERE id=? AND disabled=0", id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM account_tokens WHERE user_id=? AND purpose='verify'", id); err != nil {
		return err
	}
	if err = audit(ctx, tx, id, "", "account.verified", s.now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) Login(ctx context.Context, email, password string) (Session, error) {
	email, err := normalizeEmail(email)
	if err != nil || len(password) > 1024 {
		return Session{}, ErrCredentials
	}
	var a Account
	var salt, expected []byte
	var verified, disabled int
	err = s.db.QueryRowContext(ctx, "SELECT id,email,salt,password_hash,verified,disabled FROM users WHERE email=?", email).Scan(&a.ID, &a.Email, &salt, &expected, &verified, &disabled)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Session{}, err
	}
	missing := errors.Is(err, sql.ErrNoRows)
	if missing {
		salt = make([]byte, 16)
		expected = make([]byte, 32)
	}
	actual, err := s.hash(ctx, password, salt)
	if err != nil {
		return Session{}, err
	}
	if subtle.ConstantTimeCompare(actual, expected) != 1 || missing || verified != 1 || disabled != 0 {
		return Session{}, ErrCredentials
	}
	// Recheck credential/account state inside the session transaction: a concurrent
	// password reset or disable must not allow an old-password login to finish.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Session{}, err
	}
	defer tx.Rollback()
	var current []byte
	err = tx.QueryRowContext(ctx, "SELECT password_hash FROM users WHERE id=? AND verified=1 AND disabled=0", a.ID).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrCredentials
	}
	if err != nil {
		return Session{}, err
	}
	if subtle.ConstantTimeCompare(current, expected) != 1 {
		return Session{}, ErrCredentials
	}
	session := Session{Token: randomToken(), Account: a, ExpiresAt: s.now().Add(24 * time.Hour)}
	if _, err = tx.ExecContext(ctx, "INSERT INTO sessions VALUES(?,?,?)", digest(session.Token), a.ID, session.ExpiresAt.Unix()); err != nil {
		return Session{}, err
	}
	if err = audit(ctx, tx, a.ID, "", "session.created", s.now().Unix()); err != nil {
		return Session{}, err
	}
	return session, tx.Commit()
}
func (s *Store) Authenticate(ctx context.Context, token string) (Account, error) {
	var a Account
	err := s.db.QueryRowContext(ctx, `SELECT u.id,u.email FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=? AND s.expires_at>? AND u.verified=1 AND u.disabled=0`, digest(token), s.now().Unix()).Scan(&a.ID, &a.Email)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, ErrDenied
	}
	return a, err
}
func (s *Store) Logout(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash=?", digest(token))
	return err
}

// RequestReset returns no token for unknown/disabled accounts. HTTP callers must
// send the same generic response regardless and deliver tokens only by mail.
func (s *Store) RequestReset(ctx context.Context, email string) (string, error) {
	return s.requestReset(ctx, email, nil)
}
func (s *Store) requestReset(ctx context.Context, email string, queue enqueueAccountMail) (string, error) {
	email, err := normalizeEmail(email)
	if err != nil {
		return "", nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var id string
	err = tx.QueryRowContext(ctx, "SELECT id FROM users WHERE email=? AND disabled=0 AND verified=1", email).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	token := randomToken()
	if _, err = tx.ExecContext(ctx, "DELETE FROM account_tokens WHERE user_id=? AND purpose='reset'", id); err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO account_tokens VALUES(?,?,'reset',?)", digest(token), id, s.now().Add(30*time.Minute).Unix()); err != nil {
		return "", err
	}
	if queue != nil {
		if err = queue(ctx, tx, id, email, token, "reset"); err != nil {
			return "", err
		}
	}
	return token, tx.Commit()
}
func (s *Store) ResetPassword(ctx context.Context, token, password string) error {
	if !validPassword(password) {
		return ErrInvalid
	}
	// Check before expensive hashing, then consume/recheck atomically afterward.
	var user string
	err := s.db.QueryRowContext(ctx, "SELECT user_id FROM account_tokens WHERE token_hash=? AND purpose='reset' AND expires_at>?", digest(token), s.now().Unix()).Scan(&user)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDenied
	}
	if err != nil {
		return err
	}
	salt := make([]byte, 16)
	if _, err = rand.Read(salt); err != nil {
		return err
	}
	hash, err := s.hash(ctx, password, salt)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, "DELETE FROM account_tokens WHERE token_hash=? AND purpose='reset' AND expires_at>?", digest(token), s.now().Unix())
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrDenied
	}
	if _, err = tx.ExecContext(ctx, "UPDATE users SET salt=?,password_hash=? WHERE id=?", salt, hash, user); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM sessions WHERE user_id=?", user); err != nil {
		return err
	}
	if err = audit(ctx, tx, user, "", "password.reset", s.now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

// authorize resolves identity and membership from the session in the same
// transaction as the resource operation. Caller-supplied user IDs are not trusted.
func (s *Store) authorize(ctx context.Context, tx *sql.Tx, token, workspace string, write bool) (string, error) {
	var id, role string
	err := tx.QueryRowContext(ctx, `SELECT u.id,m.role FROM sessions s JOIN users u ON u.id=s.user_id JOIN memberships m ON m.user_id=u.id WHERE s.token_hash=? AND s.expires_at>? AND u.verified=1 AND u.disabled=0 AND m.workspace_id=?`, digest(token), s.now().Unix(), workspace).Scan(&id, &role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrDenied
	}
	if err != nil {
		return "", err
	}
	if write && role != "owner" && role != "developer" {
		return "", ErrDenied
	}
	return id, nil
}
func (s *Store) CreateProject(ctx context.Context, token, workspace, name, kind string) (Project, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 100 || (kind != "static" && kind != "node") {
		return Project{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Project{}, err
	}
	defer tx.Rollback()
	actor, err := s.authorize(ctx, tx, token, workspace, true)
	if err != nil {
		return Project{}, err
	}
	var existing string
	err = tx.QueryRowContext(ctx, "SELECT id FROM projects WHERE workspace_id=? AND name=?", workspace, name).Scan(&existing)
	if err == nil {
		return Project{}, ErrExists
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Project{}, err
	}
	p := Project{ID: randomToken(), WorkspaceID: workspace, Name: name, Kind: kind}
	if _, err = tx.ExecContext(ctx, "INSERT INTO projects VALUES(?,?,?,?)", p.ID, workspace, name, kind); err != nil {
		return Project{}, fmt.Errorf("create project: %w", err)
	}
	if err = audit(ctx, tx, actor, workspace, "project.created", s.now().Unix()); err != nil {
		return Project{}, err
	}
	return p, tx.Commit()
}
func (s *Store) GetProject(ctx context.Context, token, id string) (Project, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Project{}, err
	}
	defer tx.Rollback()
	var p Project
	err = tx.QueryRowContext(ctx, "SELECT id,workspace_id,name,kind FROM projects WHERE id=?", id).Scan(&p.ID, &p.WorkspaceID, &p.Name, &p.Kind)
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, ErrDenied
	}
	if err != nil {
		return Project{}, err
	}
	if _, err = s.authorize(ctx, tx, token, p.WorkspaceID, false); err != nil {
		return Project{}, err
	}
	return p, tx.Commit()
}

type Workspace struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

func (s *Store) Workspaces(ctx context.Context, token string) ([]Workspace, error) {
	// Session validation and result selection share one transaction.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var user string
	err = tx.QueryRowContext(ctx, `SELECT u.id FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=? AND s.expires_at>? AND u.verified=1 AND u.disabled=0`, digest(token), s.now().Unix()).Scan(&user)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrDenied
	}
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT w.id,w.name,m.role FROM workspaces w JOIN memberships m ON m.workspace_id=w.id WHERE m.user_id=? ORDER BY w.name,w.id`, user)
	if err != nil {
		return nil, err
	}
	out := []Workspace{}
	for rows.Next() {
		var w Workspace
		if err = rows.Scan(&w.ID, &w.Name, &w.Role); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, w)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return out, tx.Commit()
}
func (s *Store) Projects(ctx context.Context, token, workspace string) ([]Project, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = s.authorize(ctx, tx, token, workspace, false); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT id,workspace_id,name,kind FROM projects WHERE workspace_id=? ORDER BY name,id", workspace)
	if err != nil {
		return nil, err
	}
	out := []Project{}
	for rows.Next() {
		var p Project
		if err = rows.Scan(&p.ID, &p.WorkspaceID, &p.Name, &p.Kind); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return out, tx.Commit()
}
