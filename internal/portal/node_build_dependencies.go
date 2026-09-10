package portal

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/npmfetch"
)

func (s *Store) leasedNodeBuild(ctx context.Context, tx *sql.Tx, id, execution, lease string) (NodeBuild, error) {
	if len(lease) != 64 || execution == "" {
		return NodeBuild{}, ErrBuildLease
	}
	job, err := scanNodeBuild(tx.QueryRowContext(ctx, "SELECT "+nodeBuildColumns+" FROM node_builds WHERE id=? AND state='running' AND execution_id=? AND lease_hash=? AND lease_until>?", id, execution, digest(lease), s.now().Unix()))
	if errors.Is(err, sql.ErrNoRows) {
		return NodeBuild{}, ErrBuildLease
	}
	if err != nil {
		return NodeBuild{}, err
	}
	allowed, err := nodeBuildActor(ctx, tx, id)
	if err != nil {
		return NodeBuild{}, err
	}
	if !allowed {
		return NodeBuild{}, ErrDenied
	}
	return job, nil
}

// BindNodeBuildDependencies is trusted worker access. jobs is the operator's
// assigned storage root, never a browser-supplied path. Verification and lockfile
// comparison happen before a second transactional lease/permission check. The
// storage directory must remain immutable through verification, binding and use.
func (s *Store) BindNodeBuildDependencies(ctx context.Context, id, execution, lease string, jobs *os.Root, bundle npmfetch.Bundle) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	job, err := s.leasedNodeBuild(ctx, tx, id, execution, lease)
	if err != nil {
		return err
	}
	var source []byte
	if err = tx.QueryRowContext(ctx, "SELECT archive FROM uploads WHERE id=? AND project_id=?", job.UploadID, job.ProjectID).Scan(&source); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	expected, err := npmfetch.FromSource(ctx, source, job.Plan.SourceSHA256)
	if err != nil {
		return err
	}
	manifest, err := npmfetch.VerifyBundle(ctx, jobs, bundle, job.Plan.SourceSHA256)
	if err != nil {
		return err
	}
	if len(expected.Tarballs) != len(manifest.Tarballs) {
		return ErrBuildConflict
	}
	entries := map[string]string{}
	for _, item := range expected.Tarballs {
		entries[item.URL] = item.Integrity
	}
	for _, item := range manifest.Tarballs {
		if entries[item.URL] != item.Integrity {
			return ErrBuildConflict
		}
	}
	raw, err := json.Marshal(bundle)
	if err != nil {
		return err
	}
	tx, err = s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = s.leasedNodeBuild(ctx, tx, id, execution, lease); err != nil {
		return err
	}
	var existing []byte
	if err = tx.QueryRowContext(ctx, "SELECT dependency_bundle FROM node_builds WHERE id=?", id).Scan(&existing); err != nil {
		return err
	}
	if len(existing) > 0 {
		if !bytes.Equal(existing, raw) {
			return ErrBuildConflict
		}
		return tx.Commit()
	}
	if _, err = tx.ExecContext(ctx, "UPDATE node_builds SET dependency_bundle=? WHERE id=?", raw, id); err != nil {
		return err
	}
	return tx.Commit()
}

// NodeBuildDependencies returns only the bound reference to the current worker.
// Reopen with VerifyBundle before import; stored metadata alone is not validation
// of files that could have been lost or changed since binding.
func (s *Store) NodeBuildDependencies(ctx context.Context, id, execution, lease string) (*npmfetch.Bundle, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = s.leasedNodeBuild(ctx, tx, id, execution, lease); err != nil {
		return nil, err
	}
	var raw []byte
	if err = tx.QueryRowContext(ctx, "SELECT dependency_bundle FROM node_builds WHERE id=?", id).Scan(&raw); err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, tx.Commit()
	}
	var bundle npmfetch.Bundle
	if err = json.Unmarshal(raw, &bundle); err != nil {
		return nil, err
	}
	return &bundle, tx.Commit()
}
