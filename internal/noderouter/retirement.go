package noderouter

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
)

func (r *Router) validCandidate(c Candidate) bool {
	return c.ProjectID == r.config.ProjectID && c.RuntimeID == r.config.RuntimeID && c.Revision > 0 && digestID(c.DeploymentID) && digestID(c.OperationID) && digestID(c.ArtifactSHA256) && r.backends[c.Backend] != nil
}
func rejectRetired(ctx context.Context, tx *sql.Tx, operation string) error {
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM retired_operations WHERE operation=?", operation).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return ErrRetired
	}
	return nil
}

// FenceRetirement permanently rejects activation of this operation, including
// already-running health probes. It refuses an active operation or a backend
// still used by the active route. Move traffic to a replacement first.
//
// This is trusted control-plane access. It does not drain in-flight requests,
// cancel socket copies, stop services or prove RoutingDetached for the pool.
// Never erase tombstones while delayed activation requests could still exist.
func (r *Router) FenceRetirement(ctx context.Context, c Candidate) error {
	if !r.validCandidate(c) {
		return ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	var old []byte
	err = tx.QueryRowContext(ctx, "SELECT candidate FROM retired_operations WHERE operation=?", c.OperationID).Scan(&old)
	if err == nil {
		if !bytes.Equal(old, raw) {
			return ErrConflict
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	state, err := readState(tx.QueryRowContext(ctx, "SELECT state FROM node_route WHERE id=1"))
	if err != nil {
		return err
	}
	if state.Active != nil && (state.Active.OperationID == c.OperationID || state.Active.Backend == c.Backend) {
		return ErrConflict
	}
	if state.Fence.OperationID == c.OperationID && state.Fence != c {
		return ErrConflict
	}
	if state.Fence == c && state.Status == "pending" {
		state.Status = "failed"
		if err = writeState(ctx, tx, state); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO retired_operations(operation,candidate) VALUES(?,?)", c.OperationID, raw); err != nil {
		return err
	}
	return tx.Commit()
}

// BackendPort returns the pinned loopback port for a validated routing candidate.
// Launchers use it to bind retirement to the exact reserved service listener.
func (r *Router) BackendPort(c Candidate) (int, error) {
	if !r.validCandidate(c) {
		return 0, ErrInvalid
	}
	return strconv.Atoi(r.backends[c.Backend].Port())
}

// BackendForPort resolves an operator-pinned slot without accepting a customer URL.
func (r *Router) BackendForPort(port int) (string, error) {
	for name, target := range r.backends {
		if target.Port() == strconv.Itoa(port) {
			return name, nil
		}
	}
	return "", ErrInvalid
}

// RejectActivation permanently fences an unactivated candidate and publishes
// its failed revision while retaining the serving route. This also covers a
// deployment that failed before Activate wrote a pending route.
func (r *Router) RejectActivation(ctx context.Context, c Candidate) error {
	if err := r.FenceRetirement(ctx, c); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	state, err := readState(tx.QueryRowContext(ctx, "SELECT state FROM node_route WHERE id=1"))
	if err != nil {
		return err
	}
	if state.Fence.Revision > c.Revision || (state.Fence.Revision == c.Revision && state.Fence != c) {
		return ErrConflict
	}
	if state.Active != nil && (state.Active.OperationID == c.OperationID || state.Active.Backend == c.Backend) {
		return ErrConflict
	}
	state.Fence, state.Status = c, "failed"
	if err = writeState(ctx, tx, state); err != nil {
		return err
	}
	return tx.Commit()
}
