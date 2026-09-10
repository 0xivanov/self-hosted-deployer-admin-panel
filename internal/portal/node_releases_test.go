//go:build integration

package portal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodebuild"
)

type artifactReader func(context.Context, string) (NodeArtifactObservation, []byte, error)

func (f artifactReader) ReadNodeArtifact(ctx context.Context, id string) (NodeArtifactObservation, []byte, error) {
	return f(ctx, id)
}

func releaseReader(s *Store, c *NodeBuildClaim, manifest string) artifactReader {
	return func(context.Context, string) (NodeArtifactObservation, []byte, error) {
		return NodeArtifactObservation{NodeExecutionObservation: NodeExecutionObservation{ExecutionID: c.ExecutionID, SourceSHA256: c.Job.Plan.SourceSHA256, ToolchainSHA256: c.Job.ToolchainSHA256, Architecture: c.Job.Plan.Architecture, Outcome: "succeeded", Retired: true, ObservedAt: s.now()}, ArtifactSHA256: c.Job.Plan.SourceSHA256, DependencyManifestSHA256: manifest}, c.Archive, nil
	}
}
func TestNodeReleasePersistsWithAtomicCompletionAndTenantAccess(t *testing.T) {
	t.Parallel()
	s, path, a, c, root, bundle := dependencyFixture(t)
	ctx := t.Context()
	session, err := s.Login(ctx, a.Email, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BindNodeBuildDependencies(ctx, c.Job.ID, c.ExecutionID, c.Lease, root, bundle); err != nil {
		t.Fatal(err)
	}
	if err = s.DispatchNodeBuild(ctx, c.Job.ID, c.ExecutionID, c.Lease, root, nodeSubmit(func(context.Context, NodeExecutionRequest) error { return nil })); err != nil {
		t.Fatal(err)
	}
	// Recovery must not depend on keeping the old worker lease alive.
	now := s.now().Add(2 * time.Minute)
	s.now = func() time.Time { return now }
	release, err := s.RetainNodeRelease(ctx, releaseReader(s, c, bundle.ManifestSHA256), c.Job.ProjectID, c.Job.ID)
	if err != nil || release == nil || release.ArtifactSHA256 != c.Job.Plan.SourceSHA256 || release.BuildID != c.Job.ID {
		t.Fatal(release, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	repeated, err := reopened.RetainNodeRelease(ctx, artifactReader(func(context.Context, string) (NodeArtifactObservation, []byte, error) {
		t.Fatal("retained release called executor again")
		return NodeArtifactObservation{}, nil, nil
	}), c.Job.ProjectID, c.Job.ID)
	if err != nil || repeated == nil || *repeated != *release {
		t.Fatal(repeated, err)
	}
	data, err := reopened.NodeReleaseArchive(ctx, session.Token, c.Job.ProjectID, c.Job.ID)
	if err != nil || !bytes.Equal(data, c.Archive) {
		t.Fatal(err)
	}
	history, err := reopened.NodeReleases(ctx, session.Token, c.Job.ProjectID)
	if err != nil || len(history) != 1 || history[0] != *release {
		t.Fatal(history, err)
	}
	var state, lease string
	if err = reopened.db.QueryRow("SELECT state,lease_hash FROM node_builds WHERE id=?", c.Job.ID).Scan(&state, &lease); err != nil || state != "succeeded" || lease != "" {
		t.Fatal(state, lease, err)
	}
	_, other := verifiedAccount(t, reopened, "other-release@example.test")
	if _, err = reopened.NodeReleaseArchive(ctx, other.Token, c.Job.ProjectID, c.Job.ID); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	if _, err = reopened.NodeReleases(ctx, other.Token, c.Job.ProjectID); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	if err = reopened.DeleteNodeRelease(ctx, other.Token, c.Job.ProjectID, c.Job.ID); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	corrupted := bytes.Clone(c.Archive)
	corrupted[0] ^= 1
	if _, err = reopened.db.Exec("UPDATE node_releases SET archive=? WHERE build_id=?", corrupted, c.Job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = reopened.NodeReleaseArchive(ctx, session.Token, c.Job.ProjectID, c.Job.ID); err == nil {
		t.Fatal("corrupted retained archive returned")
	}
	if _, err = reopened.db.Exec("UPDATE memberships SET role='viewer' WHERE user_id=?", a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = reopened.NodeReleaseArchive(ctx, session.Token, c.Job.ProjectID, c.Job.ID); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
}
func TestNodeReleaseRejectsUnprovenArtifacts(t *testing.T) {
	t.Parallel()
	s, _, _, c, root, bundle := dependencyFixture(t)
	ctx := t.Context()
	reader := releaseReader(s, c, bundle.ManifestSHA256)
	if _, err := s.RetainNodeRelease(ctx, reader, c.Job.ProjectID, c.Job.ID); !errors.Is(err, ErrBuildConflict) {
		t.Fatal("undispatched accepted", err)
	}
	if err := s.BindNodeBuildDependencies(ctx, c.Job.ID, c.ExecutionID, c.Lease, root, bundle); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchNodeBuild(ctx, c.Job.ID, c.ExecutionID, c.Lease, root, nodeSubmit(func(context.Context, NodeExecutionRequest) error { return nil })); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"execution", "source", "toolchain", "architecture", "bundle", "digest", "running", "failed", "stale", "future", "corrupt", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			bad := artifactReader(func(ctx context.Context, id string) (NodeArtifactObservation, []byte, error) {
				o, data, err := reader(ctx, id)
				switch mode {
				case "execution":
					o.ExecutionID = "other"
				case "source":
					o.SourceSHA256 = "other"
				case "toolchain":
					o.ToolchainSHA256 = "other"
				case "architecture":
					o.Architecture = "amd64"
				case "bundle":
					o.DependencyManifestSHA256 = "other"
				case "digest":
					o.ArtifactSHA256 = "other"
				case "running":
					o.Retired = false
				case "failed":
					o.Outcome = "failed"
				case "stale":
					o.ObservedAt = s.now().Add(-time.Second)
				case "future":
					o.ObservedAt = s.now().Add(time.Second)
				case "corrupt":
					data = []byte("corrupt archive")
				case "unavailable":
					err = errors.New("provider unavailable")
				}
				return o, data, err
			})
			if release, err := s.RetainNodeRelease(ctx, bad, c.Job.ProjectID, c.Job.ID); err == nil || release != nil {
				t.Fatal(release, err)
			}
			var state string
			var count int
			if err := s.db.QueryRow("SELECT state FROM node_builds WHERE id=?", c.Job.ID).Scan(&state); err != nil || state != "running" {
				t.Fatal(state, err)
			}
			if err := s.db.QueryRow("SELECT count(*) FROM node_releases").Scan(&count); err != nil || count != 0 {
				t.Fatal(count, err)
			}
		})
	}
}
func TestNodeReleaseRevocationDiscardsRetiredOutput(t *testing.T) {
	t.Parallel()
	s, _, a, c, root, bundle := dependencyFixture(t)
	ctx := t.Context()
	if err := s.BindNodeBuildDependencies(ctx, c.Job.ID, c.ExecutionID, c.Lease, root, bundle); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchNodeBuild(ctx, c.Job.ID, c.ExecutionID, c.Lease, root, nodeSubmit(func(context.Context, NodeExecutionRequest) error { return nil })); err != nil {
		t.Fatal(err)
	}
	reader := artifactReader(func(ctx context.Context, id string) (NodeArtifactObservation, []byte, error) {
		if _, err := s.db.Exec("UPDATE users SET disabled=1 WHERE id=?", a.ID); err != nil {
			t.Fatal(err)
		}
		return releaseReader(s, c, bundle.ManifestSHA256)(ctx, id)
	})
	release, err := s.RetainNodeRelease(ctx, reader, c.Job.ProjectID, c.Job.ID)
	if err != nil || release != nil {
		t.Fatal(release, err)
	}
	var state string
	var count int
	if err = s.db.QueryRow("SELECT state FROM node_builds WHERE id=?", c.Job.ID).Scan(&state); err != nil || state != "cancelled" {
		t.Fatal(state, err)
	}
	if err = s.db.QueryRow("SELECT count(*) FROM node_releases").Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}
func TestNodeReleaseRetentionCountAndMigration(t *testing.T) {
	t.Parallel()
	s, path, a, c, root, bundle := dependencyFixture(t)
	ctx := t.Context()
	session, err := s.Login(ctx, a.Email, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BindNodeBuildDependencies(ctx, c.Job.ID, c.ExecutionID, c.Lease, root, bundle); err != nil {
		t.Fatal(err)
	}
	if err = s.DispatchNodeBuild(ctx, c.Job.ID, c.ExecutionID, c.Lease, root, nodeSubmit(func(context.Context, NodeExecutionRequest) error { return nil })); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("DROP TABLE node_deployment_releases; DROP TABLE node_deployments; DROP TABLE node_releases; PRAGMA user_version=20"); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := 0; i < 11; i++ {
		if i > 0 {
			if _, err = s.RequestNodeBuild(ctx, session.Token, c.Job.ProjectID, c.Job.UploadID, fmt.Sprintf("node-release-quota-%02d", i), NodeBuildAssignment{Settings: nodebuild.Settings{Architecture: "arm64"}, ToolchainSHA256: c.Job.ToolchainSHA256}); err != nil {
				t.Fatal(err)
			}
			prepared, e := s.PrepareNodeBuild(ctx, c.Job.ProjectID, c.Job.ToolchainSHA256, "arm64", root, emptyDependencyDownloader{})
			if e != nil {
				t.Fatal(e)
			}
			c = prepared.Claim
			bundle = *prepared.Bundle
			if err = s.DispatchNodeBuild(ctx, c.Job.ID, c.ExecutionID, c.Lease, root, nodeSubmit(func(context.Context, NodeExecutionRequest) error { return nil })); err != nil {
				t.Fatal(err)
			}
		}
		release, e := s.RetainNodeRelease(ctx, releaseReader(s, c, bundle.ManifestSHA256), c.Job.ProjectID, c.Job.ID)
		if i < 10 {
			if e != nil || release == nil {
				t.Fatal(i, release, e)
			}
		} else if !errors.Is(e, ErrBuildQuota) || release != nil {
			t.Fatal(i, release, e)
		}
	}
	history, err := s.NodeReleases(ctx, session.Token, c.Job.ProjectID)
	if err != nil || len(history) != 10 {
		t.Fatal(len(history), err)
	}
	// Reclaim one archive and retry the already-retired pending build. No rebuild.
	if err = s.DeleteNodeRelease(ctx, session.Token, c.Job.ProjectID, history[0].BuildID); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteNodeRelease(ctx, session.Token, c.Job.ProjectID, history[0].BuildID); err != nil {
		t.Fatal("non-idempotent deletion", err)
	}
	if _, err = s.NodeReleaseArchive(ctx, session.Token, c.Job.ProjectID, history[0].BuildID); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	if release, err := s.RetainNodeRelease(ctx, releaseReader(s, c, bundle.ManifestSHA256), c.Job.ProjectID, c.Job.ID); err != nil || release == nil {
		t.Fatal(release, err)
	}
}

func TestConcurrentNodeReleaseRetentionIsIdempotent(t *testing.T) {
	t.Parallel()
	s, _, _, c, root, bundle := dependencyFixture(t)
	ctx := t.Context()
	if err := s.BindNodeBuildDependencies(ctx, c.Job.ID, c.ExecutionID, c.Lease, root, bundle); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchNodeBuild(ctx, c.Job.ID, c.ExecutionID, c.Lease, root, nodeSubmit(func(context.Context, NodeExecutionRequest) error { return nil })); err != nil {
		t.Fatal(err)
	}
	type result struct {
		release *NodeRelease
		err     error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			r, e := s.RetainNodeRelease(ctx, releaseReader(s, c, bundle.ManifestSHA256), c.Job.ProjectID, c.Job.ID)
			results <- result{r, e}
		})
	}
	wg.Wait()
	close(results)
	var previous *NodeRelease
	for result := range results {
		if result.err != nil || result.release == nil {
			t.Fatal(result)
		}
		if previous != nil && *previous != *result.release {
			t.Fatal("two different retained releases")
		}
		previous = result.release
	}
	var count int
	if err := s.db.QueryRow("SELECT count(*) FROM node_releases").Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
}
