//go:build integration

package portal

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/npmfetch"
)

type workerBuildExecutor struct {
	s       *Store
	request NodeExecutionRequest
	submits int
	lost    bool
}

func TestWorkNodeBuildExpiresUnsubmittedPreparation(t *testing.T) {
	t.Parallel()
	s, _, job, root, _ := prepareFixture(t)
	claim, err := s.ClaimNodeBuild(t.Context(), job.ProjectID, job.ToolchainSHA256, "arm64")
	if err != nil || claim == nil {
		t.Fatal(claim, err)
	}
	executor := &workerBuildExecutor{s: s}
	download := preparationDownload(func(context.Context, npmfetch.Tarball) ([]byte, error) {
		t.Fatal("preparation restarted")
		return nil, nil
	})
	worked, err := s.WorkNodeBuild(t.Context(), job.ProjectID, job.ToolchainSHA256, "arm64", root, download, executor)
	if err != nil || worked {
		t.Fatal("live preparation interrupted", worked, err)
	}
	now := s.now().Add(2 * time.Minute)
	s.now = func() time.Time { return now }
	worked, err = s.WorkNodeBuild(t.Context(), job.ProjectID, job.ToolchainSHA256, "arm64", root, download, executor)
	if err != nil || !worked || executor.submits != 0 {
		t.Fatal(worked, err)
	}
	var state string
	if err = s.db.QueryRow("SELECT state FROM node_builds WHERE id=?", job.ID).Scan(&state); err != nil || state != "failed" {
		t.Fatal(state, err)
	}
	if _, err = s.RenewNodeBuildLease(t.Context(), job.ID, claim.ExecutionID, claim.Lease); !errors.Is(err, ErrBuildLease) {
		t.Fatal("old worker still authorized", err)
	}
}

func (e *workerBuildExecutor) SubmitNodeExecution(_ context.Context, r NodeExecutionRequest) error {
	e.request = r
	e.submits++
	if e.lost {
		return errors.New("accepted reply lost")
	}
	return nil
}
func (e *workerBuildExecutor) InspectNodeExecution(_ context.Context, id string) (NodeExecutionObservation, error) {
	r := e.request
	return NodeExecutionObservation{ExecutionID: r.ExecutionID, SourceSHA256: r.Plan.SourceSHA256, ToolchainSHA256: r.ToolchainSHA256, Architecture: r.Plan.Architecture, Outcome: "succeeded", Retired: true, ObservedAt: e.s.now()}, nil
}
func (e *workerBuildExecutor) ReadNodeArtifact(ctx context.Context, id string) (NodeArtifactObservation, []byte, error) {
	o, err := e.InspectNodeExecution(ctx, id)
	return NodeArtifactObservation{NodeExecutionObservation: o, ArtifactSHA256: e.request.Plan.SourceSHA256, DependencyManifestSHA256: e.request.Bundle.ManifestSHA256}, e.request.Archive, err
}

func TestWorkNodeBuildRetainsReleaseWithoutDuplicateExecution(t *testing.T) {
	t.Parallel()
	for _, lost := range []bool{false, true} {
		name := "accepted"
		if lost {
			name = "lost_reply"
		}
		t.Run(name, func(t *testing.T) {
			s, a, job, root, _ := prepareFixture(t)
			executor := &workerBuildExecutor{s: s, lost: lost}
			downloads := 0
			download := preparationDownload(func(context.Context, npmfetch.Tarball) ([]byte, error) { downloads++; return []byte("package"), nil })
			worked, err := s.WorkNodeBuild(t.Context(), job.ProjectID, job.ToolchainSHA256, "arm64", root, download, executor)
			if !worked || (err != nil) != lost {
				t.Fatal(worked, err)
			}
			if lost {
				// Subsequent work must observe and retain the accepted execution.
				worked, err = s.WorkNodeBuild(t.Context(), job.ProjectID, job.ToolchainSHA256, "arm64", root, download, executor)
				if err != nil || !worked {
					t.Fatal(worked, err)
				}
			}
			session, err := s.Login(t.Context(), a.Email, testPassword)
			if err != nil {
				t.Fatal(err)
			}
			releases, err := s.NodeReleases(t.Context(), session.Token, job.ProjectID)
			if err != nil || len(releases) != 1 || releases[0].BuildID != job.ID {
				t.Fatal(releases, err)
			}
			worked, err = s.WorkNodeBuild(t.Context(), job.ProjectID, job.ToolchainSHA256, "arm64", root, download, executor)
			if err != nil || worked || executor.submits != 1 || downloads != 1 {
				t.Fatal(worked, err, executor.submits, downloads)
			}
		})
	}
}
