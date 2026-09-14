//go:build integration

package portal

import (
	"context"
	"errors"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/npmfetch"
	"testing"
	"time"
)

func TestPendingNodeBuildPreservesDispatchedIdentity(t *testing.T) {
	t.Parallel()
	s, _, job, root, _ := prepareFixture(t)
	download := preparationDownload(func(context.Context, npmfetch.Tarball) ([]byte, error) { return []byte("package"), nil })
	p, err := s.PrepareNodeBuild(t.Context(), job.ProjectID, job.ToolchainSHA256, "arm64", root, download)
	if err != nil || p == nil {
		t.Fatal(err)
	}
	before, err := s.PendingNodeBuildExecution(t.Context(), job.ProjectID, job.ToolchainSHA256, "arm64")
	if err != nil || before != nil {
		t.Fatal(before, err)
	}
	executor := &workerBuildExecutor{s: s, lost: true}
	err = s.DispatchNodeBuild(t.Context(), p.Claim.Job.ID, p.Claim.ExecutionID, p.Claim.Lease, root, executor)
	if err == nil {
		t.Fatal("fixture must lose accepted response")
	}
	// Even after its execution lease expires, a submitted request is only observed.
	now := s.now().Add(2 * time.Minute)
	s.now = func() time.Time { return now }
	for range 2 {
		request, err := s.PendingNodeBuildExecution(t.Context(), job.ProjectID, job.ToolchainSHA256, "arm64")
		if err != nil || request == nil || request.ExecutionID != executor.request.ExecutionID || request.BuildID != job.ID || request.NotAfter != executor.request.NotAfter {
			t.Fatal(request, err)
		}
	}
	if _, err = s.PendingNodeBuildExecution(t.Context(), job.ProjectID, job.ToolchainSHA256, "amd64"); !errors.Is(err, ErrBuildConflict) {
		t.Fatal(err)
	}
	other, err := s.PendingNodeBuildExecution(t.Context(), "other-project", job.ToolchainSHA256, "arm64")
	if err != nil || other != nil {
		t.Fatal(other, err)
	}
	if executor.submits != 1 {
		t.Fatal("redispatch")
	}
	var state string
	if err = s.db.QueryRow("SELECT state FROM node_builds WHERE id=?", job.ID).Scan(&state); err != nil || state != "running" {
		t.Fatal(state, err)
	}
}
func TestPendingNodeBuildFencesExpiredPreparation(t *testing.T) {
	t.Parallel()
	s, _, job, _, _ := prepareFixture(t)
	c, err := s.ClaimNodeBuild(t.Context(), job.ProjectID, job.ToolchainSHA256, "arm64")
	if err != nil || c == nil {
		t.Fatal(err)
	}
	now := s.now().Add(2 * time.Minute)
	s.now = func() time.Time { return now }
	request, err := s.PendingNodeBuildExecution(t.Context(), job.ProjectID, job.ToolchainSHA256, "arm64")
	if err != nil || request != nil {
		t.Fatal(request, err)
	}
	if _, err = s.RenewNodeBuildLease(t.Context(), job.ID, c.ExecutionID, c.Lease); !errors.Is(err, ErrBuildLease) {
		t.Fatal("expired preparer still permitted", err)
	}
	var state string
	if err = s.db.QueryRow("SELECT state FROM node_builds WHERE id=?", job.ID).Scan(&state); err != nil || state != "failed" {
		t.Fatal(state, err)
	}
}
