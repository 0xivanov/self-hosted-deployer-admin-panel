// Package publisher connects leased portal jobs to an operator-assigned runtime.
// It is intended for a separate trusted worker process, not a browser handler.
package publisher

import (
	"context"
	"errors"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/portal"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/staticpublish"
)

type Runtime interface {
	Project() string
	Publish(context.Context, int64, []byte) (staticpublish.Status, error)
}
type Worker struct {
	store   *portal.Store
	runtime Runtime
}

func New(store *portal.Store, runtime Runtime) (*Worker, error) {
	if store == nil || runtime == nil || runtime.Project() == "" {
		return nil, errors.New("an assigned store and runtime are required")
	}
	return &Worker{store: store, runtime: runtime}, nil
}

// Once retries the exact immutable revision after an expired lease. Replaying
// Publish rather than trusting a read-only status also finishes directory sync
// after a runtime's uncertain rename outcome. An error never marks the job as
// failed: the remote side may have committed before its response was lost.
func (w *Worker) Once(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	claim, err := w.store.ClaimPublication(ctx, w.runtime.Project())
	if err != nil {
		return false, err
	}
	if claim == nil {
		return false, nil
	}
	if claim.Job.ProjectID != w.runtime.Project() {
		return true, errors.New("runtime assignment mismatch")
	}
	status, err := w.runtime.Publish(ctx, claim.Job.Revision, claim.Archive)
	if err != nil {
		return true, errors.New("publication outcome unresolved; retained for retry")
	}
	if status.Revision != claim.Job.Revision || status.Release != claim.SHA256 {
		return true, errors.New("runtime acknowledgement mismatch; retained for reconciliation")
	}
	if err = w.store.FinishPublication(ctx, claim.Job.ID, claim.Lease, status.Release, true); err != nil {
		return true, err
	}
	return true, nil
}

// Run polls one assigned project. report receives errors without credentials or
// runtime response bodies. Cancellation stops polling and in-flight requests.
func (w *Worker) Run(ctx context.Context, report func(error)) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			_, err := w.Once(ctx)
			if ctx.Err() != nil {
				return
			}
			if err != nil && report != nil {
				report(err)
			}
			timer.Reset(5 * time.Second)
		}
	}
}
