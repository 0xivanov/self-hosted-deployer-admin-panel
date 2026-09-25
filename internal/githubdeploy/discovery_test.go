package githubdeploy

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"testing"
)

func TestListAccessibleRepositoriesReturnsEligibleRepositories(t *testing.T) {
	client := &http.Client{Transport: accessTransport(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/user":
			return jsonResponse(http.StatusOK, map[string]any{"id": 99, "login": "alice"}), nil
		case "/user/installations":
			return jsonResponse(http.StatusOK, map[string]any{"installations": []any{
				map[string]any{"id": 7, "app_id": 123, "permissions": map[string]string{"contents": "read"}},
				map[string]any{"id": 8, "app_id": 999, "permissions": map[string]string{"contents": "write"}},
				map[string]any{"id": 9, "app_id": 123, "suspended_at": "", "permissions": map[string]string{"contents": "write"}},
			}}), nil
		case "/user/installations/7/repositories":
			return jsonResponse(http.StatusOK, map[string]any{"repositories": []any{
				map[string]any{"id": 42, "full_name": "octocat/site", "owner": map[string]string{"login": "octocat"}, "default_branch": "main", "permissions": map[string]bool{"pull": true}},
				map[string]any{"id": 43, "full_name": "octocat/no-pull", "owner": map[string]string{"login": "octocat"}, "default_branch": "main", "permissions": map[string]bool{"pull": false}},
				map[string]any{"id": 44, "full_name": "wrong/site", "owner": map[string]string{"login": "octocat"}, "default_branch": "main", "permissions": map[string]bool{"pull": true}},
			}}), nil
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
			return nil, errors.New("unexpected path")
		}
	})}
	got, err := listAccessibleRepositories(context.Background(), client, 123, "token")
	if err != nil {
		t.Fatalf("listAccessibleRepositories: %v", err)
	}
	if len(got) != 1 || got[0].RepositoryID != 42 || got[0].InstallationID != 7 || got[0].RepositoryFullName != "octocat/site" {
		t.Fatalf("unexpected repositories: %+v", got)
	}
}

func TestListAccessibleRepositoriesReportsInstallationListingLimit(t *testing.T) {
	client := &http.Client{Transport: accessTransport(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/user":
			return jsonResponse(http.StatusOK, map[string]any{"id": 99, "login": "alice"}), nil
		case "/user/installations":
			items := make([]any, 100)
			for i := range items {
				items[i] = map[string]any{"id": i + 1, "app_id": 999, "permissions": map[string]string{"contents": "read"}}
			}
			return jsonResponse(http.StatusOK, map[string]any{"installations": items}), nil
		default:
			return jsonResponse(http.StatusNotFound, nil), nil
		}
	})}
	_, err := listAccessibleRepositories(context.Background(), client, 123, "token")
	if !errors.Is(err, ErrGitHubListingLimit) {
		t.Fatalf("error = %v, want ErrGitHubListingLimit", err)
	}
}

func TestListAccessibleRepositoriesReportsRepositoryListingLimit(t *testing.T) {
	client := &http.Client{Transport: accessTransport(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/user":
			return jsonResponse(http.StatusOK, map[string]any{"id": 99, "login": "alice"}), nil
		case "/user/installations":
			return jsonResponse(http.StatusOK, map[string]any{"installations": []any{map[string]any{"id": 7, "app_id": 123, "permissions": map[string]string{"contents": "read"}}}}), nil
		case "/user/installations/7/repositories":
			items := make([]any, 100)
			for i := range items {
				items[i] = map[string]any{"id": i + 1, "full_name": "octocat/site", "owner": map[string]string{"login": "octocat"}, "default_branch": "main", "permissions": map[string]bool{"pull": true}}
			}
			return jsonResponse(http.StatusOK, map[string]any{"repositories": items}), nil
		default:
			return jsonResponse(http.StatusNotFound, nil), nil
		}
	})}
	_, err := listAccessibleRepositories(context.Background(), client, 123, "token")
	if !errors.Is(err, ErrGitHubListingLimit) {
		t.Fatalf("error = %v, want ErrGitHubListingLimit", err)
	}
}

func TestListAccessibleRepositoriesReportsGlobalResultLimit(t *testing.T) {
	client := &http.Client{Transport: accessTransport(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/user":
			return jsonResponse(http.StatusOK, map[string]any{"id": 99, "login": "alice"}), nil
		case "/user/installations":
			return jsonResponse(http.StatusOK, map[string]any{"installations": []any{
				map[string]any{"id": 7, "app_id": 123, "permissions": map[string]string{"contents": "read"}},
				map[string]any{"id": 8, "app_id": 123, "permissions": map[string]string{"contents": "read"}},
			}}), nil
		case "/user/installations/7/repositories", "/user/installations/8/repositories":
			installationID := int64(7)
			if r.URL.Path == "/user/installations/8/repositories" {
				installationID = 8
			}
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			items := make([]any, 100)
			for i := range items {
				id := (page-1)*100 + i + 1
				if installationID == 8 {
					id += 1000
				}
				items[i] = map[string]any{"id": id, "full_name": "octocat/site-" + strconv.Itoa(id), "owner": map[string]string{"login": "octocat"}, "default_branch": "main", "permissions": map[string]bool{"pull": true}}
			}
			return jsonResponse(http.StatusOK, map[string]any{"repositories": items}), nil
		default:
			return jsonResponse(http.StatusNotFound, nil), nil
		}
	})}
	_, err := listAccessibleRepositories(context.Background(), client, 123, "token")
	if !errors.Is(err, ErrGitHubListingLimit) {
		t.Fatalf("error = %v, want ErrGitHubListingLimit", err)
	}
}

func TestListAccessibleRepositoriesReportsRequestBudget(t *testing.T) {
	client := &http.Client{Transport: accessTransport(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/user":
			return jsonResponse(http.StatusOK, map[string]any{"id": 99, "login": "alice"}), nil
		case "/user/installations":
			items := make([]any, 100)
			for i := range items {
				items[i] = map[string]any{"id": i + 1, "app_id": 123, "permissions": map[string]string{"contents": "read"}}
			}
			return jsonResponse(http.StatusOK, map[string]any{"installations": items}), nil
		default:
			return jsonResponse(http.StatusNotFound, nil), nil
		}
	})}
	budget := 1
	_, err := listAccessibleRepositoriesWithBudget(context.Background(), client, 123, "token", &budget)
	if !errors.Is(err, ErrGitHubListingLimit) {
		t.Fatalf("error = %v, want ErrGitHubListingLimit", err)
	}
}
