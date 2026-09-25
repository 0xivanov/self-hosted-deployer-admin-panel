package githubdeploy

import (
	"context"
	"encoding/hex"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const maxSourceArchive = 10 << 20

// Source is an immutable source snapshot identified by its full commit SHA.
type Source struct {
	Commit  string
	Archive []byte
}

// ValidCommit reports whether value is a full 40-character Git commit SHA.
func ValidCommit(value string) bool {
	if len(value) != 40 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 20 && !strings.EqualFold(value, strings.Repeat("0", 40))
}

// ResolveBranch verifies the selected repository and resolves one branch to
// an immutable commit. The returned SHA must be persisted before fetching the
// archive so retries cannot silently follow a later branch update.
func (a *App) ResolveBranch(ctx context.Context, installationID, repositoryID int64, repository, branch string) (string, error) {
	owner, name, err := validateSourceSelection(installationID, repositoryID, repository, branch)
	if err != nil {
		return "", err
	}
	checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	token, err := a.RepositoryToken(checkCtx, installationID, repositoryID)
	if err != nil {
		return "", sourceError(checkCtx, err)
	}
	return resolveBranchWithToken(checkCtx, githubAPIClient, token.Value, repositoryID, repository, owner, name, branch)
}

// FetchCommit downloads the archive for exactly commit. It never resolves a
// branch and therefore remains stable when a branch moves between retries.
func (a *App) FetchCommit(ctx context.Context, installationID, repositoryID int64, repository, commit string) ([]byte, error) {
	owner, name, err := validateSourceRepository(installationID, repositoryID, repository)
	if err != nil || !ValidCommit(commit) {
		return nil, ErrGitHubAccess
	}
	checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	token, err := a.RepositoryToken(checkCtx, installationID, repositoryID)
	if err != nil {
		return nil, sourceError(checkCtx, err)
	}
	return fetchCommitWithToken(checkCtx, githubAPIClient, token.Value, repositoryID, repository, owner, name, strings.ToLower(commit))
}

// FetchSource resolves branch and downloads the corresponding immutable
// snapshot. Callers that need durable restart semantics should use
// ResolveBranch followed by FetchCommit instead.
func (a *App) FetchSource(ctx context.Context, installationID, repositoryID int64, repository, branch string) (Source, error) {
	commit, err := a.ResolveBranch(ctx, installationID, repositoryID, repository, branch)
	if err != nil {
		return Source{}, err
	}
	archive, err := a.FetchCommit(ctx, installationID, repositoryID, repository, commit)
	if err != nil {
		return Source{}, err
	}
	return Source{Commit: commit, Archive: archive}, nil
}

func validateSourceSelection(installationID, repositoryID int64, repository, branch string) (string, string, error) {
	owner, name, err := validateSourceRepository(installationID, repositoryID, repository)
	if err != nil || !validDefaultBranch(branch) {
		return "", "", ErrGitHubAccess
	}
	return owner, name, nil
}

func validateSourceRepository(installationID, repositoryID int64, repository string) (string, string, error) {
	if installationID <= 0 || repositoryID <= 0 {
		return "", "", ErrGitHubAccess
	}
	parts := strings.Split(repository, "/")
	if len(parts) != 2 || !validRepository(repository, parts[0]) {
		return "", "", ErrGitHubAccess
	}
	return parts[0], parts[1], nil
}

func resolveBranchWithToken(ctx context.Context, client *http.Client, token string, repositoryID int64, repository, owner, name, branch string) (string, error) {
	if client == nil || validateGitHubUserToken(token) != nil || repositoryID <= 0 || !validRepository(repository, owner) || !validDefaultBranch(branch) {
		return "", ErrGitHubAccess
	}
	var identity struct {
		ID       int64  `json:"id"`
		FullName string `json:"full_name"`
	}
	if err := githubGETJSON(ctx, client, token, "/repositories/"+strconv.FormatInt(repositoryID, 10), &identity); err != nil || identity.ID != repositoryID || identity.FullName != repository {
		return "", sourceError(ctx, err)
	}
	path := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/git/ref/heads/" + escapeBranchPath(branch)
	var ref struct {
		Ref    string `json:"ref"`
		Object struct {
			Type string `json:"type"`
			SHA  string `json:"sha"`
		} `json:"object"`
	}
	if err := githubGETJSON(ctx, client, token, path, &ref); err != nil || ref.Ref != "refs/heads/"+branch || ref.Object.Type != "commit" || !ValidCommit(ref.Object.SHA) {
		return "", sourceError(ctx, err)
	}
	return strings.ToLower(ref.Object.SHA), nil
}

func fetchCommitWithToken(ctx context.Context, client *http.Client, token string, repositoryID int64, repository, owner, name, commit string) ([]byte, error) {
	if client == nil || validateGitHubUserToken(token) != nil || repositoryID <= 0 || !validRepository(repository, owner) || !ValidCommit(commit) {
		return nil, ErrGitHubAccess
	}
	var identity struct {
		ID       int64  `json:"id"`
		FullName string `json:"full_name"`
	}
	if err := githubGETJSON(ctx, client, token, "/repositories/"+strconv.FormatInt(repositoryID, 10), &identity); err != nil || identity.ID != repositoryID || identity.FullName != repository {
		return nil, sourceError(ctx, err)
	}
	return fetchArchive(ctx, client, token, owner, name, strings.ToLower(commit))
}

func fetchArchive(ctx context.Context, client *http.Client, token, owner, name, commit string) ([]byte, error) {
	endpoint := "https://api.github.com/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/zipball/" + commit
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, ErrGitHubAccess
	}
	setGitHubHeaders(req, token)
	response, err := doNoRedirect(client, req)
	if err != nil {
		return nil, sourceError(ctx, err)
	}
	defer response.Body.Close()
	if response.StatusCode >= http.StatusMultipleChoices && response.StatusCode < http.StatusBadRequest {
		location := response.Header.Get("Location")
		if !validCodeloadLocation(location, owner, name, commit) {
			return nil, ErrGitHubAccess
		}
		redirect, err := http.NewRequestWithContext(ctx, http.MethodGet, location, nil)
		if err != nil {
			return nil, ErrGitHubAccess
		}
		response.Body.Close()
		response, err = doNoRedirect(client, redirect)
		if err != nil {
			return nil, sourceError(ctx, err)
		}
		defer response.Body.Close()
	}
	if response.StatusCode != http.StatusOK {
		return nil, ErrGitHubAccess
	}
	archive, err := io.ReadAll(io.LimitReader(response.Body, maxSourceArchive+1))
	if err != nil || len(archive) > maxSourceArchive {
		return nil, ErrGitHubAccess
	}
	return archive, nil
}

func doNoRedirect(client *http.Client, request *http.Request) (*http.Response, error) {
	bounded := *client
	bounded.Timeout = 30 * time.Second
	bounded.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return bounded.Do(request)
}

func setGitHubHeaders(request *http.Request, token string) {
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	request.Header.Set("User-Agent", "Launchstead")
}

func escapeBranchPath(branch string) string {
	parts := strings.Split(branch, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}

func validCodeloadLocation(raw, owner, name, commit string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || len(raw) > 8192 || parsed.Scheme != "https" || parsed.Host != "codeload.github.com" || parsed.User != nil || parsed.Fragment != "" {
		return false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 4 || parts[0] != owner || parts[1] != name || (parts[2] != "legacy.zip" && parts[2] != "zip") || !strings.EqualFold(parts[3], commit) {
		return false
	}
	return ValidCommit(parts[3])
}

func sourceError(ctx context.Context, err error) error {
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return ErrGitHubAccess
}
