package portal

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/projectarchive"
)

// DownloadUpload returns original source only to current project editors.
// Runtime identities, publication retention and billing are unchanged.
func (s *Store) DownloadUpload(ctx context.Context, token, project, id string) ([]byte, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, _, err = s.uploadProject(ctx, tx, token, project, true); err != nil {
		return nil, err
	}
	var data []byte
	var digest string
	err = tx.QueryRowContext(ctx, "SELECT archive,sha256 FROM uploads WHERE id=? AND project_id=? AND length(archive)>0 AND length(archive)<=?", id, project, projectarchive.MaxCompressed).Scan(&data, &digest)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrDenied
	}
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != digest {
		return nil, errors.New("stored upload integrity check failed")
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return data, nil
}
