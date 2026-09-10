//go:build integration

package portal

import (
	"archive/zip"
	"bytes"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/staticsite"
)

func publicationFixture(t *testing.T) (*Store, string, Account, Session, Project, Upload) {
	t.Helper()
	s, path := newStore(t)
	a, session := verifiedAccount(t, s, "publisher@example.test")
	p, err := s.CreateProject(t.Context(), session.Token, a.WorkspaceID, "website", "static")
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.SaveUpload(t.Context(), session.Token, p.ID, testArchive(t))
	if err != nil {
		t.Fatal(err)
	}
	return s, path, a, session, p, u
}
func TestPublicationIdempotencyTenantAndRetention(t *testing.T) {
	t.Parallel()
	s, _, _, session, p, u := publicationFixture(t)
	ctx := t.Context()
	_, other := verifiedAccount(t, s, "foreign-publisher@example.test")
	if _, err := s.RequestPublication(ctx, other.Token, p.ID, u.ID, "foreign-request-key"); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	j, err := s.RequestPublication(ctx, session.Token, p.ID, u.ID, "first-request-key")
	if err != nil {
		t.Fatal(err)
	}
	repeat, err := s.RequestPublication(ctx, session.Token, p.ID, u.ID, "first-request-key")
	if err != nil || repeat != j {
		t.Fatal(repeat, err)
	}
	if _, err = s.RequestPublication(ctx, session.Token, p.ID, "different-upload", "first-request-key"); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if _, err = s.RequestPublication(ctx, session.Token, p.ID, u.ID, "second-request-key"); !errors.Is(err, ErrPublishing) {
		t.Fatal(err)
	}
	if err = s.DeleteUpload(ctx, session.Token, p.ID, u.ID); !errors.Is(err, ErrRetained) {
		t.Fatal(err)
	}
	if _, _, err = s.PublicationJobs(ctx, other.Token, p.ID); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
}
func TestPublicationLeaseRecoveryAndRevocation(t *testing.T) {
	t.Parallel()
	s, path, a, session, p, u := publicationFixture(t)
	ctx := t.Context()
	now := time.Now()
	s.now = func() time.Time { return now }
	j, err := s.RequestPublication(ctx, session.Token, p.ID, u.ID, "restart-request-key")
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.ClaimPublication(ctx, p.ID)
	if err != nil || first == nil {
		t.Fatal(err)
	}
	if next, err := s.ClaimPublication(ctx, p.ID); err != nil || next != nil {
		t.Fatal("double claimed", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now = now.Add(61 * time.Second)
	s.now = func() time.Time { return now }
	next, err := s.ClaimPublication(ctx, p.ID)
	if err != nil || next == nil || next.Job.ID != j.ID || next.Job.Attempts != 2 {
		t.Fatal(next, err)
	}
	if err = s.FinishPublication(ctx, j.ID, first.Lease, first.SHA256, true); !errors.Is(err, ErrConflict) {
		t.Fatal("stale lease accepted", err)
	}
	if err = s.FinishPublication(ctx, j.ID, next.Lease, "wrong-hash", true); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("UPDATE memberships SET role='viewer' WHERE user_id=?", a.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishPublication(ctx, j.ID, next.Lease, next.SHA256, true); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	now = now.Add(61 * time.Second)
	if c, err := s.ClaimPublication(ctx, p.ID); !errors.Is(err, ErrDenied) || c != nil {
		t.Fatal(c, err)
	}
	jobs, active, err := s.PublicationJobs(ctx, session.Token, p.ID)
	if err != nil || active != "" || len(jobs) != 1 || jobs[0].State != "running" {
		t.Fatal(jobs, active, err)
	}
}
func TestPublicationStaticRuntimeAndRollback(t *testing.T) {
	t.Parallel()
	s, _, _, session, p, u := publicationFixture(t)
	ctx := t.Context()
	runtime, err := staticsite.Open(filepath.Join(t.TempDir(), "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	publish := func(upload, key string) PublicationJob {
		t.Helper()
		j, err := s.RequestPublication(ctx, session.Token, p.ID, upload, key)
		if err != nil {
			t.Fatal(err)
		}
		c, err := s.ClaimPublication(ctx, p.ID)
		if err != nil || c == nil {
			t.Fatal(err)
		}
		hash, err := runtime.PublishRevision(ctx, c.Job.Revision, c.Archive)
		if err != nil {
			t.Fatal(err)
		}
		if err = s.FinishPublication(ctx, c.Job.ID, c.Lease, hash, true); err != nil {
			t.Fatal(err)
		}
		return j
	}
	defer runtime.Close()
	first := publish(u.ID, "first-release-key")
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	f, err := z.Create("index.html")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write([]byte("second release")); err != nil {
		t.Fatal(err)
	}
	if err = z.Close(); err != nil {
		t.Fatal(err)
	}
	secondUpload, err := s.SaveUpload(ctx, session.Token, p.ID, b.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	second := publish(secondUpload.ID, "second-release-key")
	if first.Revision >= second.Revision || runtime.Active() != secondUpload.SHA256 {
		t.Fatal("second release not applied")
	}
	if _, err = s.RequestPublication(ctx, session.Token, p.ID, u.ID, "failed-release-key"); err != nil {
		t.Fatal(err)
	}
	c, err := s.ClaimPublication(ctx, p.ID)
	if err != nil || c == nil {
		t.Fatal(err)
	}
	if err = s.FinishPublication(ctx, c.Job.ID, c.Lease, "", false); err != nil {
		t.Fatal(err)
	}
	_, active, err := s.PublicationJobs(ctx, session.Token, p.ID)
	if err != nil || active != second.ID || runtime.Active() != secondUpload.SHA256 {
		t.Fatal("failure displaced live release", err)
	}
	rollback := publish(u.ID, "rollback-release-key")
	_, active, err = s.PublicationJobs(ctx, session.Token, p.ID)
	if err != nil || active != rollback.ID || runtime.Active() != u.SHA256 {
		t.Fatal("rollback failed", err)
	}
}
