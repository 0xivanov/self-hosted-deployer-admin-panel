//go:build integration

package portal

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodebuild"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/npmfetch"
)

type preparationDownload func(context.Context, npmfetch.Tarball) ([]byte, error)

func (f preparationDownload) Fetch(ctx context.Context, t npmfetch.Tarball) ([]byte, error) {
	return f(ctx, t)
}
func prepareFixture(t *testing.T) (*Store, Account, NodeBuild, *os.Root, string) {
	t.Helper()
	s, _ := newStore(t)
	a, session := verifiedAccount(t, s, "prepare-node@example.test")
	p, err := s.CreateProject(t.Context(), session.Token, a.WorkspaceID, "app", "node")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha512.Sum512([]byte("package"))
	lock, _ := json.Marshal(map[string]any{"lockfileVersion": 3, "packages": map[string]any{"": map[string]any{}, "node_modules/a": map[string]any{"resolved": "https://registry.npmjs.org/a/-/a.tgz", "integrity": "sha512-" + base64.StdEncoding.EncodeToString(sum[:])}}})
	var data bytes.Buffer
	z := zip.NewWriter(&data)
	for _, f := range []struct{ name, body string }{{"package.json", `{"scripts":{"start":"node server.js"}}`}, {"package-lock.json", string(lock)}} {
		w, e := z.Create(f.name)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = w.Write([]byte(f.body)); e != nil {
			t.Fatal(e)
		}
	}
	if err = z.Close(); err != nil {
		t.Fatal(err)
	}
	upload, err := s.SaveUpload(t.Context(), session.Token, p.ID, data.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	j, err := s.RequestNodeBuild(t.Context(), session.Token, p.ID, upload.ID, strings.Repeat("r", 16), NodeBuildAssignment{Settings: nodebuild.Settings{Architecture: "arm64"}, ToolchainSHA256: strings.Repeat("a", 64)})
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
	return s, a, j, root, dir
}
func TestPrepareNodeBuildConnectsClaimDownloadAndBinding(t *testing.T) {
	t.Parallel()
	s, _, job, root, _ := prepareFixture(t)
	calls := 0
	p, err := s.PrepareNodeBuild(t.Context(), job.ProjectID, job.ToolchainSHA256, "arm64", root, preparationDownload(func(context.Context, npmfetch.Tarball) ([]byte, error) { calls++; return []byte("package"), nil }))
	if err != nil || p == nil || p.Bundle == nil || p.Claim.Job.ID != job.ID || calls != 1 {
		t.Fatal(p, err, calls)
	}
	saved, err := s.NodeBuildDependencies(t.Context(), job.ID, p.Claim.ExecutionID, p.Claim.Lease)
	if err != nil || saved == nil || *saved != *p.Bundle {
		t.Fatal(saved, err)
	}
	if _, err = npmfetch.VerifyBundle(t.Context(), root, *saved, p.Claim.Job.Plan.SourceSHA256); err != nil {
		t.Fatal(err)
	}
	next, err := s.PrepareNodeBuild(t.Context(), job.ProjectID, job.ToolchainSHA256, "arm64", root, emptyDependencyDownloader{})
	if err != nil || next != nil {
		t.Fatal("running build redispatched", next, err)
	}
}
func TestPrepareNodeBuildRenewalRevocationAndFailure(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"renewal", "revoked", "provider"} {
		t.Run(mode, func(t *testing.T) {
			s, a, job, root, dir := prepareFixture(t)
			reader := preparationDownload(func(ctx context.Context, _ npmfetch.Tarball) ([]byte, error) {
				if mode == "provider" {
					return nil, errors.New("provider unavailable")
				}
				if mode == "revoked" {
					if _, err := s.db.Exec("UPDATE users SET disabled=1 WHERE id=?", a.ID); err != nil {
						t.Fatal(err)
					}
					select {
					case <-ctx.Done():
						return nil, ctx.Err()
					case <-time.After(time.Second):
						return nil, errors.New("renewal failed to cancel")
					}
				}
				// Force a near-expiry timestamp, then observe an actual heartbeat update.
				until := s.now().Unix() + 2
				if _, err := s.db.Exec("UPDATE node_builds SET lease_until=? WHERE id=?", until, job.ID); err != nil {
					t.Fatal(err)
				}
				deadline := time.Now().Add(time.Second)
				for time.Now().Before(deadline) {
					var current int64
					if err := s.db.QueryRow("SELECT lease_until FROM node_builds WHERE id=?", job.ID).Scan(&current); err != nil {
						t.Fatal(err)
					}
					if current > until {
						return []byte("package"), nil
					}
					time.Sleep(time.Millisecond)
				}
				return nil, errors.New("lease did not renew")
			})
			p, err := s.prepareNodeBuild(t.Context(), job.ProjectID, job.ToolchainSHA256, "arm64", root, reader, 5*time.Millisecond)
			if p == nil || p.Claim == nil || p.Claim.ExecutionID == "" {
				t.Fatal("lost execution identity", p, err)
			}
			if mode == "renewal" {
				if err != nil || p.Bundle == nil {
					t.Fatal(p, err)
				}
				return
			}
			if err == nil || p.Bundle != nil {
				t.Fatal(p, err)
			}
			if mode == "revoked" && !errors.Is(err, ErrDenied) {
				t.Fatal(err)
			}
			files, e := os.ReadDir(dir)
			if e != nil || len(files) != 0 {
				t.Fatal("partial dependency directory remains", files, e)
			}
			var state string
			if e = s.db.QueryRow("SELECT state FROM node_builds WHERE id=?", job.ID).Scan(&state); e != nil || state != "running" {
				t.Fatal("uncertain execution released", state, e)
			}
		})
	}
}
