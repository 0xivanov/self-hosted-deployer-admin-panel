//go:build integration

package portal

import (
	"errors"
	"testing"
	"time"
)

func TestOwnerResumesRevokedPublication(t *testing.T) {
	t.Parallel()
	s, _, owner, session, p, u := publicationFixture(t)
	ctx := t.Context()
	now := time.Now()
	s.now = func() time.Time { return now }
	developer, devSession := verifiedAccount(t, s, "recovery-dev@example.test")
	if _, err := s.db.Exec("INSERT INTO memberships VALUES(?,?,'developer')", developer.ID, owner.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	job, err := s.RequestPublication(ctx, devSession.Token, p.ID, u.ID, "revoked-publication-key")
	if err != nil {
		t.Fatal(err)
	}
	original, err := s.ClaimPublication(ctx, p.ID)
	if err != nil || original == nil {
		t.Fatal(err)
	}
	if _, err = s.ResumePublication(ctx, session.Token, p.ID, job.ID, u.ID); !errors.Is(err, ErrPublishing) {
		t.Fatal("active worker interrupted", err)
	}
	if _, err = s.ResumePublication(ctx, devSession.Token, p.ID, job.ID, u.ID); !errors.Is(err, ErrDenied) {
		t.Fatal("developer could adopt job", err)
	}
	if _, err = s.db.Exec("DELETE FROM memberships WHERE user_id=? AND workspace_id=?", developer.ID, owner.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	now = now.Add(61 * time.Second)
	if _, err = s.ClaimPublication(ctx, p.ID); !errors.Is(err, ErrDenied) {
		t.Fatal("revoked job continued", err)
	}
	if _, err = s.ResumePublication(ctx, session.Token, p.ID, job.ID, "different-upload"); !errors.Is(err, ErrConflict) {
		t.Fatal("changed payload accepted", err)
	}
	adopted, err := s.ResumePublication(ctx, session.Token, p.ID, job.ID, u.ID)
	if err != nil || adopted.State != "queued" || adopted.Revision != job.Revision || adopted.UploadID != u.ID {
		t.Fatal(adopted, err)
	}
	if _, err = s.ResumePublication(ctx, session.Token, p.ID, job.ID, u.ID); err != nil {
		t.Fatal("idempotent resume failed", err)
	}
	claim, err := s.ClaimPublication(ctx, p.ID)
	if err != nil || claim == nil || claim.Job.ID != job.ID {
		t.Fatal(claim, err)
	}
	if err = s.FinishPublication(ctx, job.ID, original.Lease, original.SHA256, true); !errors.Is(err, ErrConflict) {
		t.Fatal("original lease survived recovery", err)
	}
	if err = s.FinishPublication(ctx, job.ID, claim.Lease, claim.SHA256, true); err != nil {
		t.Fatal(err)
	}
	jobs, active, err := s.PublicationJobs(ctx, session.Token, p.ID)
	if err != nil || active != job.ID || jobs[0].State != "succeeded" {
		t.Fatal(jobs, active, err)
	}
	var count int
	if err = s.db.QueryRow("SELECT count(*) FROM audit_events WHERE actor_id=? AND action=?", owner.ID, "publication.resumed:"+job.ID+":previous_actor:"+developer.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("missing recovery audit", count, err)
	}
	if _, err = s.ResumePublication(ctx, session.Token, p.ID, job.ID, u.ID); !errors.Is(err, ErrPublishing) {
		t.Fatal("completed publication resumed", err)
	}
}
func TestResumeHTTPRequiresAssignmentOwnerAndCSRF(t *testing.T) {
	t.Parallel()
	s, _, a, session, p, u := publicationFixture(t)
	ctx := t.Context()
	job, err := s.RequestPublication(ctx, session.Token, p.ID, u.ID, "resume-http-request")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ClaimPublication(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("UPDATE publication_jobs SET lease_until=0 WHERE id=?", job.ID); err != nil {
		t.Fatal(err)
	}
	h, err := NewHTTP(s, HTTPOptions{Origin: "https://portal.example.test", PublicationSites: map[string]string{p.ID: "https://site.example.net"}})
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf := httpLogin(t, h, a.Email)
	body := `{"project":"` + p.ID + `","job":"` + job.ID + `","upload":"` + u.ID + `"}`
	if w := portalRequest(h, "POST", "/api/publications/resume", body, h.origin, "", cookie); w.Code != 403 {
		t.Fatal("CSRF bypass", w.Code)
	}
	other, _ := verifiedAccount(t, s, "resume-foreign@example.test")
	oc, ot := httpLogin(t, h, other.Email)
	if w := portalRequest(h, "POST", "/api/publications/resume", body, h.origin, ot, oc); w.Code != 403 {
		t.Fatal("cross-tenant resume", w.Code)
	}
	disabled, err := NewHTTP(s, HTTPOptions{Origin: h.origin})
	if err != nil {
		t.Fatal(err)
	}
	if w := portalRequest(disabled, "POST", "/api/publications/resume", body, h.origin, csrf, cookie); w.Code != 403 {
		t.Fatal("disabled project resumed", w.Code)
	}
	if w := portalRequest(h, "POST", "/api/publications/resume", body, h.origin, csrf, cookie); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}
