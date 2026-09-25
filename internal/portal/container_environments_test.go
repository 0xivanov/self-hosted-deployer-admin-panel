//go:build integration

package portal

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestContainerEnvironmentsEncryptListResolveAndRetry(t *testing.T) {
	s, owner, ownerSession, project := containerProjectFixture(t)
	vault, err := NewContainerEnvironments(s, []byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{"Z_LAST": "last-value", "A_FIRST": "first-value"}
	first, err := vault.Create(t.Context(), ownerSession.Token, project.ID, "environment-request-key", "production", values)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Names) != 2 || first.Names[0] != "A_FIRST" || first.Names[1] != "Z_LAST" {
		t.Fatalf("sorted names: %#v", first.Names)
	}
	var ciphertext []byte
	if err = s.db.QueryRow("SELECT ciphertext FROM container_environments WHERE id=?", first.ID).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(ciphertext), "last-value") || strings.Contains(string(ciphertext), "first-value") {
		t.Fatal("environment plaintext persisted")
	}
	retry, err := vault.Create(t.Context(), ownerSession.Token, project.ID, "environment-request-key", "production", map[string]string{"A_FIRST": "first-value", "Z_LAST": "last-value"})
	if err != nil || retry.ID != first.ID {
		t.Fatalf("idempotent retry: %#v %v", retry, err)
	}
	if _, err = vault.Create(t.Context(), ownerSession.Token, project.ID, "environment-request-key", "changed", values); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed retry: %v", err)
	}
	list, err := vault.List(t.Context(), ownerSession.Token, project.ID)
	if err != nil || len(list) != 1 || len(list[0].Names) != 2 {
		t.Fatalf("environment metadata: %#v %v", list, err)
	}
	resolved, err := vault.Resolve(t.Context(), project.ID, first.ID)
	if err != nil || resolved["A_FIRST"] != "first-value" || resolved["Z_LAST"] != "last-value" {
		t.Fatalf("resolved values: %#v %v", resolved, err)
	}
	if _, err = vault.Resolve(t.Context(), owner.WorkspaceID, first.ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("wrong project resolved values: %v", err)
	}
}

func TestContainerEnvironmentsAuthorizationValidationAndCap(t *testing.T) {
	s, owner, ownerSession, project := containerProjectFixture(t)
	viewer, viewerSession := verifiedAccount(t, s, "container-environment-viewer@example.test")
	if _, err := s.db.Exec("INSERT INTO memberships VALUES(?,?,?)", viewer.ID, owner.WorkspaceID, "viewer"); err != nil {
		t.Fatal(err)
	}
	vault, err := NewContainerEnvironments(s, []byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = vault.List(t.Context(), viewerSession.Token, project.ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("viewer listed environments: %v", err)
	}
	for _, values := range []map[string]string{{"bad-name": "value"}, {"GOOD": strings.Repeat("x", 8193)}, {"GOOD": "bad\x00value"}} {
		if _, err = vault.Create(t.Context(), ownerSession.Token, project.ID, "environment-invalid-request-key", "label", values); !errors.Is(err, ErrContainerEnvironmentInvalid) {
			t.Fatalf("invalid environment accepted: %v", err)
		}
	}
	for i := 0; i < maxContainerEnvironments; i++ {
		key := "environment-cap-key-" + strings.Repeat("x", 16) + string(rune('a'+i))
		if _, err = vault.Create(t.Context(), ownerSession.Token, project.ID, key, "label", map[string]string{"VALUE": string(rune('a' + i))}); err != nil {
			t.Fatalf("environment %d: %v", i, err)
		}
	}
	if _, err = vault.Create(t.Context(), ownerSession.Token, project.ID, "environment-cap-overflow", "overflow", map[string]string{}); !errors.Is(err, ErrContainerEnvironmentLimit) {
		t.Fatalf("environment cap: %v", err)
	}
}

func TestContainerEnvironmentsAllowEmptyAndMigrateToSchema46(t *testing.T) {
	s, path := newStore(t)
	s.ConfigureContainerProjects(true)
	a, session := verifiedAccount(t, s, "container-environment-migration@example.test")
	project, err := s.CreateProject(t.Context(), session.Token, a.WorkspaceID, "migrated", "container")
	if err != nil {
		t.Fatal(err)
	}
	vault, err := NewContainerEnvironments(s, []byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	item, err := vault.Create(t.Context(), session.Token, project.ID, "environment-empty-request-key", "clear", map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := vault.Resolve(t.Context(), project.ID, item.ID)
	if err != nil || len(resolved) != 0 {
		t.Fatalf("empty environment: %#v %v", resolved, err)
	}
	if _, err = s.db.Exec("DROP TABLE github_link_states; DROP TABLE container_environments; PRAGMA user_version=45"); err != nil {
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
		t.Fatalf("project after migration: %#v %v", got, err)
	}
	var version int
	if err = s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 50 {
		t.Fatalf("schema version: %d %v", version, err)
	}
}

func TestContainerEnvironmentVariableCountAndAAD(t *testing.T) {
	s, _, session, project := containerProjectFixture(t)
	vault, err := NewContainerEnvironments(s, []byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	for i := 0; i < 65; i++ {
		values[fmt.Sprintf("VAR_%d", i)] = ""
	}
	if _, err := vault.Create(t.Context(), session.Token, project.ID, "too-many-variables-key", "Too many", values); !errors.Is(err, ErrContainerEnvironmentInvalid) {
		t.Fatal("accepted 65 variables")
	}
	first, err := vault.Create(t.Context(), session.Token, project.ID, "first-environment-key", "First", map[string]string{"TOKEN": "synthetic-secret"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := vault.Create(t.Context(), session.Token, project.ID, "second-environment-key", "Second", map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("UPDATE container_environments SET ciphertext=(SELECT ciphertext FROM container_environments WHERE id=?) WHERE id=?", first.ID, second.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = vault.Resolve(t.Context(), project.ID, second.ID); err == nil {
		t.Fatal("ciphertext moved across versions")
	}
	empty, err := vault.Create(t.Context(), session.Token, project.ID, "empty-environment-key", "Empty", nil)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := vault.Create(t.Context(), session.Token, project.ID, "empty-environment-key", "Empty", map[string]string{})
	if err != nil || retry.ID != empty.ID {
		t.Fatal("nil and empty map retry mismatch")
	}
}
