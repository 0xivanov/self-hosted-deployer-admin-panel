package portal

import (
	"context"
	"database/sql"
	"errors"
)

var ErrHostingCapacity = errors.New("hosting capacity is full")

// ConfigureProjectCapacity sets admission limits before the HTTP server starts.
// Zero preserves unconfigured/local operation. Every persisted project reserves
// a slot, including projects awaiting provisioning or external deletion cleanup.
// All customer project writers must use the same configured limits.
func (s *Store) ConfigureProjectCapacity(total, node int) error {
	if total < 0 || node < 0 || total > 50 || node > total {
		return ErrInvalid
	}
	s.projectCapacity, s.nodeCapacity = total, node
	return nil
}

type ProjectAvailability struct {
	Used               int64  `json:"used"`
	Limit              *int64 `json:"limit,omitempty"`
	Static             bool   `json:"static"`
	Node               bool   `json:"node"`
	Container          bool   `json:"container"`
	CapacityConfigured bool   `json:"capacity_configured"`
	Message            string `json:"message"`
}

func (s *Store) capacityAvailable(ctx context.Context, tx *sql.Tx) (bool, bool, error) {
	if s.projectCapacity == 0 {
		return true, true, nil
	}
	var total, node int
	if err := tx.QueryRowContext(ctx, "SELECT count(*),COALESCE(sum(CASE WHEN kind IN ('node','container') THEN 1 ELSE 0 END),0) FROM projects").Scan(&total, &node); err != nil {
		return false, false, err
	}
	room := total < s.projectCapacity
	return room, room && node < s.nodeCapacity, nil
}

func (s *Store) ProjectAvailability(ctx context.Context, token, workspace string) (ProjectAvailability, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ProjectAvailability{}, err
	}
	defer tx.Rollback()
	if _, err = s.authorize(ctx, tx, token, workspace, false); err != nil {
		return ProjectAvailability{}, err
	}
	result := ProjectAvailability{CapacityConfigured: s.projectCapacity > 0}
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM projects WHERE workspace_id=?", workspace).Scan(&result.Used); err != nil {
		return result, err
	}
	result.Static, result.Node, err = s.capacityAvailable(ctx, tx)
	if err != nil {
		return result, err
	}
	limits, err := s.workspaceHostingLimits(ctx, tx, workspace)
	if err != nil {
		return result, err
	}
	if limits != nil {
		result.Limit = &limits.Projects
		result.Node = result.Node && limits.Node
		if result.Used >= limits.Projects {
			result.Static = false
			result.Node = false
			result.Message = "Your workspace website allowance is full. Remove an unused website or contact support."
		}
	}
	if result.Message == "" && !result.Static && !result.Node {
		result.Message = "Hosting capacity is currently full. Contact support before adding another website."
	}
	if result.Message == "" && !result.Node {
		result.Message = "Static websites are available. Node.js websites are unavailable under the current plan or hosting capacity."
	}
	if result.Message == "" {
		result.Message = "Space is checked again when you create a website. Websites being removed count until cleanup finishes."
	}
	result.Container = s.containerProjects && result.Node
	return result, tx.Commit()
}
