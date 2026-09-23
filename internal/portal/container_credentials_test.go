//go:build integration

package portal

import (
	"errors"
	"strings"
	"testing"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/registryimage"
)

func TestContainerCredentialsEncryptsAndScopesMetadata(t *testing.T) {
	s, owner, ownerSession, project := containerProjectFixture(t)
	credentials, err := NewContainerCredentials(s, []byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	value := registryimage.Credentials{Username: "robot", Password: "secret:with-colon"}
	saved, err := credentials.Create(ctx, ownerSession.Token, project.ID, "credential-request-key", "primary", "ghcr.io", value)
	if err != nil {
		t.Fatal(err)
	}
	var ciphertext []byte
	if err = s.db.QueryRow("SELECT ciphertext FROM container_credentials WHERE id=?", saved.ID).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(ciphertext), "robot") || strings.Contains(string(ciphertext), "secret") {
		t.Fatal("credential plaintext persisted")
	}
	list, err := credentials.List(ctx, ownerSession.Token, project.ID)
	if err != nil || len(list) != 1 || list[0].ID != saved.ID {
		t.Fatalf("credential metadata: %#v %v", list, err)
	}
	if list[0].Label != "primary" || list[0].Registry != "ghcr.io" {
		t.Fatalf("credential metadata leaked or changed: %#v", list[0])
	}
	resolved, err := credentials.Resolve(ctx, project.ID, saved.ID, "ghcr.io/acme/demo:tag")
	if err != nil || resolved != value {
		t.Fatalf("resolve credential: %#v %v", resolved, err)
	}
	if _, err = credentials.Resolve(ctx, project.ID, saved.ID, "docker.io/library/demo:tag"); !errors.Is(err, ErrDenied) {
		t.Fatalf("wrong registry resolve: %v", err)
	}
	if _, err = credentials.Resolve(ctx, owner.WorkspaceID, saved.ID, "ghcr.io/acme/demo:tag"); !errors.Is(err, ErrDenied) {
		t.Fatalf("wrong project resolve: %v", err)
	}
}

func TestContainerCredentialsImmutableRetryAndRoleIsolation(t *testing.T) {
	s, owner, ownerSession, project := containerProjectFixture(t)
	developer, developerSession := verifiedAccount(t, s, "container-credentials-developer@example.test")
	if _, err := s.db.Exec("INSERT INTO memberships VALUES(?,?,?)", developer.ID, owner.WorkspaceID, "developer"); err != nil {
		t.Fatal(err)
	}
	viewer, viewerSession := verifiedAccount(t, s, "container-credentials-viewer@example.test")
	if _, err := s.db.Exec("INSERT INTO memberships VALUES(?,?,?)", viewer.ID, owner.WorkspaceID, "viewer"); err != nil {
		t.Fatal(err)
	}
	vault, err := NewContainerCredentials(s, []byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	value := registryimage.Credentials{Username: "robot", Password: "secret"}
	first, err := vault.Create(t.Context(), ownerSession.Token, project.ID, "credential-idempotent-key", "primary", "docker.io", value)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := vault.Create(t.Context(), developerSession.Token, project.ID, "credential-idempotent-key", "primary", "docker.io", value)
	if err != nil || retry != first {
		t.Fatalf("idempotent retry: %#v %v", retry, err)
	}
	if _, err = vault.Create(t.Context(), ownerSession.Token, project.ID, "credential-idempotent-key", "changed", "docker.io", value); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed retry: %v", err)
	}
	if _, err = vault.List(t.Context(), viewerSession.Token, project.ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("viewer listed credentials: %v", err)
	}
	_, otherSession := verifiedAccount(t, s, "container-credentials-other@example.test")
	if _, err = vault.List(t.Context(), otherSession.Token, project.ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("other tenant listed credentials: %v", err)
	}
}

func TestContainerCredentialsCapAndMigration(t *testing.T) {
	s, _, ownerSession, project := containerProjectFixture(t)
	vault, err := NewContainerCredentials(s, []byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxContainerCredentials; i++ {
		key := "credential-cap-key-" + strings.Repeat("x", 16) + string(rune('a'+i))
		if _, err = vault.Create(t.Context(), ownerSession.Token, project.ID, key, "label-"+string(rune('a'+i)), "ghcr.io", registryimage.Credentials{Username: "robot", Password: "secret"}); err != nil {
			t.Fatalf("credential %d: %v", i, err)
		}
	}
	if _, err = vault.Create(t.Context(), ownerSession.Token, project.ID, "credential-cap-overflow", "overflow", "ghcr.io", registryimage.Credentials{Username: "robot", Password: "secret"}); !errors.Is(err, ErrContainerCredentialLimit) {
		t.Fatalf("credential cap: %v", err)
	}
}

func TestContainerCredentialsMigrationPreservesProjectData(t *testing.T) {
	s, path := newStore(t)
	s.ConfigureContainerProjects(true)
	a, session := verifiedAccount(t, s, "container-credentials-migration@example.test")
	project, err := s.CreateProject(t.Context(), session.Token, a.WorkspaceID, "migrated", "container")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("DROP TABLE container_credentials; PRAGMA user_version=44"); err != nil {
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
	got, err := s.GetProject(t.Context(), session.Token, project.ID)
	if err != nil || got.ID != project.ID {
		t.Fatalf("project after credential migration: %#v %v", got, err)
	}
	var version int
	if err = s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 45 {
		t.Fatalf("schema version: %d %v", version, err)
	}
}

func TestContainerCredentialsCiphertextCannotMoveBetweenRevisions(t *testing.T) {
	s, _, session, project := containerProjectFixture(t)
	vault, err := NewContainerCredentials(s, []byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	first, err := vault.Create(t.Context(), session.Token, project.ID, "credential-first-key", "first", "ghcr.io", registryimage.Credentials{Username: "robot", Password: "first-token"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := vault.Create(t.Context(), session.Token, project.ID, "credential-second-key", "second", "ghcr.io", registryimage.Credentials{Username: "robot", Password: "second-token"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE container_credentials SET ciphertext=(SELECT ciphertext FROM container_credentials WHERE id=?) WHERE id=?`, first.ID, second.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := vault.Resolve(t.Context(), project.ID, second.ID, "ghcr.io/acme/demo:tag"); !errors.Is(err, ErrDenied) {
		t.Fatalf("rebound ciphertext accepted: %v", err)
	}
	wrongKey, err := NewContainerCredentials(s, []byte(strings.Repeat("x", 32)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrongKey.Resolve(t.Context(), project.ID, first.ID, "ghcr.io/acme/demo:tag"); !errors.Is(err, ErrDenied) {
		t.Fatal("wrong key accepted")
	}
}
