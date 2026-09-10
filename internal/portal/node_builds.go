package portal

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodebuild"
)

var ErrBuildConflict = errors.New("build request conflicts with a retained request or pending build")
var ErrBuildQuota = errors.New("retained build quota reached")

// NodeBuildAssignment is trusted operator configuration. The worker must match
// the pinned toolchain digest and architecture before executing this plan.
type NodeBuildAssignment struct {
	Settings        nodebuild.Settings
	ToolchainSHA256 string
}
type NodeBuild struct {
	ID              string         `json:"id"`
	ProjectID       string         `json:"project_id"`
	UploadID        string         `json:"upload_id"`
	Plan            nodebuild.Plan `json:"plan"`
	ToolchainSHA256 string         `json:"toolchain_sha256"`
	State           string         `json:"state"`
	CreatedAt       int64          `json:"created_at"`
}

const nodeBuildColumns = "id,project_id,upload_id,plan,toolchain_sha256,state,created_at"

func scanNodeBuild(row interface{ Scan(...any) error }) (NodeBuild, error) {
	var j NodeBuild
	var raw []byte
	if err := row.Scan(&j.ID, &j.ProjectID, &j.UploadID, &raw, &j.ToolchainSHA256, &j.State, &j.CreatedAt); err != nil {
		return j, err
	}
	err := json.Unmarshal(raw, &j.Plan)
	return j, err
}

// RequestNodeBuild persists a source-bound build specification. No customer code
// is executed. All records retain their upload, including cancelled requests.
func (s *Store) RequestNodeBuild(ctx context.Context, token, project, upload, key string, assignment NodeBuildAssignment) (NodeBuild, error) {
	pin, err := hex.DecodeString(assignment.ToolchainSHA256)
	if err != nil || len(pin) != 32 || hex.EncodeToString(pin) != assignment.ToolchainSHA256 || len(key) < 16 || len(key) > 128 {
		return NodeBuild{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return NodeBuild{}, err
	}
	defer tx.Rollback()
	p, actor, err := s.uploadProject(ctx, tx, token, project, true)
	if err != nil {
		return NodeBuild{}, err
	}
	if p.Kind != "node" {
		return NodeBuild{}, ErrInvalid
	}
	var source []byte
	var digest string
	err = tx.QueryRowContext(ctx, "SELECT archive,sha256 FROM uploads WHERE id=? AND project_id=?", upload, project).Scan(&source, &digest)
	if errors.Is(err, sql.ErrNoRows) {
		return NodeBuild{}, ErrDenied
	}
	if err != nil {
		return NodeBuild{}, err
	}
	plan, err := nodebuild.Prepare(ctx, source, digest, assignment.Settings)
	if err != nil {
		return NodeBuild{}, err
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		return NodeBuild{}, err
	}
	existing, err := scanNodeBuild(tx.QueryRowContext(ctx, "SELECT "+nodeBuildColumns+" FROM node_builds WHERE project_id=? AND request_key=?", project, key))
	if err == nil {
		previous, e := json.Marshal(existing.Plan)
		if e != nil {
			return NodeBuild{}, e
		}
		if existing.UploadID != upload || existing.ToolchainSHA256 != assignment.ToolchainSHA256 || !bytes.Equal(previous, raw) {
			return NodeBuild{}, ErrBuildConflict
		}
		return existing, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return NodeBuild{}, err
	}
	var count, pending int
	if err = tx.QueryRowContext(ctx, "SELECT count(*),COALESCE(sum(state IN ('queued','running')),0) FROM node_builds WHERE project_id=?", project).Scan(&count, &pending); err != nil {
		return NodeBuild{}, err
	}
	if pending > 0 {
		return NodeBuild{}, ErrBuildConflict
	}
	if count >= 100 {
		return NodeBuild{}, ErrBuildQuota
	}
	j := NodeBuild{ID: randomToken(), ProjectID: project, UploadID: upload, Plan: plan, ToolchainSHA256: assignment.ToolchainSHA256, State: "queued", CreatedAt: s.now().Unix()}
	if _, err = tx.ExecContext(ctx, "INSERT INTO node_builds(id,project_id,upload_id,actor_id,request_key,plan,toolchain_sha256,state,created_at) VALUES(?,?,?,?,?,?,?,?,?)", j.ID, project, upload, actor, key, raw, j.ToolchainSHA256, j.State, j.CreatedAt); err != nil {
		return NodeBuild{}, err
	}
	if err = audit(ctx, tx, actor, p.WorkspaceID, "node_build.requested:"+j.ID, j.CreatedAt); err != nil {
		return NodeBuild{}, err
	}
	return j, tx.Commit()
}

func (s *Store) NodeBuilds(ctx context.Context, token, project string) ([]NodeBuild, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Build details are limited to owners/developers, like future build logs.
	if _, _, err = s.uploadProject(ctx, tx, token, project, true); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT "+nodeBuildColumns+" FROM node_builds WHERE project_id=? ORDER BY created_at DESC,id", project)
	if err != nil {
		return nil, err
	}
	out := []NodeBuild{}
	for rows.Next() {
		j, e := scanNodeBuild(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		out = append(out, j)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return out, tx.Commit()
}

// CancelNodeBuild is idempotent for cancelled requests. Running work must use a
// future executor cancellation protocol, never just relabel an active VM job.
func (s *Store) CancelNodeBuild(ctx context.Context, token, project, id string) error {
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
	err = tx.QueryRowContext(ctx, "SELECT state FROM node_builds WHERE id=? AND project_id=?", id, project).Scan(&state)
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
		return ErrBuildConflict
	}
	if _, err = tx.ExecContext(ctx, "UPDATE node_builds SET state='cancelled' WHERE id=?", id); err != nil {
		return err
	}
	if err = audit(ctx, tx, actor, p.WorkspaceID, "node_build.cancelled:"+id, s.now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}
