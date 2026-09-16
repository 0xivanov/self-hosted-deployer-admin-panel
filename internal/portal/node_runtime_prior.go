package portal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// NodeRuntimePrior returns the last trusted active runtime observation for an
// assigned project/runtime. It is worker-only read access and never mutates
// deployment state or exposes customer/session data.
func (s *Store) NodeRuntimePrior(ctx context.Context, project, runtimeID string) (*NodeRuntimeObservation, error) {
	var id, runtime, artifact string
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT j.id,j.runtime_id,j.artifact_sha256,j.result FROM node_active_deployments a JOIN node_deployments j ON j.id=a.deployment_id WHERE a.project_id=?`, project).Scan(&id, &runtime, &artifact, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if runtime != runtimeID || len(raw) == 0 {
		return nil, ErrConflict
	}
	var o NodeRuntimeObservation
	if err = json.Unmarshal(raw, &o); err != nil || o.Routing.Active == nil || o.Routing.Fence != *o.Routing.Active || o.Routing.Status != "active" {
		return nil, ErrConflict
	}
	c := *o.Routing.Active
	if c.ProjectID != project || c.RuntimeID != runtimeID || c.DeploymentID != id || c.ArtifactSHA256 != artifact || c.OperationID == "" || c.Revision <= 0 || !o.Settled || !o.Healthy {
		return nil, ErrConflict
	}
	return &o, nil
}
