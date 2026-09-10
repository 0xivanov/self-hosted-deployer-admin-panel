package noderouter

import (
	"context"
	"encoding/json"
)

func (r *Router) endBackend(backend string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inFlight[backend]--
	close(r.changed)
	r.changed = make(chan struct{})
}

// WithDrainedBackend permanently fences c, waits for its backend's proxy and
// health operations, then runs action with new activations excluded. Replacement
// content continues serving. This is trusted local control, not a customer API.
//
// action must be bounded, synchronous and must not call router control methods.
// It must not return while a dispatched stop can still execute later. Cancellation
// while waiting leaves the retirement fence but does not invoke action. During
// action the database guard survives cancellation until action returns. A return
// error may follow a completed action: callers must reconcile, not assume no stop.
// This guard covers this canonical router database, not other network ingress or
// service-manager starts. The launcher must separately fence those operations.
func (r *Router) WithDrainedBackend(ctx context.Context, c Candidate, action func(context.Context) error) error {
	if !r.validCandidate(c) || action == nil {
		return ErrInvalid
	}
	if err := r.beginUpstream(); err != nil {
		return err
	}
	defer r.endUpstream()
	if err := r.FenceRetirement(ctx, c); err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		r.mu.Lock()
		busy, changed := r.inFlight[c.Backend] != 0, r.changed
		r.mu.Unlock()
		if busy {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-changed:
				continue
			}
		}
		conn, err := r.db.Conn(ctx)
		if err != nil {
			return err
		}
		// A canceled context must not automatically roll back the guard underneath
		// an action that is still finishing. SQLite lock waits remain bounded.
		tx, err := conn.BeginTx(context.WithoutCancel(ctx), nil)
		if err != nil {
			conn.Close()
			return err
		}
		finish := func() { tx.Rollback(); conn.Close() }
		var raw []byte
		err = tx.QueryRowContext(ctx, "SELECT candidate FROM retired_operations WHERE operation=?", c.OperationID).Scan(&raw)
		var retired Candidate
		if err == nil {
			err = json.Unmarshal(raw, &retired)
		}
		if err != nil {
			finish()
			return err
		}
		if retired != c {
			finish()
			return ErrConflict
		}
		state, err := readState(tx.QueryRowContext(ctx, "SELECT state FROM node_route WHERE id=1"))
		if err != nil {
			finish()
			return err
		}
		if (state.Active != nil && state.Active.Backend == c.Backend) || (state.Status == "pending" && state.Fence.Backend == c.Backend) {
			finish()
			return ErrConflict
		}
		r.mu.Lock()
		busy = r.inFlight[c.Backend] != 0
		r.mu.Unlock()
		if busy {
			finish()
			continue
		}
		if err = ctx.Err(); err != nil {
			finish()
			return err
		}
		// Keep the immediate write reservation, but do not write: separate content
		// connections can read the active replacement while activations are excluded.
		return func() error {
			defer finish()
			return action(ctx)
		}()
	}
}
