package githubdeploy

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type accessTransport func(*http.Request) (*http.Response, error)

func (f accessTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func jsonResponse(status int, value any) *http.Response {
	body, _ := json.Marshal(value)
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body)))}
}

func TestVerifyRepositoryAccessPaginatesAndReturnsProof(t *testing.T) {
	token := "github-user-token"
	var requests []string
	client := &http.Client{Transport: accessTransport(func(r *http.Request) (*http.Response, error) {
		requests = append(requests, r.URL.RequestURI())
		if r.Header.Get("Authorization") != "Bearer "+token || r.URL.Scheme != "https" || r.URL.Host != "api.github.com" {
			t.Fatal("request authentication or destination was not fixed")
		}
		switch r.URL.RequestURI() {
		case "/user":
			return jsonResponse(http.StatusOK, map[string]any{"id": 99, "login": "alice", "extra": true}), nil
		case "/user/installations?per_page=100&page=1":
			items := make([]any, 100)
			for i := range items {
				items[i] = map[string]any{"id": i + 100, "app_id": 999, "permissions": map[string]string{"contents": "write"}}
			}
			return jsonResponse(http.StatusOK, map[string]any{"installations": items}), nil
		case "/user/installations?per_page=100&page=2":
			return jsonResponse(http.StatusOK, map[string]any{"installations": []any{map[string]any{"id": 7, "app_id": 123, "permissions": map[string]string{"contents": "write"}, "suspended_at": nil}}}), nil
		case "/user/installations/7/repositories?per_page=100&page=1":
			items := make([]any, 100)
			for i := range items {
				items[i] = map[string]any{"id": i + 1000, "full_name": "octocat/other", "owner": map[string]string{"login": "octocat"}, "default_branch": "main", "permissions": map[string]bool{"pull": true}}
			}
			return jsonResponse(http.StatusOK, map[string]any{"repositories": items}), nil
		case "/user/installations/7/repositories?per_page=100&page=2":
			return jsonResponse(http.StatusOK, map[string]any{"repositories": []any{map[string]any{"id": 42, "full_name": "octocat/Hello-World", "owner": map[string]string{"login": "octocat"}, "default_branch": "main", "permissions": map[string]bool{"pull": true}}}}), nil
		default:
			t.Fatalf("unexpected request path %s", r.URL.RequestURI())
			return nil, errors.New("unexpected request")
		}
	})}
	proof, err := verifyRepositoryAccess(context.Background(), client, 123, token, 7, 42)
	if err != nil {
		t.Fatalf("verifyRepositoryAccess: %v", err)
	}
	if proof.UserID != 99 || proof.Login != "alice" || proof.InstallationID != 7 || proof.RepositoryID != 42 || proof.RepositoryFullName != "octocat/Hello-World" || proof.DefaultBranch != "main" {
		t.Fatalf("unexpected proof: %+v", proof)
	}
	if len(requests) != 5 {
		t.Fatalf("request count = %d, want 5", len(requests))
	}
}

func TestVerifyRepositoryAccessResolvesProviderNumericAppID(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{clientID: "client-1", key: key}
	client := &http.Client{Transport: accessTransport(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/app":
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ey") {
				t.Fatal("app lookup did not use an app JWT")
			}
			return jsonResponse(http.StatusOK, map[string]any{"id": 123, "client_id": "client-1"}), nil
		case "/user":
			return jsonResponse(http.StatusOK, map[string]any{"id": 99, "login": "alice"}), nil
		case "/user/installations":
			return jsonResponse(http.StatusOK, map[string]any{"installations": []any{map[string]any{"id": 7, "app_id": 123, "permissions": map[string]string{"contents": "read"}}}}), nil
		case "/user/installations/7/repositories":
			return jsonResponse(http.StatusOK, map[string]any{"repositories": []any{map[string]any{"id": 42, "full_name": "octocat/repo", "owner": map[string]string{"login": "octocat"}, "default_branch": "main", "permissions": map[string]bool{"pull": true}}}}), nil
		default:
			return jsonResponse(http.StatusNotFound, nil), nil
		}
	})}
	previous := githubAPIClient
	githubAPIClient = client
	t.Cleanup(func() { githubAPIClient = previous })
	proof, err := app.VerifyRepositoryAccess(context.Background(), "user-token", 7, 42)
	if err != nil || proof.RepositoryID != 42 || proof.InstallationID != 7 {
		t.Fatalf("numeric app ID resolution failed: proof=%+v err=%v", proof, err)
	}

	app.clientID = "different-client"
	if _, err := app.VerifyRepositoryAccess(context.Background(), "user-token", 7, 42); !errors.Is(err, ErrGitHubAccess) {
		t.Fatalf("cross-App resolution accepted: %v", err)
	}
}

func TestVerifyRepositoryAccessRejectsInstallationOrRepositoryMismatch(t *testing.T) {
	for name, installation := range map[string]map[string]any{
		"cross app":        {"id": 7, "app_id": 999, "permissions": map[string]string{"contents": "write"}},
		"suspended":        {"id": 7, "app_id": 123, "suspended_at": "2026-01-01T00:00:00Z", "permissions": map[string]string{"contents": "write"}},
		"empty suspension": {"id": 7, "app_id": 123, "suspended_at": "", "permissions": map[string]string{"contents": "write"}},
		"no contents":      {"id": 7, "app_id": 123, "permissions": map[string]string{"issues": "write"}},
	} {
		t.Run(name, func(t *testing.T) {
			client := accessClientForInstallation(installation, map[string]any{"id": 42, "full_name": "octocat/repo", "owner": map[string]string{"login": "octocat"}, "default_branch": "main", "permissions": map[string]bool{"pull": true}})
			_, err := verifyRepositoryAccess(context.Background(), client, 123, "secret-user-token", 7, 42)
			if !errors.Is(err, ErrGitHubAccess) {
				t.Fatalf("error = %v, want ErrGitHubAccess", err)
			}
		})
	}
	for name, repository := range map[string]map[string]any{
		"wrong repo":  {"id": 43, "full_name": "octocat/repo", "owner": map[string]string{"login": "octocat"}, "default_branch": "main", "permissions": map[string]bool{"pull": true}},
		"no pull":     {"id": 42, "full_name": "octocat/repo", "owner": map[string]string{"login": "octocat"}, "default_branch": "main", "permissions": map[string]bool{"pull": false}},
		"wrong owner": {"id": 42, "full_name": "other/repo", "owner": map[string]string{"login": "octocat"}, "default_branch": "main", "permissions": map[string]bool{"pull": true}},
	} {
		t.Run(name, func(t *testing.T) {
			client := accessClientForInstallation(map[string]any{"id": 7, "app_id": 123, "permissions": map[string]string{"contents": "write"}}, repository)
			_, err := verifyRepositoryAccess(context.Background(), client, 123, "secret-user-token", 7, 42)
			if !errors.Is(err, ErrGitHubAccess) {
				t.Fatalf("error = %v, want ErrGitHubAccess", err)
			}
		})
	}
}

func TestVerifyRepositoryAccessAcceptsReadInstallationPermission(t *testing.T) {
	client := accessClientForInstallation(map[string]any{"id": 7, "app_id": 123, "permissions": map[string]string{"contents": "read"}}, map[string]any{"id": 42, "full_name": "octocat/repo", "owner": map[string]string{"login": "octocat"}, "default_branch": "main", "permissions": map[string]bool{"pull": true}})
	if _, err := verifyRepositoryAccess(context.Background(), client, 123, "secret-user-token", 7, 42); err != nil {
		t.Fatalf("read installation permission rejected: %v", err)
	}
}

func TestVerifyRepositoryAccessReportsListingLimit(t *testing.T) {
	client := &http.Client{Transport: accessTransport(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/user":
			return jsonResponse(http.StatusOK, map[string]any{"id": 99, "login": "alice"}), nil
		case "/user/installations":
			items := make([]any, 100)
			for i := range items {
				items[i] = map[string]any{"id": i + 100, "app_id": 123, "permissions": map[string]string{"contents": "read"}}
			}
			return jsonResponse(http.StatusOK, map[string]any{"installations": items}), nil
		default:
			return jsonResponse(http.StatusNotFound, nil), nil
		}
	})}
	_, err := verifyRepositoryAccess(context.Background(), client, 123, "token", 7, 42)
	if !errors.Is(err, ErrGitHubListingLimit) {
		t.Fatalf("error = %v, want listing limit", err)
	}
}

func TestVerifyRepositoryAccessRejectsRedirectAndDoesNotEchoToken(t *testing.T) {
	token := "secret-token-that-must-not-echo"
	calls := 0
	client := &http.Client{Transport: accessTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": []string{"https://evil.example/steal"}}, Body: io.NopCloser(strings.NewReader("provider secret detail"))}, nil
	})}
	_, err := verifyRepositoryAccess(context.Background(), client, 123, token, 7, 42)
	if !errors.Is(err, ErrGitHubAccess) || calls != 1 || strings.Contains(err.Error(), token) || strings.Contains(err.Error(), "provider secret detail") {
		t.Fatalf("redirect boundary failed: calls=%d err=%v", calls, err)
	}
}

func TestVerifyRepositoryAccessRejectsInvalidTokenAndContext(t *testing.T) {
	client := accessClientForInstallation(map[string]any{"id": 7, "app_id": 123, "permissions": map[string]string{"contents": "write"}}, map[string]any{"id": 42, "full_name": "octocat/repo", "owner": map[string]string{"login": "octocat"}, "default_branch": "main", "permissions": map[string]bool{"pull": true}})
	for _, token := range []string{"", "has space", strings.Repeat("x", maxGitHubTokenBytes+1)} {
		if _, err := verifyRepositoryAccess(context.Background(), client, 123, token, 7, 42); !errors.Is(err, ErrGitHubAccess) {
			t.Fatalf("token %q accepted: %v", token, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := verifyRepositoryAccess(ctx, client, 123, "token", 7, 42); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled context error = %v", err)
	}
}

func accessClientForInstallation(installation, repository map[string]any) *http.Client {
	return &http.Client{Transport: accessTransport(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/user":
			return jsonResponse(http.StatusOK, map[string]any{"id": 99, "login": "alice"}), nil
		case "/user/installations":
			return jsonResponse(http.StatusOK, map[string]any{"installations": []any{installation}}), nil
		case "/user/installations/7/repositories":
			return jsonResponse(http.StatusOK, map[string]any{"repositories": []any{repository}}), nil
		default:
			return jsonResponse(http.StatusNotFound, map[string]string{"error": "not found"}), nil
		}
	})}
}
