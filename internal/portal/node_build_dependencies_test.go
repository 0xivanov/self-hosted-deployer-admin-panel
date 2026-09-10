//go:build integration

package portal

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/npmfetch"
)

type emptyDependencyDownloader struct{}

func (emptyDependencyDownloader) Fetch(context.Context, npmfetch.Tarball) ([]byte, error) {
	return nil, errors.New("dependency-free fixture must not fetch")
}
func dependencyFixture(t *testing.T) (*Store, string, Account, *NodeBuildClaim, *os.Root, npmfetch.Bundle) {
	t.Helper()
	s, path, a, _, job := buildClaimFixture(t)
	c, err := s.ClaimNodeBuild(t.Context(), job.ProjectID, job.ToolchainSHA256, "arm64")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err = os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	bundle, err := npmfetch.DownloadBundle(t.Context(), c.Archive, c.Job.Plan.SourceSHA256, root, emptyDependencyDownloader{})
	if err != nil {
		t.Fatal(err)
	}
	return s, path, a, c, root, bundle
}
func TestBuildDependencyBindingPersistsAndCannotChange(t *testing.T) {
	t.Parallel()
	s, path, _, c, root, bundle := dependencyFixture(t)
	ctx := t.Context()
	before, err := s.NodeBuildDependencies(ctx, c.Job.ID, c.ExecutionID, c.Lease)
	if err != nil || before != nil {
		t.Fatal(before, err)
	}
	for i := 0; i < 2; i++ {
		if err = s.BindNodeBuildDependencies(ctx, c.Job.ID, c.ExecutionID, c.Lease, root, bundle); err != nil {
			t.Fatal(err)
		}
	}
	other, err := npmfetch.DownloadBundle(ctx, c.Archive, c.Job.Plan.SourceSHA256, root, emptyDependencyDownloader{})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BindNodeBuildDependencies(ctx, c.Job.ID, c.ExecutionID, c.Lease, root, other); !errors.Is(err, ErrBuildConflict) {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	saved, err := reopened.NodeBuildDependencies(ctx, c.Job.ID, c.ExecutionID, c.Lease)
	if err != nil || saved == nil || *saved != bundle {
		t.Fatal(saved, err)
	}
	if _, err = reopened.NodeBuildDependencies(ctx, c.Job.ID, "foreign", c.Lease); !errors.Is(err, ErrBuildLease) {
		t.Fatal(err)
	}
}
func TestBuildDependencyBindingRejectsRevocationAndExpiry(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"revoked", "expires-during-verification", "corrupt"} {
		t.Run(mode, func(t *testing.T) {
			s, _, a, c, root, bundle := dependencyFixture(t)
			ctx := t.Context()
			switch mode {
			case "revoked":
				if _, err := s.db.Exec("UPDATE users SET disabled=1 WHERE id=?", a.ID); err != nil {
					t.Fatal(err)
				}
			case "expires-during-verification":
				now := s.now()
				calls := 0
				s.now = func() time.Time {
					calls++
					if calls > 1 {
						return now.Add(2 * time.Minute)
					}
					return now
				}
			case "corrupt":
				if err := root.WriteFile(bundle.Directory+"/bundle.json", []byte("bad"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.BindNodeBuildDependencies(ctx, c.Job.ID, c.ExecutionID, c.Lease, root, bundle); err == nil {
				t.Fatal("invalid binding accepted")
			}
			var raw []byte
			if err := s.db.QueryRow("SELECT dependency_bundle FROM node_builds WHERE id=?", c.Job.ID).Scan(&raw); err != nil || len(raw) != 0 {
				t.Fatal("binding written after rejection", err)
			}
		})
	}
}

func TestBuildDependencyBindingRejectsDifferentLockfileSet(t *testing.T) {
	t.Parallel()
	s, _, _, c, root, bundle := dependencyFixture(t)
	payload := []byte("unexpected package")
	hash := sha256.Sum256(payload)
	sri := sha512.Sum512(payload)
	file := hex.EncodeToString(hash[:]) + ".tgz"
	manifest := npmfetch.Manifest{SourceSHA256: c.Job.Plan.SourceSHA256, Tarballs: []npmfetch.StoredTarball{{URL: "https://registry.npmjs.org/a/-/a.tgz", File: file, Integrity: "sha512-" + base64.StdEncoding.EncodeToString(sri[:]), Bytes: int64(len(payload))}}}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err = root.WriteFile(bundle.Directory+"/"+file, payload, 0600); err != nil {
		t.Fatal(err)
	}
	if err = root.WriteFile(bundle.Directory+"/bundle.json", raw, 0600); err != nil {
		t.Fatal(err)
	}
	hash = sha256.Sum256(raw)
	bundle.ManifestSHA256 = hex.EncodeToString(hash[:])
	// It is internally valid but does not represent the source lockfile.
	if _, err = npmfetch.VerifyBundle(t.Context(), root, bundle, c.Job.Plan.SourceSHA256); err != nil {
		t.Fatal(err)
	}
	if err = s.BindNodeBuildDependencies(t.Context(), c.Job.ID, c.ExecutionID, c.Lease, root, bundle); !errors.Is(err, ErrBuildConflict) {
		t.Fatal(err)
	}
}
