package githubdeploy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

const sourceTestCommit = "0123456789abcdef0123456789abcdef01234567"

func TestResolveBranchWithTokenVerifiesRepositoryAndCommit(t *testing.T) {
	client := &http.Client{Transport: accessTransport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer installation-token" {
			t.Fatal("missing installation authorization")
		}
		switch r.URL.Path {
		case "/repositories/42":
			return jsonResponse(http.StatusOK, map[string]any{"id": 42, "full_name": "octocat/site"}), nil
		case "/repos/octocat/site/git/ref/heads/feature/x":
			return jsonResponse(http.StatusOK, map[string]any{"ref": "refs/heads/feature/x", "object": map[string]any{"type": "commit", "sha": strings.ToUpper(sourceTestCommit)}}), nil
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
			return nil, errors.New("unexpected path")
		}
	})}
	commit, err := resolveBranchWithToken(context.Background(), client, "installation-token", 42, "octocat/site", "octocat", "site", "feature/x")
	if err != nil || commit != sourceTestCommit {
		t.Fatalf("commit=%q err=%v", commit, err)
	}
}

func TestResolveBranchRejectsRepositoryRenameAndNonCommitRef(t *testing.T) {
	for i, identity := range []string{"octocat/renamed", ""} {
		name := "identity-" + strconv.Itoa(i)
		t.Run(name, func(t *testing.T) {
			client := &http.Client{Transport: accessTransport(func(r *http.Request) (*http.Response, error) {
				return jsonResponse(http.StatusOK, map[string]any{"id": 42, "full_name": identity}), nil
			})}
			if _, err := resolveBranchWithToken(context.Background(), client, "token", 42, "octocat/site", "octocat", "site", "main"); !errors.Is(err, ErrGitHubAccess) {
				t.Fatalf("error=%v, want ErrGitHubAccess", err)
			}
		})
	}
	client := &http.Client{Transport: accessTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/repositories/42" {
			return jsonResponse(http.StatusOK, map[string]any{"id": 42, "full_name": "octocat/site"}), nil
		}
		return jsonResponse(http.StatusOK, map[string]any{"ref": "refs/heads/main", "object": map[string]any{"type": "tag", "sha": sourceTestCommit}}), nil
	})}
	if _, err := resolveBranchWithToken(context.Background(), client, "token", 42, "octocat/site", "octocat", "site", "main"); !errors.Is(err, ErrGitHubAccess) {
		t.Fatalf("non-commit ref error=%v", err)
	}
}

func TestFetchCommitWithTokenFollowsOnlyValidatedCodeloadRedirect(t *testing.T) {
	archive := []byte("zip archive bytes")
	client := &http.Client{Transport: accessTransport(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Host {
		case "api.github.com":
			if r.URL.Path != "/repositories/42" {
				t.Fatalf("unexpected API path %s", r.URL.Path)
			}
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"full_name":"octocat/site"}`))}, nil
		case "codeload.github.com":
			if r.Header.Get("Authorization") != "" {
				t.Fatal("installation token sent to codeload")
			}
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(archive)))}, nil
		default:
			t.Fatalf("unexpected host %s", r.URL.Host)
			return nil, errors.New("unexpected host")
		}
	})}
	// Use a transport response for the zip endpoint by wrapping the test
	// transport's API response based on the request path.
	client.Transport = accessTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "api.github.com" && r.URL.Path == "/repositories/42" {
			return jsonResponse(http.StatusOK, map[string]any{"id": 42, "full_name": "octocat/site"}), nil
		}
		if r.URL.Host == "api.github.com" && strings.Contains(r.URL.Path, "/zipball/") {
			return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": []string{"https://codeload.github.com/octocat/site/legacy.zip/" + sourceTestCommit + "?token=signed-secret"}}, Body: io.NopCloser(strings.NewReader("redirect"))}, nil
		}
		if r.URL.Host == "codeload.github.com" {
			if r.Header.Get("Authorization") != "" {
				t.Fatal("installation token sent to codeload")
			}
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(archive)))}, nil
		}
		return nil, errors.New("unexpected request")
	})
	got, err := fetchCommitWithToken(context.Background(), client, "installation-token", 42, "octocat/site", "octocat", "site", sourceTestCommit)
	if err != nil || string(got) != string(archive) {
		t.Fatalf("archive=%q err=%v", got, err)
	}
}

func TestFetchArchiveRejectsUntrustedRedirect(t *testing.T) {
	for _, location := range []string{
		"https://evil.example/octocat/site/legacy.zip/" + sourceTestCommit,
		"https://codeload.github.com/other/site/legacy.zip/" + sourceTestCommit,
	} {
		client := &http.Client{Transport: accessTransport(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": []string{location}}, Body: io.NopCloser(strings.NewReader("redirect"))}, nil
		})}
		if _, err := fetchArchive(context.Background(), client, "token", "octocat", "site", sourceTestCommit); !errors.Is(err, ErrGitHubAccess) {
			t.Fatalf("location %q accepted: %v", location, err)
		}
	}
}

func TestValidCommit(t *testing.T) {
	if !ValidCommit(sourceTestCommit) || ValidCommit(strings.Repeat("0", 39)) || ValidCommit(strings.Repeat("z", 40)) {
		t.Fatal("commit validation failed")
	}
}
