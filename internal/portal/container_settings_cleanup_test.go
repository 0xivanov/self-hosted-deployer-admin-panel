//go:build integration

package portal

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/registryimage"
)

func TestContainerSettingsCleanupPreservesRetainedReleases(t *testing.T) {
	s, owner, session, p := containerProjectFixture(t)
	credentials, _ := NewContainerCredentials(s, []byte(strings.Repeat("k", 32)))
	environments, _ := NewContainerEnvironments(s, []byte(strings.Repeat("k", 32)))
	cred, err := credentials.Create(t.Context(), session.Token, p.ID, "cleanup-credential-key", "registry", "ghcr.io", registryimage.Credentials{Username: "robot", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	env, err := environments.Create(t.Context(), session.Token, p.ID, "cleanup-environment-key", "settings", map[string]string{"MODE": "production"})
	if err != nil {
		t.Fatal(err)
	}
	cl, err := credentials.List(t.Context(), session.Token, p.ID)
	if err != nil || len(cl) != 1 || !cl[0].Deletable {
		t.Fatalf("unused: %+v %v", cl, err)
	}
	input := ContainerReleaseInput{Reference: "ghcr.io/acme/demo:v1", Port: 8080, HealthPath: "/", CredentialID: cred.ID, EnvironmentID: env.ID}
	release, err := s.PrepareContainerRelease(t.Context(), session.Token, p.ID, "cleanup-release-key", input, func(_ context.Context, _, ref string) (registryimage.Candidate, error) {
		return containerCandidate(ref), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = credentials.Delete(t.Context(), session.Token, p.ID, cred.ID); !errors.Is(err, ErrContainerSettingsInUse) {
		t.Fatalf("retained credential: %v", err)
	}
	if err = environments.Delete(t.Context(), session.Token, p.ID, env.ID); !errors.Is(err, ErrContainerSettingsInUse) {
		t.Fatalf("retained environment: %v", err)
	}
	cl, err = credentials.List(t.Context(), session.Token, p.ID)
	if err != nil || cl[0].Deletable {
		t.Fatalf("retained metadata: %+v %v", cl, err)
	}
	el, err := environments.List(t.Context(), session.Token, p.ID)
	if err != nil || el[0].Deletable {
		t.Fatalf("retained environment metadata: %+v %v", el, err)
	}
	unused, err := credentials.Create(t.Context(), session.Token, p.ID, "unused-credential-key", "unused", "ghcr.io", registryimage.Credentials{Username: "robot", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	viewer, vs := verifiedAccount(t, s, "cleanup-viewer@example.test")
	if _, err = s.db.Exec("INSERT INTO memberships VALUES(?,?,?)", viewer.ID, owner.WorkspaceID, "viewer"); err != nil {
		t.Fatal(err)
	}
	_, outsider := verifiedAccount(t, s, "cleanup-outsider@example.test")
	for _, token := range []string{vs.Token, outsider.Token} {
		if err = credentials.Delete(t.Context(), token, p.ID, unused.ID); !errors.Is(err, ErrDenied) {
			t.Fatalf("unauthorized: %v", err)
		}
	}
	other, err := s.CreateProject(t.Context(), session.Token, owner.WorkspaceID, "Sibling", "container")
	if err != nil {
		t.Fatal(err)
	}
	if err = credentials.Delete(t.Context(), session.Token, other.ID, unused.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = credentials.Resolve(t.Context(), p.ID, unused.ID, input.Reference); err != nil {
		t.Fatalf("cross-project deleted credential: %v", err)
	}
	if _, err = s.db.Exec("UPDATE container_releases SET input='broken' WHERE id=?", release.ID); err != nil {
		t.Fatal(err)
	}
	if err = credentials.Delete(t.Context(), session.Token, p.ID, unused.ID); err == nil {
		t.Fatal("malformed history allowed deletion")
	}
	// Restore valid input without references to the unused credential.
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("UPDATE container_releases SET input=? WHERE id=?", raw, release.ID); err != nil {
		t.Fatal(err)
	}
	if err = credentials.Delete(t.Context(), session.Token, p.ID, unused.ID); err != nil {
		t.Fatal(err)
	}
	if err = credentials.Delete(t.Context(), session.Token, p.ID, unused.ID); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if _, err = credentials.Resolve(t.Context(), p.ID, unused.ID, input.Reference); !errors.Is(err, ErrDenied) {
		t.Fatalf("deleted credential resolved: %v", err)
	}
}

func TestContainerSettingsCleanupDuringImageResolution(t *testing.T) {
	for _, kind := range []string{"credential", "environment"} {
		t.Run(kind, func(t *testing.T) {
			s, _, session, p := containerProjectFixture(t)
			credentials, _ := NewContainerCredentials(s, []byte(strings.Repeat("k", 32)))
			environments, _ := NewContainerEnvironments(s, []byte(strings.Repeat("k", 32)))
			cred, err := credentials.Create(t.Context(), session.Token, p.ID, "race-credential-key", "registry", "ghcr.io", registryimage.Credentials{Username: "robot", Password: "secret"})
			if err != nil {
				t.Fatal(err)
			}
			env, err := environments.Create(t.Context(), session.Token, p.ID, "race-environment-key", "settings", nil)
			if err != nil {
				t.Fatal(err)
			}
			input := ContainerReleaseInput{Reference: "ghcr.io/acme/demo:v1", Port: 8080, HealthPath: "/", CredentialID: cred.ID, EnvironmentID: env.ID}
			_, err = s.PrepareContainerRelease(t.Context(), session.Token, p.ID, "race-release-key", input, func(ctx context.Context, _, ref string) (registryimage.Candidate, error) {
				var e error
				if kind == "credential" {
					e = credentials.Delete(ctx, session.Token, p.ID, cred.ID)
				} else {
					e = environments.Delete(ctx, session.Token, p.ID, env.ID)
				}
				if e != nil {
					t.Fatal(e)
				}
				return containerCandidate(ref), nil
			})
			expected := ErrInvalid
			if kind == "environment" {
				expected = ErrDenied
			}
			if !errors.Is(err, expected) {
				t.Fatalf("expected missing settings denial, got %v", err)
			}
			releases, err := s.ContainerReleases(t.Context(), session.Token, p.ID)
			if err != nil || len(releases) != 0 {
				t.Fatalf("orphan release: %+v %v", releases, err)
			}
		})
	}
}

func TestContainerSettingsCleanupHTTPRequiresCSRF(t *testing.T) {
	s, owner, session, p := containerProjectFixture(t)
	credentials, _ := NewContainerCredentials(s, []byte(strings.Repeat("k", 32)))
	cred, err := credentials.Create(t.Context(), session.Token, p.ID, "http-credential-key", "registry", "ghcr.io", registryimage.Credentials{Username: "robot", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewHTTP(s, HTTPOptions{ContainerHosting: true, ContainerCredentials: credentials, Origin: "https://portal.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf := httpLogin(t, h, owner.Email)
	body := `{"project":"` + p.ID + `","id":"` + cred.ID + `"}`
	if w := portalRequest(h, "POST", "/api/container/credentials/delete", body, h.origin, "", cookie); w.Code != 403 {
		t.Fatalf("csrf: %d", w.Code)
	}
	if w := portalRequest(h, "POST", "/api/container/credentials/delete", body, h.origin, csrf, cookie); w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
}
