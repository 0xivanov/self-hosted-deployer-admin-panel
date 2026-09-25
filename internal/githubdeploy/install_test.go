package githubdeploy

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"net/http"
	"testing"
)

func TestInstallationURLUsesVerifiedAppSlug(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{clientID: "client-1", key: key}
	previous := githubAPIClient
	githubAPIClient = &http.Client{Transport: accessTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/app" || r.Header.Get("Authorization") == "" {
			t.Fatalf("unexpected app request: %s", r.URL.String())
		}
		return jsonResponse(http.StatusOK, map[string]any{"id": 42, "client_id": "client-1", "slug": "launchstead", "html_url": "https://evil.example/install"}), nil
	})}
	t.Cleanup(func() { githubAPIClient = previous })
	got, err := app.InstallationURL(context.Background())
	if err != nil || got != "https://github.com/apps/launchstead/installations/new" {
		t.Fatalf("url=%q err=%v", got, err)
	}
}

func TestInstallationURLAcceptsNumericAppIDConfiguration(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{clientID: "42", key: key}
	previous := githubAPIClient
	githubAPIClient = &http.Client{Transport: accessTransport(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, map[string]any{"id": 42, "client_id": "client-1", "slug": "launchstead"}), nil
	})}
	t.Cleanup(func() { githubAPIClient = previous })
	if got, err := app.InstallationURL(context.Background()); err != nil || got == "" {
		t.Fatalf("numeric configuration failed: url=%q err=%v", got, err)
	}
}

func TestInstallationURLRejectsWrongIdentityAndMalformedSlug(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	for name, metadata := range map[string]map[string]any{
		"wrong client":     {"id": 42, "client_id": "different", "slug": "launchstead"},
		"wrong numeric id": {"id": 43, "client_id": "client-1", "slug": "launchstead"},
		"uppercase":        {"id": 42, "client_id": "client-1", "slug": "Launchstead"},
		"path traversal":   {"id": 42, "client_id": "client-1", "slug": "../evil"},
		"trailing hyphen":  {"id": 42, "client_id": "client-1", "slug": "launchstead-"},
	} {
		t.Run(name, func(t *testing.T) {
			clientID := "client-1"
			if name == "wrong numeric id" {
				clientID = "42"
			}
			app := &App{clientID: clientID, key: key}
			previous := githubAPIClient
			githubAPIClient = &http.Client{Transport: accessTransport(func(r *http.Request) (*http.Response, error) {
				return jsonResponse(http.StatusOK, metadata), nil
			})}
			t.Cleanup(func() { githubAPIClient = previous })
			if _, err := app.InstallationURL(context.Background()); !errors.Is(err, ErrGitHubAccess) {
				t.Fatalf("error=%v, want ErrGitHubAccess", err)
			}
		})
	}
}
