package portal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// ContainerRuntimeRequest is committed before external work. A runtime must
// durably deduplicate Deployment.ID and reject reuse with different input.
// A nil submit error means acceptance, not a healthy or completed deployment.
type ContainerRuntimeRequest struct {
	Deployment     ContainerDeployment `json:"deployment"`
	Release        ContainerRelease    `json:"release"`
	Previous       *ContainerRelease   `json:"previous,omitempty"`
	ActivateBefore int64               `json:"activate_before"`
}
type ContainerRuntimeSubmitter interface {
	SubmitContainerRuntime(context.Context, ContainerRuntimeRequest) error
}

func containerReleaseByID(ctx context.Context, tx *sql.Tx, project, id string) (ContainerRelease, error) {
	var key string
	if err := tx.QueryRowContext(ctx, "SELECT request_key FROM container_releases WHERE id=? AND project_id=?", id, project).Scan(&key); err != nil {
		return ContainerRelease{}, err
	}
	return savedContainerRelease(ctx, tx, project, key)
}

// DispatchContainerDeployment sends each request at most once. Running jobs are
// deliberately left for reconciliation, including when submission loses its reply.
// This API is worker-only and takes an operator-controlled project assignment.
func (s *Store) DispatchContainerDeployment(ctx context.Context, project, runtimeID string, runtime ContainerRuntimeSubmitter) (bool, error) {
	if runtime == nil {
		return false, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	d, err := scanContainerDeployment(tx.QueryRowContext(ctx, "SELECT "+containerDeploymentColumns+" FROM container_deployments WHERE project_id=? AND runtime_id=? AND state='queued' ORDER BY revision LIMIT 1", project, runtimeID))
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var allowed int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM container_deployments d
 JOIN projects p ON p.id=d.project_id JOIN users u ON u.id=d.actor_id
 JOIN memberships m ON m.user_id=u.id AND m.workspace_id=p.workspace_id
 WHERE d.id=? AND p.kind='container' AND p.deletion_requested_at=0 AND u.verified=1 AND u.disabled=0 AND m.role IN ('owner','developer')`, d.ID).Scan(&allowed)
	if err != nil {
		return false, err
	}
	accessErr := s.requireProjectHostingAccess(ctx, tx, project)
	if accessErr != nil && !errors.Is(accessErr, ErrHostingPayment) && !errors.Is(accessErr, ErrHostingPlanLimit) {
		return false, accessErr
	}
	if allowed != 1 || accessErr != nil {
		if _, err = tx.ExecContext(ctx, "UPDATE container_deployments SET state='cancelled' WHERE id=?", d.ID); err != nil {
			return false, err
		}
		return true, tx.Commit()
	}
	release, err := containerReleaseByID(ctx, tx, project, d.ReleaseID)
	if err != nil {
		return false, err
	}
	if !validContainerCandidate(release.Input, release.Image) {
		return false, ErrInvalid
	}
	normalized, err := normalizeContainerInput(release.Input)
	if err != nil || normalized != release.Input {
		return false, ErrInvalid
	}
	q := ContainerRuntimeRequest{Deployment: d, Release: release, ActivateBefore: s.now().Add(10 * time.Minute).Unix()}
	var previousID string
	err = tx.QueryRowContext(ctx, "SELECT release_id FROM container_deployments WHERE project_id=? AND state='succeeded' ORDER BY revision DESC LIMIT 1", project).Scan(&previousID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if previousID != "" {
		previous, err := containerReleaseByID(ctx, tx, project, previousID)
		if err != nil {
			return false, err
		}
		q.Previous = &previous
	}
	q.Deployment.State = "running"
	raw, err := json.Marshal(q)
	if err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE container_deployments SET state='running',dispatch_intent=? WHERE id=? AND state='queued'", raw, d.ID); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	call, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err = call.Err(); err != nil {
		return true, err
	}
	return true, runtime.SubmitContainerRuntime(call, q)
}
