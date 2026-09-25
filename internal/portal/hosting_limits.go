package portal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

var ErrHostingPlanLimit = errors.New("hosting plan limit reached")

type HostingPlanLimits struct {
	Projects    int64 `json:"projects"`
	Uploads     int64 `json:"uploads"`
	UploadBytes int64 `json:"upload_bytes"`
	Node        bool  `json:"node"`
}
type HostingUsage struct {
	Projects    int64 `json:"projects"`
	Uploads     int64 `json:"uploads"`
	UploadBytes int64 `json:"upload_bytes"`
}

// ConfigureHostingLimits is operator configuration for enrolled sandbox workspaces.
// Changes apply to future checkouts. Saved checkout allowances stay unchanged.
func (s *Store) ConfigureHostingLimits(ctx context.Context, plan string, limits HostingPlanLimits) error {
	if plan == "" || limits.Projects < 1 || limits.Projects > 100 || limits.Uploads < 1 || limits.Uploads > WorkspaceUploadCount || limits.UploadBytes < 1 || limits.UploadBytes > WorkspaceUploadBytes {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	mode := s.billingModeValue()
	var exists int
	if err = tx.QueryRowContext(ctx, "SELECT 1 FROM billing_plans WHERE mode=? AND id=?", mode, plan).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return ErrDenied
	} else if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO hosting_plan_limits(mode,plan_id,projects,uploads,upload_bytes,node) VALUES(?,?,?,?,?,?) ON CONFLICT(mode,plan_id) DO UPDATE SET projects=excluded.projects,uploads=excluded.uploads,upload_bytes=excluded.upload_bytes,node=excluded.node`, mode, plan, limits.Projects, limits.Uploads, limits.UploadBytes, limits.Node); err != nil {
		return err
	}
	if err = audit(ctx, tx, "operator", "", "hosting.plan-limits.configured:"+plan, s.now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) savedHostingLimits(ctx context.Context, tx *sql.Tx, plan string) (*HostingPlanLimits, error) {
	var limits HostingPlanLimits
	err := tx.QueryRowContext(ctx, "SELECT projects,uploads,upload_bytes,node FROM hosting_plan_limits WHERE mode=? AND plan_id=?", s.billingModeValue(), plan).Scan(&limits.Projects, &limits.Uploads, &limits.UploadBytes, &limits.Node)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &limits, nil
}
func (s *Store) workspaceHostingLimits(ctx context.Context, tx *sql.Tx, workspace string) (*HostingPlanLimits, error) {
	entitlement, err := s.qualifyingHostingEntitlement(ctx, tx, workspace)
	if err != nil {
		return nil, err
	}
	if entitlement == nil {
		return nil, nil
	}
	return entitlement.Limits, nil
}
func (s *Store) hostingUsage(ctx context.Context, tx *sql.Tx, workspace string) (HostingUsage, error) {
	var usage HostingUsage
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM projects WHERE workspace_id=?", workspace).Scan(&usage.Projects); err != nil {
		return usage, err
	}
	err := tx.QueryRowContext(ctx, "SELECT count(*),COALESCE(sum(length(u.archive)),0) FROM uploads u JOIN projects p ON p.id=u.project_id WHERE p.workspace_id=?", workspace).Scan(&usage.Uploads, &usage.UploadBytes)
	return usage, err
}
func (s *Store) requireHostingKind(ctx context.Context, tx *sql.Tx, workspace, kind string) error {
	limits, err := s.workspaceHostingLimits(ctx, tx, workspace)
	if err != nil {
		return err
	}
	if limits != nil && (kind == "node" || kind == "container") && !limits.Node {
		return ErrHostingPlanLimit
	}
	return nil
}
func (s *Store) requireHostingProject(ctx context.Context, tx *sql.Tx, workspace, kind string) error {
	limits, err := s.workspaceHostingLimits(ctx, tx, workspace)
	if err != nil {
		return err
	}
	if limits == nil {
		return nil
	}
	if (kind == "node" || kind == "container") && !limits.Node {
		return ErrHostingPlanLimit
	}
	var count int64
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM projects WHERE workspace_id=?", workspace).Scan(&count); err != nil {
		return err
	}
	if count >= limits.Projects {
		return ErrHostingPlanLimit
	}
	return nil
}
func (s *Store) requireHostingUpload(ctx context.Context, tx *sql.Tx, workspace string, count, stored, incoming int64) error {
	limits, err := s.workspaceHostingLimits(ctx, tx, workspace)
	if err != nil {
		return err
	}
	if limits == nil {
		return nil
	}
	if count >= limits.Uploads || incoming < 0 || stored > limits.UploadBytes || incoming > limits.UploadBytes-stored {
		return ErrHostingPlanLimit
	}
	return nil
}

// A NULL snapshot preserves the platform defaults offered with that checkout.
func decodeHostingLimits(data []byte) (*HostingPlanLimits, error) {
	if data == nil {
		return nil, nil
	}
	var limits HostingPlanLimits
	if err := json.Unmarshal(data, &limits); err != nil {
		return nil, ErrBillingConflict
	}
	if limits.Projects < 1 || limits.Projects > 100 || limits.Uploads < 1 || limits.Uploads > WorkspaceUploadCount || limits.UploadBytes < 1 || limits.UploadBytes > WorkspaceUploadBytes {
		return nil, ErrBillingConflict
	}
	return &limits, nil
}
