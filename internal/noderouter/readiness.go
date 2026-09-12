package noderouter

import (
	"context"
	"net/http"
	"time"
)

// WaitHealthy waits for a newly started backend without changing the live route.
// Activate still performs its own final health check and durable revision fence.
func (r *Router) WaitHealthy(ctx context.Context, c Candidate) error {
	if !r.validCandidate(c) {
		return ErrInvalid
	}
	if err := r.beginUpstream(); err != nil {
		return err
	}
	defer r.endUpstream()
	r.mu.Lock()
	r.inFlight[c.Backend]++
	r.mu.Unlock()
	defer r.endBackend(c.Backend)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	target := *r.backends[c.Backend]
	target.Path = r.config.HealthPath
	for {
		var retired int
		if err := r.readDB.QueryRowContext(ctx, "SELECT count(*) FROM retired_operations WHERE operation=?", c.OperationID).Scan(&retired); err != nil {
			return err
		}
		if retired != 0 {
			return ErrRetired
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
		if err != nil {
			return err
		}
		request.Host = r.config.ContentHost
		response, err := r.health.Do(request)
		healthy := err == nil && response.StatusCode >= 200 && response.StatusCode < 300
		if response != nil {
			response.Body.Close()
		}
		if healthy {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}
