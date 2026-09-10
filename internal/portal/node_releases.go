package portal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodeartifact"
)

// NodeArtifactObservation must originate from the trusted executor control plane,
// never customer stdout. Retirement excludes every delayed start/resume request.
// The executor must export only after all customer processes are proven stopped.
type NodeArtifactObservation struct {
	NodeExecutionObservation
	ArtifactSHA256           string
	DependencyManifestSHA256 string
}

// Implementations must bound transferred bytes before buffering them and must
// retain immutable evidence and archives for retry/recovery.
type NodeArtifactReader interface {
	ReadNodeArtifact(context.Context, string) (NodeArtifactObservation, []byte, error)
}
type NodeRelease struct {
	BuildID         string `json:"build_id"`
	ProjectID       string `json:"project_id"`
	ExecutionID     string `json:"execution_id"`
	SourceSHA256    string `json:"source_sha256"`
	ToolchainSHA256 string `json:"toolchain_sha256"`
	Architecture    string `json:"architecture"`
	ArtifactSHA256  string `json:"artifact_sha256"`
	Files           int    `json:"files"`
	ExpandedBytes   int64  `json:"expanded_bytes"`
	CompressedBytes int64  `json:"compressed_bytes"`
	CreatedAt       int64  `json:"created_at"`
}

func savedNodeRelease(ctx context.Context, tx *sql.Tx, id, project string) (*NodeRelease, error) {
	var raw []byte
	err := tx.QueryRowContext(ctx, "SELECT r.metadata FROM node_releases r JOIN node_builds j ON j.id=r.build_id WHERE j.id=? AND j.project_id=?", id, project).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var release NodeRelease
	if err = json.Unmarshal(raw, &release); err != nil {
		return nil, err
	}
	return &release, nil
}

// RetainNodeRelease is trusted recovery-worker access. Fresh identity-matched
// retirement evidence permits completion even after a worker lease expires.
// It atomically retains a validated archive and completes the build, never
// activates hosting. If the submitter lost permission, it cancels the retired
// build and discards its output instead (nil release, nil error).
func (s *Store) RetainNodeRelease(ctx context.Context, reader NodeArtifactReader, project, id string) (*NodeRelease, error) {
	if reader == nil {
		return nil, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	saved, err := savedNodeRelease(ctx, tx, id, project)
	if err != nil {
		return nil, err
	}
	if saved != nil {
		return saved, tx.Commit()
	}
	job, err := scanNodeBuild(tx.QueryRowContext(ctx, "SELECT "+nodeBuildColumns+" FROM node_builds WHERE id=? AND project_id=?", id, project))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrDenied
	}
	if err != nil {
		return nil, err
	}
	var execution string
	var intent, previous []byte
	if err = tx.QueryRowContext(ctx, "SELECT execution_id,dispatch_intent,result FROM node_builds WHERE id=?", id).Scan(&execution, &intent, &previous); err != nil {
		return nil, err
	}
	if job.State == "cancelled" && len(previous) > 0 {
		return nil, tx.Commit()
	}
	if job.State != "running" || execution == "" || len(intent) == 0 {
		return nil, ErrBuildConflict
	}
	var request NodeExecutionRequest
	if err = json.Unmarshal(intent, &request); err != nil {
		return nil, err
	}
	if request.ExecutionID != execution || request.BuildID != id || request.ProjectID != project || request.Bundle.ManifestSHA256 == "" || request.Plan.SourceSHA256 != job.Plan.SourceSHA256 || request.Plan.Architecture != job.Plan.Architecture || request.ToolchainSHA256 != job.ToolchainSHA256 {
		return nil, ErrBuildConflict
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	started := s.now()
	call, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	observation, archive, err := reader.ReadNodeArtifact(call, execution)
	if err != nil {
		return nil, err
	}
	if observation.ExecutionID != execution || observation.SourceSHA256 != job.Plan.SourceSHA256 || observation.ToolchainSHA256 != job.ToolchainSHA256 || observation.Architecture != job.Plan.Architecture || observation.DependencyManifestSHA256 != request.Bundle.ManifestSHA256 || observation.Outcome != "succeeded" || !observation.Retired || observation.ObservedAt.Before(started) || observation.ObservedAt.After(s.now()) {
		return nil, ErrBuildConflict
	}
	manifest, err := nodeartifact.Validate(call, archive, observation.ArtifactSHA256)
	if err != nil {
		return nil, err
	}
	evidence, err := json.Marshal(observation)
	if err != nil {
		return nil, err
	}
	tx, err = s.db.BeginTx(call, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	saved, err = savedNodeRelease(call, tx, id, project)
	if err != nil {
		return nil, err
	}
	if saved != nil {
		return saved, tx.Commit()
	}
	var state, currentExecution string
	if err = tx.QueryRowContext(call, "SELECT state,execution_id FROM node_builds WHERE id=? AND project_id=?", id, project).Scan(&state, &currentExecution); err != nil {
		return nil, err
	}
	if state != "running" || currentExecution != execution {
		return nil, ErrBuildConflict
	}
	allowed, err := nodeBuildActor(call, tx, id)
	if err != nil {
		return nil, err
	}
	var actor, workspace string
	if err = tx.QueryRowContext(call, "SELECT j.actor_id,p.workspace_id FROM node_builds j JOIN projects p ON p.id=j.project_id WHERE j.id=?", id).Scan(&actor, &workspace); err != nil {
		return nil, err
	}
	finalState := "cancelled"
	var release *NodeRelease
	if allowed {
		var count int
		var used int64
		if err = tx.QueryRowContext(call, "SELECT count(*) FROM node_releases r JOIN node_builds j ON j.id=r.build_id WHERE j.project_id=?", project).Scan(&count); err != nil {
			return nil, err
		}
		if err = tx.QueryRowContext(call, "SELECT COALESCE(sum(r.compressed_bytes),0) FROM node_releases r JOIN node_builds j ON j.id=r.build_id JOIN projects p ON p.id=j.project_id WHERE p.workspace_id=?", workspace).Scan(&used); err != nil {
			return nil, err
		}
		if count >= 10 || used+int64(len(archive)) > 500<<20 {
			return nil, ErrBuildQuota
		}
		release = &NodeRelease{BuildID: id, ProjectID: project, ExecutionID: execution, SourceSHA256: job.Plan.SourceSHA256, ToolchainSHA256: job.ToolchainSHA256, Architecture: job.Plan.Architecture, ArtifactSHA256: manifest.SHA256, Files: manifest.Files, ExpandedBytes: manifest.ExpandedBytes, CompressedBytes: int64(len(archive)), CreatedAt: s.now().Unix()}
		raw, e := json.Marshal(release)
		if e != nil {
			return nil, e
		}
		if _, err = tx.ExecContext(call, "INSERT INTO node_releases(build_id,metadata,archive,compressed_bytes) VALUES(?,?,?,?)", id, raw, archive, len(archive)); err != nil {
			return nil, err
		}
		finalState = "succeeded"
	}
	if _, err = tx.ExecContext(call, "UPDATE node_builds SET state=?,result=?,lease_hash='',lease_until=0 WHERE id=?", finalState, evidence, id); err != nil {
		return nil, err
	}
	if err = audit(call, tx, actor, workspace, "node_build.artifact_"+finalState+":"+id, s.now().Unix()); err != nil {
		return nil, err
	}
	return release, tx.Commit()
}

// NodeReleases exposes retained metadata only to current project owners/developers.
func (s *Store) NodeReleases(ctx context.Context, token, project string) ([]NodeRelease, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, _, err = s.uploadProject(ctx, tx, token, project, true); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT r.metadata FROM node_releases r JOIN node_builds j ON j.id=r.build_id WHERE j.project_id=? ORDER BY j.created_at DESC,j.id", project)
	if err != nil {
		return nil, err
	}
	result := []NodeRelease{}
	for rows.Next() {
		var raw []byte
		var release NodeRelease
		if err = rows.Scan(&raw); err == nil {
			err = json.Unmarshal(raw, &release)
		}
		if err != nil {
			rows.Close()
			return nil, err
		}
		result = append(result, release)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return result, tx.Commit()
}

// NodeReleaseArchive checks current membership and stored archive integrity. It
// does not grant a browser any executor capability or activate the release.
func (s *Store) NodeReleaseArchive(ctx context.Context, token, project, id string) ([]byte, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, _, err = s.uploadProject(ctx, tx, token, project, true); err != nil {
		return nil, err
	}
	saved, err := savedNodeRelease(ctx, tx, id, project)
	if err != nil {
		return nil, err
	}
	if saved == nil {
		return nil, ErrDenied
	}
	var data []byte
	if err = tx.QueryRowContext(ctx, "SELECT archive FROM node_releases WHERE build_id=?", id).Scan(&data); err != nil {
		return nil, err
	}
	if _, err = nodeartifact.Validate(ctx, data, saved.ArtifactSHA256); err != nil {
		return nil, err
	}
	return data, tx.Commit()
}

// DeleteNodeRelease removes a retained archive, preserving build/audit history.
// Future deployment references must use restricting foreign keys to prevent
// deleting active or queued releases. No hosting activation exists here yet.
func (s *Store) DeleteNodeRelease(ctx context.Context, token, project, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, actor, err := s.uploadProject(ctx, tx, token, project, true)
	if err != nil {
		return err
	}
	saved, err := savedNodeRelease(ctx, tx, id, project)
	if err != nil {
		return err
	}
	if saved == nil {
		return tx.Commit()
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM node_releases WHERE build_id=?", id); err != nil {
		return err
	}
	if err = audit(ctx, tx, actor, p.WorkspaceID, "node_release.deleted:"+id, s.now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}
