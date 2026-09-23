package portal

import (
	"context"
	"errors"
	"testing"
)

func projectClientFixture(t *testing.T) (*Store, Account, Session, Account, Session, Project) {
	t.Helper()
	s, _ := newStore(t)
	owner, ownerSession := verifiedAccount(t, s, "clients-owner@example.test")
	client, clientSession := verifiedAccount(t, s, "clients-client@example.test")
	p, err := s.CreateProject(context.Background(), ownerSession.Token, owner.WorkspaceID, "Shared", "static")
	if err != nil {
		t.Fatal(err)
	}
	return s, owner, ownerSession, client, clientSession, p
}

func TestProjectClientsOwnerOnlyAndNoWorkspaceOrSiblingLeakage(t *testing.T) {
	s, owner, ownerSession, client, clientSession, p := projectClientFixture(t)
	ctx := context.Background()
	developer, developerSession := verifiedAccount(t, s, "clients-developer@example.test")
	if _, err := s.db.Exec("INSERT INTO memberships VALUES(?,?,?)", developer.ID, owner.WorkspaceID, "developer"); err != nil {
		t.Fatal(err)
	}
	sibling, err := s.CreateProject(ctx, ownerSession.Token, owner.WorkspaceID, "Private", "static")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ChangeProjectClient(ctx, developerSession.Token, p.ID, client.Email, true); !errors.Is(err, ErrDenied) {
		t.Fatalf("developer grant: %v", err)
	}
	if err = s.ChangeProjectClient(ctx, ownerSession.Token, p.ID, client.Email, true); err != nil {
		t.Fatalf("owner grant: %v", err)
	}
	clients, err := s.ProjectClients(ctx, ownerSession.Token, p.ID)
	if err != nil || len(clients) != 1 || clients[0].Email != client.Email {
		t.Fatalf("owner client list: %#v %v", clients, err)
	}
	if _, err = s.ProjectClients(ctx, clientSession.Token, p.ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("client listing owner data: %v", err)
	}
	if _, err = s.GetProject(ctx, clientSession.Token, p.ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("client project API access: %v", err)
	}
	var memberships int
	if err = s.db.QueryRow("SELECT count(*) FROM memberships WHERE user_id=? AND workspace_id=?", client.ID, owner.WorkspaceID).Scan(&memberships); err != nil || memberships != 0 {
		t.Fatalf("client gained workspace membership: %d %v", memberships, err)
	}
	websites, err := s.SharedWebsites(ctx, clientSession.Token)
	if err != nil || len(websites) != 1 || websites[0].ID != p.ID {
		t.Fatalf("initial shared website list: %#v %v", websites, err)
	}
	if err = s.ChangeProjectClient(ctx, ownerSession.Token, sibling.ID, client.Email, true); err != nil {
		t.Fatalf("sibling grant setup: %v", err)
	}
	websites, err = s.SharedWebsites(ctx, clientSession.Token)
	if err != nil || len(websites) != 2 {
		t.Fatalf("shared website list: %#v %v", websites, err)
	}
	if websites[0].ID != sibling.ID || websites[1].ID != p.ID {
		t.Fatalf("shared website identities: %#v", websites)
	}
}

func TestSharedWebsitesSummaryRevocationDisabledAndDeleting(t *testing.T) {
	s, owner, ownerSession, client, clientSession, p := projectClientFixture(t)
	ctx := context.Background()
	if err := s.ChangeProjectClient(ctx, ownerSession.Token, p.ID, client.Email, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("INSERT INTO project_domains(id,project_id,hostname,token,state,created_at) VALUES(?,?,?,?,?,?)", "domain", p.ID, "shared.example.test", "token", "active", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("INSERT INTO uploads(id,project_id,sha256,files,expanded_bytes,archive,created_at) VALUES(?,?,?,?,?,?,?)", "upload", p.ID, "hash", 1, 1, []byte("zip"), 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("INSERT INTO publication_jobs(id,project_id,upload_id,actor_id,request_key,revision,state,created_at) VALUES(?,?,?,?,?,?,?,?)", "job", p.ID, "upload", owner.ID, "request", 1, "succeeded", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("INSERT INTO publications(project_id,job_id) VALUES(?,?)", p.ID, "job"); err != nil {
		t.Fatal(err)
	}
	websites, err := s.SharedWebsites(ctx, clientSession.Token)
	if err != nil || len(websites) != 1 || websites[0].Name != p.Name || websites[0].Kind != "static" || !websites[0].Published || websites[0].Domain != "shared.example.test" {
		t.Fatalf("summary: %#v %v", websites, err)
	}
	if err = s.ChangeProjectClient(ctx, ownerSession.Token, p.ID, client.Email, false); err != nil {
		t.Fatal(err)
	}
	websites, err = s.SharedWebsites(ctx, clientSession.Token)
	if err != nil || len(websites) != 0 {
		t.Fatalf("revoked summary: %#v %v", websites, err)
	}
	if err = s.ChangeProjectClient(ctx, ownerSession.Token, p.ID, client.Email, true); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("UPDATE users SET disabled=1 WHERE id=?", client.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SharedWebsites(ctx, clientSession.Token); !errors.Is(err, ErrDenied) {
		t.Fatalf("disabled shared summary: %v", err)
	}
	if err = s.ChangeProjectClient(ctx, ownerSession.Token, p.ID, client.Email, false); err != nil {
		t.Fatalf("revoke disabled client: %v", err)
	}
	var grants int
	if err = s.db.QueryRow("SELECT count(*) FROM project_clients WHERE project_id=?", p.ID).Scan(&grants); err != nil || grants != 0 {
		t.Fatalf("disabled revoke persisted: %d %v", grants, err)
	}
	if _, err = s.db.Exec("UPDATE users SET disabled=0 WHERE id=?", client.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.ChangeProjectClient(ctx, ownerSession.Token, p.ID, client.Email, true); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("UPDATE projects SET deletion_requested_at=1 WHERE id=?", p.ID); err != nil {
		t.Fatal(err)
	}
	websites, err = s.SharedWebsites(ctx, clientSession.Token)
	if err != nil || len(websites) != 0 {
		t.Fatalf("deleting summary: %#v %v", websites, err)
	}
}

func TestProjectClientLimit(t *testing.T) {
	s, _, ownerSession, _, _, p := projectClientFixture(t)
	ctx := context.Background()
	var firstEmail string
	for i := 0; i < 21; i++ {
		client, _ := verifiedAccount(t, s, "limit-client-"+string(rune('a'+i))+"@example.test")
		if i == 0 {
			firstEmail = client.Email
		}
		err := s.ChangeProjectClient(ctx, ownerSession.Token, p.ID, client.Email, true)
		if i < 20 && err != nil {
			t.Fatalf("grant %d: %v", i, err)
		}
		if i == 20 && !errors.Is(err, ErrClientLimit) {
			t.Fatalf("21st grant: %v", err)
		}
	}
	if err := s.ChangeProjectClient(ctx, ownerSession.Token, p.ID, firstEmail, true); err != nil {
		t.Fatalf("idempotent regrant at limit: %v", err)
	}
	var count int
	if err := s.db.QueryRow("SELECT count(*) FROM project_clients WHERE project_id=?", p.ID).Scan(&count); err != nil || count != 20 {
		t.Fatalf("idempotent regrant count: %d %v", count, err)
	}
}

func TestProjectClientsMigrationAndForeignKeyCascade(t *testing.T) {
	s, path := newStore(t)
	owner, ownerSession := verifiedAccount(t, s, "clients-migration-owner@example.test")
	client, _ := verifiedAccount(t, s, "clients-migration-client@example.test")
	p, err := s.CreateProject(t.Context(), ownerSession.Token, owner.WorkspaceID, "Migrated", "static")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("DROP TABLE project_clients; PRAGMA user_version=40"); err != nil {
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
	if _, err = s.Authenticate(t.Context(), ownerSession.Token); err != nil {
		t.Fatalf("session after migration: %v", err)
	}
	if err = s.ChangeProjectClient(t.Context(), ownerSession.Token, p.ID, client.Email, true); err != nil {
		t.Fatalf("grant after migration: %v", err)
	}
	if _, err = s.db.Exec("DELETE FROM sessions WHERE user_id=?; DELETE FROM memberships WHERE user_id=?; DELETE FROM users WHERE id=?", client.ID, client.ID, client.ID); err != nil {
		t.Fatal(err)
	}
	var grants int
	if err = s.db.QueryRow("SELECT count(*) FROM project_clients WHERE project_id=?", p.ID).Scan(&grants); err != nil || grants != 0 {
		t.Fatalf("user cascade: %d %v", grants, err)
	}
	client2, _ := verifiedAccount(t, s, "clients-migration-client2@example.test")
	if err = s.ChangeProjectClient(t.Context(), ownerSession.Token, p.ID, client2.Email, true); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("DELETE FROM projects WHERE id=?", p.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRow("SELECT count(*) FROM project_clients WHERE project_id=?", p.ID).Scan(&grants); err != nil || grants != 0 {
		t.Fatalf("project cascade: %d %v", grants, err)
	}
}
