package portal

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodeartifact"
)

type NodeDeployment struct {
	ID             string `json:"id"`
	ProjectID      string `json:"project_id"`
	ReleaseID      string `json:"release_id"`
	ArtifactSHA256 string `json:"artifact_sha256"`
	RuntimeID      string `json:"runtime_id"`
	Revision       int64  `json:"revision"`
	State          string `json:"state"`
	CreatedAt      int64  `json:"created_at"`
}

const nodeDeploymentColumns = "id,project_id,release_id,artifact_sha256,runtime_id,revision,state,created_at"

func scanNodeDeployment(row interface{ Scan(...any) error }) (NodeDeployment, error) {
	var j NodeDeployment
	err := row.Scan(&j.ID, &j.ProjectID, &j.ReleaseID, &j.ArtifactSHA256, &j.RuntimeID, &j.Revision, &j.State, &j.CreatedAt)
	return j, err
}

// RequestNodeDeployment selects an immutable retained release. runtimeID is an
// operator-assigned identifier, never a customer-selected host or URL. Selecting
// an older release uses the same monotonic revision path for rollback.
func (s *Store) RequestNodeDeployment(ctx context.Context, token, project, releaseID, key, runtimeID string) (NodeDeployment, error) {
	pin, err := hex.DecodeString(runtimeID)
	if err != nil || len(pin) != 32 || hex.EncodeToString(pin) != runtimeID || len(key) < 16 || len(key) > 128 {
		return NodeDeployment{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return NodeDeployment{}, err
	}
	defer tx.Rollback()
	p, actor, err := s.uploadProject(ctx, tx, token, project, true)
	if err != nil {
		return NodeDeployment{}, err
	}
	if p.Kind != "node" {
		return NodeDeployment{}, ErrInvalid
	}
	existing, err := scanNodeDeployment(tx.QueryRowContext(ctx, "SELECT "+nodeDeploymentColumns+" FROM node_deployments WHERE project_id=? AND request_key=?", project, key))
	if err == nil {
		if existing.ReleaseID != releaseID || existing.RuntimeID != runtimeID {
			return NodeDeployment{}, ErrConflict
		}
		return existing, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return NodeDeployment{}, err
	}
	var activeRuntime string
	err = tx.QueryRowContext(ctx, "SELECT j.runtime_id FROM node_active_deployments a JOIN node_deployments j ON j.id=a.deployment_id WHERE a.project_id=?", project).Scan(&activeRuntime)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return NodeDeployment{}, err
	}
	if activeRuntime != "" && activeRuntime != runtimeID {
		return NodeDeployment{}, ErrConflict
	}
	release, err := savedNodeRelease(ctx, tx, releaseID, project)
	if err != nil {
		return NodeDeployment{}, err
	}
	if release == nil {
		return NodeDeployment{}, ErrDenied
	}
	var count, pending int
	var revision int64
	if err = tx.QueryRowContext(ctx, "SELECT count(*),COALESCE(sum(state IN ('queued','running')),0),COALESCE(max(revision),0)+1 FROM node_deployments WHERE project_id=?", project).Scan(&count, &pending, &revision); err != nil {
		return NodeDeployment{}, err
	}
	if pending > 0 {
		return NodeDeployment{}, ErrPublishing
	}
	if count >= 1000 {
		return NodeDeployment{}, ErrBuildQuota
	}
	j := NodeDeployment{ID: randomToken(), ProjectID: project, ReleaseID: releaseID, ArtifactSHA256: release.ArtifactSHA256, RuntimeID: runtimeID, Revision: revision, State: "queued", CreatedAt: s.now().Unix()}
	if _, err = tx.ExecContext(ctx, "INSERT INTO node_deployments(id,project_id,release_id,artifact_sha256,runtime_id,actor_id,request_key,revision,state,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)", j.ID, project, releaseID, j.ArtifactSHA256, runtimeID, actor, key, revision, j.State, j.CreatedAt); err != nil {
		return NodeDeployment{}, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO node_deployment_releases(deployment_id,release_id) VALUES(?,?)", j.ID, releaseID); err != nil {
		return NodeDeployment{}, err
	}
	if err = audit(ctx, tx, actor, p.WorkspaceID, "node_deployment.requested:"+j.ID, j.CreatedAt); err != nil {
		return NodeDeployment{}, err
	}
	return j, tx.Commit()
}
func (s *Store) NodeDeployments(ctx context.Context, token, project string) ([]NodeDeployment, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, _, err = s.uploadProject(ctx, tx, token, project, true); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT "+nodeDeploymentColumns+" FROM node_deployments WHERE project_id=? ORDER BY revision DESC LIMIT 100", project)
	if err != nil {
		return nil, err
	}
	result := []NodeDeployment{}
	for rows.Next() {
		j, e := scanNodeDeployment(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		result = append(result, j)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return result, tx.Commit()
}
func (s *Store) CancelNodeDeployment(ctx context.Context, token, project, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, actor, err := s.uploadProject(ctx, tx, token, project, true)
	if err != nil {
		return err
	}
	var state string
	err = tx.QueryRowContext(ctx, "SELECT state FROM node_deployments WHERE id=? AND project_id=?", id, project).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDenied
	}
	if err != nil {
		return err
	}
	if state == "cancelled" {
		return tx.Commit()
	}
	if state != "queued" {
		return ErrPublishing
	}
	if _, err = tx.ExecContext(ctx, "UPDATE node_deployments SET state='cancelled' WHERE id=?", id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM node_deployment_releases WHERE deployment_id=?", id); err != nil {
		return err
	}
	if err = audit(ctx, tx, actor, p.WorkspaceID, "node_deployment.cancelled:"+id, s.now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

type NodeDeploymentClaim struct {
	Job         NodeDeployment
	Release     NodeRelease
	OperationID string
	Lease       string `json:"-"`
	LeaseUntil  int64
	Archive     []byte `json:"-"`
}

func nodeDeploymentActor(ctx context.Context, tx *sql.Tx, id string) (bool, error) {
	var count int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM node_deployments j JOIN projects p ON p.id=j.project_id JOIN users u ON u.id=j.actor_id JOIN memberships m ON m.workspace_id=p.workspace_id AND m.user_id=u.id WHERE j.id=? AND p.kind='node' AND u.disabled=0 AND u.verified=1 AND m.role IN ('owner','developer')`, id).Scan(&count)
	return count == 1, err
}

// ClaimNodeDeployment is trusted worker access. Matching the assigned runtime,
// toolchain and architecture is mandatory. Running jobs are never reclaimed;
// an expired lease does not prove the runtime or a delayed activation stopped.
func (s *Store) ClaimNodeDeployment(ctx context.Context, project, runtimeID, toolchain, architecture string) (*NodeDeploymentClaim, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	j, err := scanNodeDeployment(tx.QueryRowContext(ctx, "SELECT "+nodeDeploymentColumns+" FROM node_deployments WHERE project_id=? AND state='queued'", project))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if j.RuntimeID != runtimeID {
		return nil, ErrConflict
	}
	allowed, err := nodeDeploymentActor(ctx, tx, j.ID)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, ErrDenied
	}
	release, err := savedNodeRelease(ctx, tx, j.ReleaseID, project)
	if err != nil {
		return nil, err
	}
	if release == nil || release.ArtifactSHA256 != j.ArtifactSHA256 || release.ToolchainSHA256 != toolchain || release.Architecture != architecture {
		return nil, ErrConflict
	}
	c := &NodeDeploymentClaim{Job: j, Release: *release, OperationID: randomToken(), Lease: randomToken()}
	if err = tx.QueryRowContext(ctx, "SELECT r.archive FROM node_releases r JOIN node_deployment_releases ref ON ref.release_id=r.build_id WHERE ref.deployment_id=?", j.ID).Scan(&c.Archive); err != nil {
		return nil, err
	}
	if _, err = nodeartifact.Validate(ctx, c.Archive, j.ArtifactSHA256); err != nil {
		return nil, err
	}
	c.LeaseUntil = s.now().Add(time.Minute).Unix()
	c.Job.State = "running"
	if _, err = tx.ExecContext(ctx, "UPDATE node_deployments SET state='running',operation_id=?,lease_hash=?,lease_until=? WHERE id=?", c.OperationID, digest(c.Lease), c.LeaseUntil, j.ID); err != nil {
		return nil, err
	}
	return c, tx.Commit()
}
func (s *Store) RenewNodeDeploymentLease(ctx context.Context, id, operation, lease string) (int64, error) {
	if len(lease) != 64 || operation == "" {
		return 0, ErrBuildLease
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM node_deployments WHERE id=? AND state='running' AND operation_id=? AND lease_hash=? AND lease_until>?", id, operation, digest(lease), s.now().Unix()).Scan(&count); err != nil {
		return 0, err
	}
	if count != 1 {
		return 0, ErrBuildLease
	}
	allowed, err := nodeDeploymentActor(ctx, tx, id)
	if err != nil {
		return 0, err
	}
	if !allowed {
		return 0, ErrDenied
	}
	until := s.now().Add(time.Minute).Unix()
	if _, err = tx.ExecContext(ctx, "UPDATE node_deployments SET lease_until=? WHERE id=?", until, id); err != nil {
		return 0, err
	}
	return until, tx.Commit()
}
