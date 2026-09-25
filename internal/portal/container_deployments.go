package portal

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
)

func (s *Store) migrateContainerDeployments() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err = tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version == 44 || version == 45 || version == 46 || version == 47 {
		return nil
	}
	if version != 43 {
		return errors.New("container deployment migration requires schema 43")
	}
	_, err = tx.Exec(`CREATE TABLE IF NOT EXISTS container_deployments(
 id TEXT PRIMARY KEY,project_id TEXT NOT NULL REFERENCES projects(id),
 release_id TEXT NOT NULL REFERENCES container_releases(id),
 actor_id TEXT NOT NULL REFERENCES users(id),runtime_id TEXT NOT NULL,
 request_key TEXT NOT NULL,revision INTEGER NOT NULL CHECK(revision>0),
 state TEXT NOT NULL CHECK(state IN ('queued','running','succeeded','failed','cancelled')),
 created_at INTEGER NOT NULL,dispatch_intent BLOB,
 UNIQUE(project_id,request_key),UNIQUE(project_id,revision));
 CREATE UNIQUE INDEX IF NOT EXISTS container_deployment_pending ON container_deployments(project_id) WHERE state IN ('queued','running');
 PRAGMA user_version=44;`)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) migrateContainerCredentials() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err = tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version == 45 || version == 46 || version == 47 {
		return nil
	}
	if version != 44 {
		return errors.New("container credential migration requires schema 44")
	}
	_, err = tx.Exec(`CREATE TABLE IF NOT EXISTS container_credentials(
 id TEXT PRIMARY KEY,project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
 request_key TEXT NOT NULL,registry TEXT NOT NULL CHECK(registry IN ('docker.io','ghcr.io')),
 label TEXT NOT NULL,ciphertext BLOB NOT NULL,created_at INTEGER NOT NULL,
 UNIQUE(project_id,request_key));
 CREATE INDEX IF NOT EXISTS container_credentials_project ON container_credentials(project_id,created_at,id);
 PRAGMA user_version=45;`)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) migrateContainerEnvironments() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err = tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version == 46 || version == 47 {
		return nil
	}
	if version != 45 {
		return errors.New("container environment migration requires schema 45")
	}
	_, err = tx.Exec(`CREATE TABLE IF NOT EXISTS container_environments(
 id TEXT PRIMARY KEY,project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
 request_key TEXT NOT NULL,label TEXT NOT NULL,names BLOB NOT NULL,ciphertext BLOB NOT NULL,created_at INTEGER NOT NULL,
 UNIQUE(project_id,request_key));
 CREATE INDEX IF NOT EXISTS container_environments_project ON container_environments(project_id,created_at,id);
 PRAGMA user_version=46;`)
	if err != nil {
		return err
	}
	return tx.Commit()
}

type ContainerDeployment struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	ReleaseID string `json:"release_id"`
	RuntimeID string `json:"runtime_id"`
	Revision  int64  `json:"revision"`
	State     string `json:"state"`
	CreatedAt int64  `json:"created_at"`
}

const containerDeploymentColumns = "id,project_id,release_id,runtime_id,revision,state,created_at"

func scanContainerDeployment(row interface{ Scan(...any) error }) (ContainerDeployment, error) {
	var d ContainerDeployment
	err := row.Scan(&d.ID, &d.ProjectID, &d.ReleaseID, &d.RuntimeID, &d.Revision, &d.State, &d.CreatedAt)
	return d, err
}

// RequestContainerDeployment records a durable publish or rollback request.
// runtimeID must come from trusted operator assignment, never customer input.
// Selecting an earlier retained release creates a new monotonic revision.
func (s *Store) RequestContainerDeployment(ctx context.Context, token, project, release, key, runtimeID string) (ContainerDeployment, error) {
	var zero ContainerDeployment
	if !s.containerProjects {
		return zero, ErrContainerUnavailable
	}
	pin, err := hex.DecodeString(runtimeID)
	if err != nil || len(pin) != 32 || hex.EncodeToString(pin) != runtimeID || len(key) < 16 || len(key) > 128 {
		return zero, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	p, actor, err := s.containerAccess(ctx, tx, token, project)
	if err != nil {
		return zero, err
	}
	old, err := scanContainerDeployment(tx.QueryRowContext(ctx, "SELECT "+containerDeploymentColumns+" FROM container_deployments WHERE project_id=? AND request_key=?", project, key))
	if err == nil {
		if old.ReleaseID != release || old.RuntimeID != runtimeID {
			return zero, ErrConflict
		}
		return old, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return zero, err
	}
	var exists int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM container_releases WHERE id=? AND project_id=?", release, project).Scan(&exists); err != nil {
		return zero, err
	}
	if exists != 1 {
		return zero, ErrDenied
	}
	var count, pending int
	var revision int64
	if err = tx.QueryRowContext(ctx, "SELECT count(*),COALESCE(sum(state IN ('queued','running')),0),COALESCE(max(revision),0)+1 FROM container_deployments WHERE project_id=?", project).Scan(&count, &pending, &revision); err != nil {
		return zero, err
	}
	if pending != 0 {
		return zero, ErrPublishing
	}
	if count >= 1000 {
		return zero, ErrBuildQuota
	}
	// Assignments cannot change while a previous publication may still be live.
	var previousRuntime string
	err = tx.QueryRowContext(ctx, "SELECT runtime_id FROM container_deployments WHERE project_id=? AND state='succeeded' ORDER BY revision DESC LIMIT 1", project).Scan(&previousRuntime)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return zero, err
	}
	if previousRuntime != "" && previousRuntime != runtimeID {
		return zero, ErrConflict
	}
	d := ContainerDeployment{ID: randomToken(), ProjectID: project, ReleaseID: release, RuntimeID: runtimeID, Revision: revision, State: "queued", CreatedAt: s.now().Unix()}
	if _, err = tx.ExecContext(ctx, "INSERT INTO container_deployments(id,project_id,release_id,actor_id,runtime_id,request_key,revision,state,created_at) VALUES(?,?,?,?,?,?,?,?,?)", d.ID, project, release, actor, runtimeID, key, revision, d.State, d.CreatedAt); err != nil {
		return zero, err
	}
	if err = audit(ctx, tx, actor, p.WorkspaceID, "container_deployment.requested:"+d.ID, d.CreatedAt); err != nil {
		return zero, err
	}
	return d, tx.Commit()
}
func (s *Store) ContainerDeployments(ctx context.Context, token, project string) ([]ContainerDeployment, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	p, _, err := s.uploadProject(ctx, tx, token, project, true)
	if err != nil {
		return nil, err
	}
	if p.Kind != "container" {
		return nil, ErrInvalid
	}
	rows, err := tx.QueryContext(ctx, "SELECT "+containerDeploymentColumns+" FROM container_deployments WHERE project_id=? ORDER BY revision DESC LIMIT 100", project)
	if err != nil {
		return nil, err
	}
	out := []ContainerDeployment{}
	for rows.Next() {
		d, err := scanContainerDeployment(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, d)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return out, tx.Commit()
}
func (s *Store) CancelContainerDeployment(ctx context.Context, token, project, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, actor, err := s.uploadProject(ctx, tx, token, project, true)
	if err != nil {
		return err
	}
	if p.Kind != "container" {
		return ErrInvalid
	}
	d, err := scanContainerDeployment(tx.QueryRowContext(ctx, "SELECT "+containerDeploymentColumns+" FROM container_deployments WHERE id=? AND project_id=?", id, project))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDenied
	}
	if err != nil {
		return err
	}
	if d.State == "cancelled" {
		return tx.Commit()
	}
	if d.State != "queued" {
		return ErrPublishing
	}
	if _, err = tx.ExecContext(ctx, "UPDATE container_deployments SET state='cancelled' WHERE id=?", id); err != nil {
		return err
	}
	if err = audit(ctx, tx, actor, p.WorkspaceID, "container_deployment.cancelled:"+id, s.now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ActiveContainerDeployment(ctx context.Context, token, project string) (*ContainerDeployment, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	p, _, err := s.uploadProject(ctx, tx, token, project, true)
	if err != nil {
		return nil, err
	}
	if p.Kind != "container" {
		return nil, ErrInvalid
	}
	d, err := scanContainerDeployment(tx.QueryRowContext(ctx, "SELECT "+containerDeploymentColumns+" FROM container_deployments WHERE project_id=? AND state='succeeded' ORDER BY revision DESC LIMIT 1", project))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &d, tx.Commit()
}
