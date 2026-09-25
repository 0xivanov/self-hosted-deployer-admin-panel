//go:build integration

package portal

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/registryimage"
)

func containerCandidate(source string) registryimage.Candidate {
	manifest := "sha256:" + strings.Repeat("a", 64)
	sourceDigest := "sha256:" + strings.Repeat("b", 64)
	config := "sha256:" + strings.Repeat("c", 64)
	return registryimage.Candidate{
		Source:         source,
		SourceDigest:   sourceDigest,
		Image:          "ghcr.io/acme/demo@" + manifest,
		ManifestDigest: manifest,
		ConfigDigest:   config,
		OS:             "linux",
		Architecture:   "arm64",
	}
}

func containerProjectFixture(t *testing.T) (*Store, Account, Session, Project) {
	t.Helper()
	s, _ := newStore(t)
	s.ConfigureContainerProjects(true)
	a, session := verifiedAccount(t, s, "container-owner@example.test")
	p, err := s.CreateProject(t.Context(), session.Token, a.WorkspaceID, "container", "container")
	if err != nil {
		t.Fatal(err)
	}
	return s, a, session, p
}

func TestContainerProjectsAndReleasesDisabledByDefault(t *testing.T) {
	s, _ := newStore(t)
	a, session := verifiedAccount(t, s, "container-disabled@example.test")
	if _, err := s.CreateProject(t.Context(), session.Token, a.WorkspaceID, "container", "container"); !errors.Is(err, ErrContainerUnavailable) {
		t.Fatalf("container project with default configuration: %v", err)
	}
	if _, err := s.PrepareContainerRelease(t.Context(), session.Token, "missing", "container-disabled-request-key", ContainerReleaseInput{}, nil); !errors.Is(err, ErrContainerUnavailable) {
		t.Fatalf("container release with default configuration: %v", err)
	}
}

func TestPrepareContainerReleaseIsIdempotentAndKeepsImagePin(t *testing.T) {
	s, _, session, p := containerProjectFixture(t)
	in := ContainerReleaseInput{Reference: "ghcr.io/acme/demo:tag", Port: 8080, HealthPath: "/healthz"}
	firstCandidate := containerCandidate(in.Reference)
	first, err := s.PrepareContainerRelease(t.Context(), session.Token, p.ID, "container-release-request-key", in, func(_ context.Context, _, _ string) (registryimage.Candidate, error) {
		return firstCandidate, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.PrepareContainerRelease(t.Context(), session.Token, p.ID, "container-release-request-key", in, func(_ context.Context, _, _ string) (registryimage.Candidate, error) {
		return containerCandidate(in.Reference), errors.New("resolver should not be called on retry")
	})
	if err != nil || !reflect.DeepEqual(second, first) {
		t.Fatalf("idempotent retry: %#v %v", second, err)
	}
	if second.Image.Image != firstCandidate.Image || second.Input.Reference != in.Reference {
		t.Fatalf("saved image pin: %#v", second)
	}
	listed, err := s.ContainerReleases(t.Context(), session.Token, p.ID)
	if err != nil || len(listed) != 1 || !reflect.DeepEqual(listed[0], first) {
		t.Fatalf("release history: %#v %v", listed, err)
	}
	in.Port = 9090
	if _, err := s.PrepareContainerRelease(t.Context(), session.Token, p.ID, "container-release-request-key", in, func(context.Context, string, string) (registryimage.Candidate, error) {
		t.Fatal("resolver called for conflicting retry")
		return firstCandidate, nil
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed settings reused request key: %v", err)
	}
}

func TestPrepareContainerReleaseRejectsCrossTenantAndInvalidCandidate(t *testing.T) {
	s, _, ownerSession, p := containerProjectFixture(t)
	_, otherSession := verifiedAccount(t, s, "container-other@example.test")
	in := ContainerReleaseInput{Reference: "ghcr.io/acme/demo:tag", Port: 8080, HealthPath: "/healthz"}
	if _, err := s.ContainerReleases(t.Context(), otherSession.Token, p.ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("foreign history access: %v", err)
	}
	valid := containerCandidate(in.Reference)
	if _, err := s.PrepareContainerRelease(t.Context(), otherSession.Token, p.ID, "container-foreign-request-key", in, func(_ context.Context, _, _ string) (registryimage.Candidate, error) {
		return valid, nil
	}); !errors.Is(err, ErrDenied) {
		t.Fatalf("cross-tenant release: %v", err)
	}
	invalid := valid
	invalid.Architecture = "amd64"
	if _, err := s.PrepareContainerRelease(t.Context(), ownerSession.Token, p.ID, "container-invalid-request-key", in, func(_ context.Context, _, _ string) (registryimage.Candidate, error) {
		return invalid, nil
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid candidate: %v", err)
	}
	var count int
	if err := s.db.QueryRow("SELECT count(*) FROM container_releases").Scan(&count); err != nil || count != 0 {
		t.Fatalf("invalid or foreign release persisted: %d %v", count, err)
	}
	for i, badInput := range []ContainerReleaseInput{
		{Reference: "ghcr.io/acme/demo:tag", Port: 80, HealthPath: "/healthz"},
		{Reference: "ghcr.io/acme/demo:tag", Port: 8080, HealthPath: "//healthz"},
		{Reference: "ghcr.io/acme/demo:tag", Port: 8080, HealthPath: "/healthz?token=secret"},
	} {
		if _, err := s.PrepareContainerRelease(t.Context(), ownerSession.Token, p.ID, "container-invalid-input-key-"+strings.Repeat("x", i+16), badInput, func(_ context.Context, _, _ string) (registryimage.Candidate, error) {
			t.Fatal("resolver called for invalid input")
			return registryimage.Candidate{}, nil
		}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid input %d: %v", i, err)
		}
	}
}

func TestContainerProjectsShareNodeCapacity(t *testing.T) {
	s, _ := newStore(t)
	s.ConfigureContainerProjects(true)
	if err := s.ConfigureProjectCapacity(2, 1); err != nil {
		t.Fatal(err)
	}
	a, session := verifiedAccount(t, s, "container-capacity@example.test")
	if _, err := s.CreateProject(t.Context(), session.Token, a.WorkspaceID, "node", "node"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateProject(t.Context(), session.Token, a.WorkspaceID, "container", "container"); !errors.Is(err, ErrHostingCapacity) {
		t.Fatalf("container bypassed node capacity: %v", err)
	}
}

func TestPrepareContainerReleaseRechecksAuthorizationAfterResolver(t *testing.T) {
	s, owner, session, p := containerProjectFixture(t)
	in := ContainerReleaseInput{Reference: "ghcr.io/acme/demo:tag", Port: 8080, HealthPath: "/healthz"}
	_, err := s.PrepareContainerRelease(t.Context(), session.Token, p.ID, "container-revoked-request-key", in, func(_ context.Context, _, _ string) (registryimage.Candidate, error) {
		if _, updateErr := s.db.Exec("UPDATE users SET disabled=1 WHERE id=?", owner.ID); updateErr != nil {
			t.Fatal(updateErr)
		}
		return containerCandidate(in.Reference), nil
	})
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("revoked resolver callback accepted: %v", err)
	}
	var count int
	if err = s.db.QueryRow("SELECT count(*) FROM container_releases").Scan(&count); err != nil || count != 0 {
		t.Fatalf("revoked release persisted: %d %v", count, err)
	}
}

func TestContainerMigrationPreservesStaticUploadAndForeignKeys(t *testing.T) {
	s, path := newStore(t)
	a, session := verifiedAccount(t, s, "container-migration@example.test")
	p, err := s.CreateProject(t.Context(), session.Token, a.WorkspaceID, "legacy", "static")
	if err != nil {
		t.Fatal(err)
	}
	archive := testArchive(t)
	upload, err := s.SaveUpload(t.Context(), session.Token, p.ID, archive)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("DROP TABLE container_releases; PRAGMA user_version=42"); err != nil {
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
	got, err := s.Uploads(t.Context(), session.Token, p.ID)
	if err != nil || len(got) != 1 || got[0] != upload {
		t.Fatalf("upload after container migration: %#v %v", got, err)
	}
	var foreignKeys, version int
	if err = s.db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		t.Fatalf("foreign keys after migration: %d %v", foreignKeys, err)
	}
	if err = s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 56 {
		t.Fatalf("schema version after migration: %d %v", version, err)
	}
	if _, err = s.db.Exec("DELETE FROM projects WHERE id=?", p.ID); err == nil {
		t.Fatal("deleted project with existing upload")
	}
	if err = s.db.QueryRow("SELECT count(*) FROM uploads WHERE id=?", upload.ID).Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		t.Fatalf("upload foreign-key preservation: %d %v", foreignKeys, err)
	}
}

func TestContainerReleaseHistoryRejectsViewer(t *testing.T) {
	s, owner, session, p := containerProjectFixture(t)
	if _, err := s.db.Exec("UPDATE memberships SET role='viewer' WHERE user_id=? AND workspace_id=?", owner.ID, p.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ContainerReleases(t.Context(), session.Token, p.ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("viewer history access: %v", err)
	}
	in := ContainerReleaseInput{Reference: "ghcr.io/acme/demo:tag", Port: 8080, HealthPath: "/"}
	if _, err := s.PrepareContainerRelease(t.Context(), session.Token, p.ID, "viewer-release-request", in, func(context.Context, string, string) (registryimage.Candidate, error) {
		t.Fatal("viewer contacted registry")
		return registryimage.Candidate{}, nil
	}); !errors.Is(err, ErrDenied) {
		t.Fatalf("viewer preparation: %v", err)
	}
}
