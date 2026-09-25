package githubdeploy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxGitHubAccessResponse = 1 << 20
	// GitHub listings are bounded to 1,000 items per verification. Callers
	// should narrow a user's selected installation/repository access when the
	// provider returns more pages.
	maxGitHubAccessPages = 10
	maxGitHubTokenBytes  = 8192
)

var ErrGitHubListingLimit = errors.New("GitHub repository access listing limit reached; narrow repository access")

// RepositoryAccess is provider proof only. Callers must still authorize the
// portal user, bind this result to a project, and prevent replay with a
// single-use linking state protected by the portal's normal CSRF/session
// checks.
type RepositoryAccess struct {
	UserID             int64
	Login              string
	InstallationID     int64
	RepositoryID       int64
	RepositoryFullName string
	DefaultBranch      string
}

type githubUser struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
}

type githubInstallation struct {
	ID          int64             `json:"id"`
	AppID       int64             `json:"app_id"`
	SuspendedAt *string           `json:"suspended_at"`
	Permissions map[string]string `json:"permissions"`
}

type githubApp struct {
	ID       int64  `json:"id"`
	ClientID string `json:"client_id"`
}

type githubInstallationPage struct {
	Installations []githubInstallation `json:"installations"`
}

type githubRepository struct {
	ID            int64  `json:"id"`
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
	Owner         struct {
		Login string `json:"login"`
	} `json:"owner"`
	Permissions map[string]bool `json:"permissions"`
}

type githubRepositoryPage struct {
	Repositories []githubRepository `json:"repositories"`
}

// VerifyRepositoryAccess proves that the GitHub user token can see a
// non-suspended installation of this App and pull one selected repository.
// The token is used only for these requests and is not retained.
func (a *App) VerifyRepositoryAccess(ctx context.Context, userToken string, installationID, repositoryID int64) (RepositoryAccess, error) {
	if err := validateGitHubUserToken(userToken); err != nil || installationID <= 0 || repositoryID <= 0 || a == nil || !validAppClientID(a.clientID) {
		return RepositoryAccess{}, ErrGitHubAccess
	}
	checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	appID, err := a.resolveAppID(checkCtx, githubAPIClient)
	if err != nil {
		return RepositoryAccess{}, accessError(checkCtx, err)
	}
	return verifyRepositoryAccess(checkCtx, githubAPIClient, appID, userToken, installationID, repositoryID)
}

func (a *App) resolveAppID(ctx context.Context, client *http.Client) (int64, error) {
	if a == nil || client == nil || !validAppClientID(a.clientID) {
		return 0, ErrGitHubAccess
	}
	jwt, err := a.appJWT(time.Now())
	if err != nil {
		return 0, ErrGitHubAccess
	}
	var providerApp githubApp
	if err := githubGETJSON(ctx, client, jwt, "/app", &providerApp); err != nil || providerApp.ID <= 0 || !validAppClientID(providerApp.ClientID) {
		return 0, accessError(ctx, err)
	}
	if numericID, parseErr := strconv.ParseInt(a.clientID, 10, 64); parseErr == nil && numericID > 0 {
		if providerApp.ID != numericID {
			return 0, ErrGitHubAccess
		}
	} else if providerApp.ClientID != a.clientID {
		return 0, ErrGitHubAccess
	}
	return providerApp.ID, nil
}

func verifyRepositoryAccess(ctx context.Context, client *http.Client, appID int64, userToken string, installationID, repositoryID int64) (RepositoryAccess, error) {
	var empty RepositoryAccess
	if client == nil || appID <= 0 || validateGitHubUserToken(userToken) != nil || installationID <= 0 || repositoryID <= 0 {
		return empty, ErrGitHubAccess
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	var user githubUser
	if err := githubGETJSON(ctx, client, userToken, "/user", &user); err != nil || user.ID <= 0 || !validGitHubName(user.Login, 39) {
		return empty, accessError(ctx, err)
	}

	var installation *githubInstallation
	for page := 1; page <= maxGitHubAccessPages; page++ {
		var result githubInstallationPage
		path := "/user/installations?per_page=100&page=" + strconv.Itoa(page)
		if err := githubGETJSON(ctx, client, userToken, path, &result); err != nil {
			return empty, accessError(ctx, err)
		}
		if len(result.Installations) > 100 {
			return empty, ErrGitHubAccess
		}
		for i := range result.Installations {
			candidate := &result.Installations[i]
			if candidate.ID != installationID {
				continue
			}
			if !installationBelongsToApp(*candidate, appID) || candidate.SuspendedAt != nil || (candidate.Permissions["contents"] != "read" && candidate.Permissions["contents"] != "write") {
				return empty, ErrGitHubAccess
			}
			installation = candidate
			break
		}
		if installation != nil || len(result.Installations) < 100 {
			break
		}
		if page == maxGitHubAccessPages {
			return empty, ErrGitHubListingLimit
		}
	}
	if installation == nil {
		return empty, ErrGitHubAccess
	}

	var repository *githubRepository
	for page := 1; page <= maxGitHubAccessPages; page++ {
		var result githubRepositoryPage
		path := "/user/installations/" + strconv.FormatInt(installationID, 10) + "/repositories?per_page=100&page=" + strconv.Itoa(page)
		if err := githubGETJSON(ctx, client, userToken, path, &result); err != nil {
			return empty, accessError(ctx, err)
		}
		if len(result.Repositories) > 100 {
			return empty, ErrGitHubAccess
		}
		for i := range result.Repositories {
			candidate := &result.Repositories[i]
			if candidate.ID != repositoryID {
				continue
			}
			if !validRepository(candidate.FullName, candidate.Owner.Login) || !validDefaultBranch(candidate.DefaultBranch) || !candidate.Permissions["pull"] {
				return empty, ErrGitHubAccess
			}
			repository = candidate
			break
		}
		if repository != nil || len(result.Repositories) < 100 {
			break
		}
		if page == maxGitHubAccessPages {
			return empty, ErrGitHubListingLimit
		}
	}
	if repository == nil {
		return empty, ErrGitHubAccess
	}
	return RepositoryAccess{UserID: user.ID, Login: user.Login, InstallationID: installation.ID, RepositoryID: repository.ID, RepositoryFullName: repository.FullName, DefaultBranch: repository.DefaultBranch}, nil
}

func validateGitHubUserToken(token string) error {
	if token == "" || len(token) > maxGitHubTokenBytes || !utf8.ValidString(token) {
		return ErrGitHubAccess
	}
	for _, r := range token {
		if r < 0x21 || r > 0x7e {
			return ErrGitHubAccess
		}
	}
	return nil
}

func installationBelongsToApp(installation githubInstallation, appID int64) bool {
	return installation.AppID > 0 && installation.AppID == appID
}

func validDefaultBranch(branch string) bool {
	return validBranchRef("refs/heads/" + branch)
}

func githubGETJSON(ctx context.Context, client *http.Client, token, path string, output any) error {
	if !strings.HasPrefix(path, "/") || strings.Contains(path, "#") {
		return ErrGitHubAccess
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com"+path, nil)
	if err != nil {
		return ErrGitHubAccess
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	req.Header.Set("User-Agent", "Launchstead")
	bounded := *client
	bounded.Timeout = 30 * time.Second
	bounded.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := bounded.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrGitHubAccess
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return ErrGitHubAccess
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxGitHubAccessResponse+1))
	if err != nil || len(raw) > maxGitHubAccessResponse {
		return ErrGitHubAccess
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(output); err != nil {
		return ErrGitHubAccess
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return ErrGitHubAccess
	}
	return nil
}

func accessError(ctx context.Context, err error) error {
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return ErrGitHubAccess
}
