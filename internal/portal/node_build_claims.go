package portal

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

var ErrBuildLease = errors.New("build lease is invalid or expired")

type NodeBuildClaim struct {
	Job         NodeBuild
	ExecutionID string
	Lease       string `json:"-"`
	LeaseUntil  int64
	Archive     []byte `json:"-"`
}

func nodeBuildActor(ctx context.Context, tx *sql.Tx, id string) (bool, error) {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM node_builds j JOIN projects p ON p.id=j.project_id JOIN users u ON u.id=j.actor_id JOIN memberships m ON m.user_id=u.id AND m.workspace_id=p.workspace_id WHERE j.id=? AND p.kind='node' AND u.verified=1 AND u.disabled=0 AND m.role IN ('owner','developer')`, id).Scan(&n)
	return n == 1, err
}

// ClaimNodeBuild is trusted worker access, never a browser API. The caller must
// resolve the project to an approved builder and independently verify the exact
// toolchain digest and architecture before dispatch. ExecutionID must identify
// the VM operation durably. Running jobs are NEVER automatically reclaimed.
func (s *Store) ClaimNodeBuild(ctx context.Context, project, toolchainSHA256, architecture string) (*NodeBuildClaim, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	job, err := scanNodeBuild(tx.QueryRowContext(ctx, "SELECT "+nodeBuildColumns+" FROM node_builds WHERE project_id=? AND state='queued'", project))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if job.ToolchainSHA256 != toolchainSHA256 || job.Plan.Architecture != architecture || job.Plan.OS != "linux" || job.Plan.NodeMajor != 24 || job.Plan.Version != 1 {
		return nil, ErrBuildConflict
	}
	allowed, err := nodeBuildActor(ctx, tx, job.ID)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, ErrDenied
	}
	c := &NodeBuildClaim{Job: job, ExecutionID: randomToken(), Lease: randomToken(), LeaseUntil: s.now().Add(time.Minute).Unix()}
	var savedDigest string
	if err = tx.QueryRowContext(ctx, "SELECT archive,sha256 FROM uploads WHERE id=? AND project_id=?", job.UploadID, project).Scan(&c.Archive, &savedDigest); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(c.Archive)
	if savedDigest != job.Plan.SourceSHA256 || hex.EncodeToString(sum[:]) != savedDigest {
		return nil, ErrBuildConflict
	}
	if _, err = tx.ExecContext(ctx, "UPDATE node_builds SET state='running',execution_id=?,lease_hash=?,lease_until=? WHERE id=? AND state='queued'", c.ExecutionID, digest(c.Lease), c.LeaseUntil, job.ID); err != nil {
		return nil, err
	}
	c.Job.State = "running"
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return c, nil
}

// RenewNodeBuildLease rechecks the original submitter's current permission.
// Failure requires the executor to stop work and reconcile its VM. Lease expiry
// alone does not prove execution has stopped, so it never releases the pending job.
func (s *Store) RenewNodeBuildLease(ctx context.Context, id, execution, lease string) (int64, error) {
	if len(lease) != 64 || execution == "" {
		return 0, ErrBuildLease
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var n int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM node_builds WHERE id=? AND state='running' AND execution_id=? AND lease_hash=? AND lease_until>?", id, execution, digest(lease), s.now().Unix()).Scan(&n); err != nil {
		return 0, err
	}
	if n != 1 {
		return 0, ErrBuildLease
	}
	allowed, err := nodeBuildActor(ctx, tx, id)
	if err != nil {
		return 0, err
	}
	if !allowed {
		return 0, ErrDenied
	}
	until := s.now().Add(time.Minute).Unix()
	if _, err = tx.ExecContext(ctx, "UPDATE node_builds SET lease_until=? WHERE id=?", until, id); err != nil {
		return 0, err
	}
	return until, tx.Commit()
}
