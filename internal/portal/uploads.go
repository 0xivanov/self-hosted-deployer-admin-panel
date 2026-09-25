package portal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/projectarchive"
)

const WorkspaceUploadBytes = 100 << 20
const WorkspaceUploadCount = 20

var ErrQuota = errors.New("workspace upload quota reached")
var ErrArchive = errors.New("invalid project archive")

type Upload struct {
	RetentionReason string `json:"retention_reason,omitempty"`
	ID              string `json:"id"`
	ProjectID       string `json:"project_id"`
	SHA256          string `json:"sha256"`
	Files           int    `json:"files"`
	ExpandedBytes   int64  `json:"expanded_bytes"`
	CompressedBytes int64  `json:"compressed_bytes"`
	CreatedAt       int64  `json:"created_at"`
}

func (s *Store) uploadProject(ctx context.Context, tx *sql.Tx, token, project string, write bool) (Project, string, error) {
	var p Project
	var requestedAt int64
	err := tx.QueryRowContext(ctx, "SELECT id,workspace_id,name,kind,deletion_requested_at,deletion_error,COALESCE((SELECT label FROM project_client_labels WHERE project_id=projects.id),'') FROM projects WHERE id=?", project).Scan(&p.ID, &p.WorkspaceID, &p.Name, &p.Kind, &requestedAt, &p.DeletionError, &p.ClientLabel)
	p.Deleting = requestedAt != 0
	if errors.Is(err, sql.ErrNoRows) {
		return p, "", ErrDenied
	}
	if err != nil {
		return p, "", err
	}
	if write && p.Deleting {
		return p, "", ErrProjectDeleting
	}
	actor, err := s.authorize(ctx, tx, token, p.WorkspaceID, write)
	return p, actor, err
}

// UploadAccess is checked before reading an HTTP body. SaveUpload checks it
// again after validation so role changes cannot race a pending upload.
func (s *Store) UploadAccess(ctx context.Context, token, project string) (Project, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Project{}, err
	}
	defer tx.Rollback()
	p, _, err := s.uploadProject(ctx, tx, token, project, true)
	if err != nil {
		return p, err
	}
	if p.Kind == "container" {
		return p, ErrInvalid
	}
	if err = s.requireHostingKind(ctx, tx, p.WorkspaceID, p.Kind); err != nil {
		return p, err
	}
	return p, tx.Commit()
}

// SaveUpload stores immutable ZIP bytes and their metadata atomically. Nothing
// is extracted, served or executed. Quotas count all versions in a workspace.
func (s *Store) SaveUpload(ctx context.Context, token, project string, data []byte) (Upload, error) {
	p, err := s.UploadAccess(ctx, token, project)
	if err != nil {
		return Upload{}, err
	}
	manifest, err := projectarchive.Validate(ctx, data, p.Kind)
	if err != nil {
		return Upload{}, fmt.Errorf("%w: %s", ErrArchive, err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Upload{}, err
	}
	defer tx.Rollback()
	p, actor, err := s.uploadProject(ctx, tx, token, project, true)
	if err != nil {
		return Upload{}, err
	}
	if err = s.requireHostingKind(ctx, tx, p.WorkspaceID, p.Kind); err != nil {
		return Upload{}, err
	}
	var count, bytes int64
	err = tx.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(length(u.archive)),0) FROM uploads u JOIN projects p ON p.id=u.project_id WHERE p.workspace_id=?`, p.WorkspaceID).Scan(&count, &bytes)
	if err != nil {
		return Upload{}, err
	}
	if count >= WorkspaceUploadCount || bytes+int64(len(data)) > WorkspaceUploadBytes {
		return Upload{}, ErrQuota
	}
	if err = s.requireHostingUpload(ctx, tx, p.WorkspaceID, count, bytes, int64(len(data))); err != nil {
		return Upload{}, err
	}
	u := Upload{ID: randomToken(), ProjectID: project, SHA256: manifest.SHA256, Files: manifest.Files, ExpandedBytes: manifest.Bytes, CompressedBytes: int64(len(data)), CreatedAt: s.now().Unix()}
	if _, err = tx.ExecContext(ctx, "INSERT INTO uploads VALUES(?,?,?,?,?,?,?)", u.ID, project, u.SHA256, u.Files, u.ExpandedBytes, data, u.CreatedAt); err != nil {
		return Upload{}, err
	}
	if err = audit(ctx, tx, actor, p.WorkspaceID, "project.uploaded:"+u.ID, u.CreatedAt); err != nil {
		return Upload{}, err
	}
	return u, tx.Commit()
}

func (s *Store) Uploads(ctx context.Context, token, project string) ([]Upload, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, _, err = s.uploadProject(ctx, tx, token, project, false); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT u.id,u.project_id,u.sha256,u.files,u.expanded_bytes,length(u.archive),u.created_at,
 CASE
 WHEN EXISTS(SELECT 1 FROM publications p JOIN publication_jobs j ON j.id=p.job_id WHERE j.upload_id=u.id) THEN 'This upload is used by your live website and cannot be deleted.'
 WHEN EXISTS(SELECT 1 FROM publication_jobs j WHERE j.upload_id=u.id AND j.state IN ('queued','running')) THEN 'This upload is being published. Wait for the operation to finish.'
 WHEN EXISTS(SELECT 1 FROM publication_jobs j WHERE j.upload_id=u.id) THEN 'Kept for publication history and restoring previous versions. Deleting historical uploads is not supported yet.'
 WHEN EXISTS(SELECT 1 FROM node_builds b WHERE b.upload_id=u.id AND b.state IN ('queued','running')) THEN 'This upload is being built. Wait for the build to finish.'
 WHEN EXISTS(SELECT 1 FROM node_builds b WHERE b.upload_id=u.id) THEN 'Kept for build history. Deleting uploads referenced by builds is not supported yet.'
 ELSE '' END
 FROM uploads u WHERE u.project_id=? ORDER BY u.created_at DESC,u.id`, project)
	if err != nil {
		return nil, err
	}
	out := []Upload{}
	for rows.Next() {
		var u Upload
		if err = rows.Scan(&u.ID, &u.ProjectID, &u.SHA256, &u.Files, &u.ExpandedBytes, &u.CompressedBytes, &u.CreatedAt, &u.RetentionReason); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, u)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return out, tx.Commit()
}

func (s *Store) DeleteUpload(ctx context.Context, token, project, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, actor, err := s.uploadProject(ctx, tx, token, project, true)
	if err != nil {
		return err
	}
	var retained int
	if err = tx.QueryRowContext(ctx, "SELECT (SELECT count(*) FROM publication_jobs WHERE upload_id=? AND project_id=?) + (SELECT count(*) FROM node_builds WHERE upload_id=? AND project_id=?)", id, project, id, project).Scan(&retained); err != nil {
		return err
	}
	if retained > 0 {
		return ErrRetained
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM uploads WHERE id=? AND project_id=?", id, project)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrDenied
	}
	if err = audit(ctx, tx, actor, p.WorkspaceID, "project.upload_deleted:"+id, s.now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}
