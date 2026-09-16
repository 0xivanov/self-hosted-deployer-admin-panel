package portal

import (
	"context"
	"errors"
	"testing"
)

func TestDeleteProjectAuthorizationAndConfirmation(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)
	ctx := context.Background()
	owner, os := verifiedAccount(t, s, "delete-owner@example.test")
	_, otherSession := verifiedAccount(t, s, "delete-other@example.test")
	developer, developerSession := verifiedAccount(t, s, "delete-developer@example.test")
	viewer, viewerSession := verifiedAccount(t, s, "delete-viewer@example.test")
	var err error
	for _, membership := range []struct {
		user string
		role string
	}{{developer.ID, "developer"}, {viewer.ID, "viewer"}} {
		if _, err = s.db.Exec("INSERT INTO memberships(user_id,workspace_id,role) VALUES(?,?,?)", membership.user, owner.WorkspaceID, membership.role); err != nil {
			t.Fatal(err)
		}
	}
	p, err := s.CreateProject(ctx, os.Token, owner.WorkspaceID, "Delete Me", "static")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DeleteProject(ctx, otherSession.Token, p.ID, p.Name); !errors.Is(err, ErrDenied) {
		t.Fatalf("cross-tenant delete: %v", err)
	}
	if _, err = s.DeleteProject(ctx, developerSession.Token, p.ID, p.Name); !errors.Is(err, ErrDenied) {
		t.Fatalf("developer delete: %v", err)
	}
	if _, err = s.DeleteProject(ctx, viewerSession.Token, p.ID, p.Name); !errors.Is(err, ErrDenied) {
		t.Fatalf("viewer delete: %v", err)
	}
	if _, err = s.CreateCustomDomain(ctx, os.Token, p.ID, "delete.example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DeleteProject(ctx, os.Token, p.ID, "delete me"); !errors.Is(err, ErrProjectNameMismatch) {
		t.Fatalf("case-mismatched confirmation: %v", err)
	}
	deleted, err := s.DeleteProject(ctx, os.Token, p.ID, p.Name)
	if err != nil || !deleted.Deleting || deleted.DeletionError != "" {
		t.Fatalf("delete request: %+v %v", deleted, err)
	}
	again, err := s.DeleteProject(ctx, os.Token, p.ID, p.Name)
	if err != nil || !again.Deleting {
		t.Fatalf("idempotent delete: %+v %v", again, err)
	}
	var domains int
	var state, message string
	if err = s.db.QueryRow("SELECT count(*),COALESCE(max(state),''),COALESCE(max(message),'') FROM project_domains WHERE project_id=?", p.ID).Scan(&domains, &state, &message); err != nil || domains != 1 || state != "removing" || message != "Project deletion pending" {
		t.Fatalf("domains not marked for removal: count=%d state=%q message=%q err=%v", domains, state, message, err)
	}
}

func TestDeleteProjectBusyAndMutationBlocked(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)
	ctx := context.Background()
	a, as := verifiedAccount(t, s, "delete-busy@example.test")
	p, err := s.CreateProject(ctx, as.Token, a.WorkspaceID, "Busy", "static")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("INSERT INTO uploads(id,project_id,sha256,files,expanded_bytes,archive,created_at) VALUES(?,?,?,?,?,?,?)", "upload", p.ID, "hash", 1, 1, []byte("zip"), 1); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("INSERT INTO publication_jobs(id,project_id,upload_id,actor_id,request_key,revision,state,created_at) VALUES(?,?,?,?,?,?,?,?)", "job", p.ID, "upload", a.ID, "request", 1, "queued", 1); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DeleteProject(ctx, as.Token, p.ID, p.Name); !errors.Is(err, ErrProjectBusy) {
		t.Fatalf("busy deletion: %v", err)
	}
	if _, err = s.db.Exec("UPDATE publication_jobs SET state='failed' WHERE id='job'"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DeleteProject(ctx, as.Token, p.ID, p.Name); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateCustomDomain(ctx, as.Token, p.ID, "blocked.example.com"); !errors.Is(err, ErrProjectDeleting) {
		t.Fatalf("domain mutation after deletion: %v", err)
	}
}
