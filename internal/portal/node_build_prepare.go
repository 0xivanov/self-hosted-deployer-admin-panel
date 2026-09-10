package portal

import (
	"context"
	"os"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/npmfetch"
)

type PreparedNodeBuild struct {
	Claim  *NodeBuildClaim
	Bundle *npmfetch.Bundle
}

// PrepareNodeBuild claims one queued build and persists its verified dependency
// bundle. It does not dispatch a VM or run npm. Even on error a non-nil result
// retains the execution identity and any completed bundle for reconciliation.
// Callers must not discard that identity or automatically redispatch the job.
func (s *Store) PrepareNodeBuild(ctx context.Context, project, toolchain, architecture string, jobs *os.Root, downloader npmfetch.Downloader) (*PreparedNodeBuild, error) {
	return s.prepareNodeBuild(ctx, project, toolchain, architecture, jobs, downloader, 20*time.Second)
}
func (s *Store) prepareNodeBuild(ctx context.Context, project, toolchain, architecture string, jobs *os.Root, downloader npmfetch.Downloader, heartbeat time.Duration) (*PreparedNodeBuild, error) {
	if jobs == nil || downloader == nil || heartbeat <= 0 {
		return nil, ErrInvalid
	}
	info, err := jobs.Stat(".")
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, ErrInvalid
	}
	claim, err := s.ClaimNodeBuild(ctx, project, toolchain, architecture)
	if err != nil || claim == nil {
		return nil, err
	}
	prepared := &PreparedNodeBuild{Claim: claim}
	work, cancel := context.WithCancel(ctx)
	defer cancel()
	stopRenewal := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(heartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-stopRenewal:
				done <- nil
				return
			case <-work.Done():
				done <- nil
				return
			case <-ticker.C:
				if _, e := s.RenewNodeBuildLease(work, claim.Job.ID, claim.ExecutionID, claim.Lease); e != nil {
					done <- e
					cancel()
					return
				}
			}
		}
	}()
	bundle, err := npmfetch.DownloadBundle(work, claim.Archive, claim.Job.Plan.SourceSHA256, jobs, downloader)
	if err == nil {
		prepared.Bundle = &bundle
		err = s.BindNodeBuildDependencies(work, claim.Job.ID, claim.ExecutionID, claim.Lease, jobs, bundle)
	}
	// Let an in-flight renewal finish before cancelling its context. Otherwise
	// a successful preparation can manufacture a cancellation error at handoff.
	close(stopRenewal)
	renewalErr := <-done
	cancel()
	if renewalErr != nil {
		return prepared, renewalErr
	}
	if err != nil {
		return prepared, err
	}
	// Refresh the handoff window once background renewal has stopped. The next
	// executor stage must take responsibility for renewal before it dispatches.
	until, err := s.RenewNodeBuildLease(ctx, claim.Job.ID, claim.ExecutionID, claim.Lease)
	if err != nil {
		return prepared, err
	}
	claim.LeaseUntil = until
	return prepared, nil
}
