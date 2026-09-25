package githubdeploy

import (
	"context"
	"net/http"
	"strconv"
	"time"
)

const (
	maxDiscoveryResults  = 1000
	maxDiscoveryRequests = 30
)

// ListAccessibleRepositories returns every repository that this App can use
// through a non-suspended installation belonging to the authenticated user.
// The result is provider proof only. Callers must still bind a selected
// repository to an authenticated portal user and protect that binding from
// replay.
func (a *App) ListAccessibleRepositories(ctx context.Context, userToken string) ([]RepositoryAccess, error) {
	if a == nil || validateGitHubUserToken(userToken) != nil || !validAppClientID(a.clientID) {
		return nil, ErrGitHubAccess
	}
	checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	appID, err := a.resolveAppID(checkCtx, githubAPIClient)
	if err != nil {
		return nil, accessError(checkCtx, err)
	}
	// /app was already queried by resolveAppID, so reserve one request from
	// the aggregate discovery budget for it.
	budget := maxDiscoveryRequests - 1
	return listAccessibleRepositoriesWithBudget(checkCtx, githubAPIClient, appID, userToken, &budget)
}

func listAccessibleRepositories(ctx context.Context, client *http.Client, appID int64, userToken string) ([]RepositoryAccess, error) {
	budget := maxDiscoveryRequests
	return listAccessibleRepositoriesWithBudget(ctx, client, appID, userToken, &budget)
}

func listAccessibleRepositoriesWithBudget(ctx context.Context, client *http.Client, appID int64, userToken string, budget *int) ([]RepositoryAccess, error) {
	if client == nil || appID <= 0 || validateGitHubUserToken(userToken) != nil {
		return nil, ErrGitHubAccess
	}
	if budget == nil || *budget <= 0 {
		return nil, ErrGitHubListingLimit
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var user githubUser
	if !consumeDiscoveryRequest(budget) {
		return nil, ErrGitHubListingLimit
	}
	if err := githubGETJSON(ctx, client, userToken, "/user", &user); err != nil || user.ID <= 0 || !validGitHubName(user.Login, 39) {
		return nil, accessError(ctx, err)
	}

	result := make([]RepositoryAccess, 0)
	seen := make(map[[2]int64]struct{})
	for page := 1; page <= maxGitHubAccessPages; page++ {
		var installations githubInstallationPage
		path := "/user/installations?per_page=100&page=" + strconv.Itoa(page)
		if !consumeDiscoveryRequest(budget) {
			return nil, ErrGitHubListingLimit
		}
		if err := githubGETJSON(ctx, client, userToken, path, &installations); err != nil {
			return nil, accessError(ctx, err)
		}
		if len(installations.Installations) > 100 {
			return nil, ErrGitHubAccess
		}
		for _, installation := range installations.Installations {
			if installation.ID <= 0 || !installationBelongsToApp(installation, appID) || installation.SuspendedAt != nil || (installation.Permissions["contents"] != "read" && installation.Permissions["contents"] != "write") {
				continue
			}
			repositories, err := listInstallationRepositories(ctx, client, user, installation, userToken, budget)
			if err != nil {
				return nil, err
			}
			for _, access := range repositories {
				key := [2]int64{access.InstallationID, access.RepositoryID}
				if _, ok := seen[key]; ok {
					continue
				}
				if len(result) >= maxDiscoveryResults {
					return nil, ErrGitHubListingLimit
				}
				seen[key] = struct{}{}
				result = append(result, access)
			}
		}
		if len(installations.Installations) < 100 {
			break
		}
		if page == maxGitHubAccessPages {
			return nil, ErrGitHubListingLimit
		}
	}
	return result, nil
}

func listInstallationRepositories(ctx context.Context, client *http.Client, user githubUser, installation githubInstallation, userToken string, budget *int) ([]RepositoryAccess, error) {
	result := make([]RepositoryAccess, 0)
	for page := 1; page <= maxGitHubAccessPages; page++ {
		var repositories githubRepositoryPage
		path := "/user/installations/" + strconv.FormatInt(installation.ID, 10) + "/repositories?per_page=100&page=" + strconv.Itoa(page)
		if !consumeDiscoveryRequest(budget) {
			return nil, ErrGitHubListingLimit
		}
		if err := githubGETJSON(ctx, client, userToken, path, &repositories); err != nil {
			return nil, accessError(ctx, err)
		}
		if len(repositories.Repositories) > 100 {
			return nil, ErrGitHubAccess
		}
		for _, repository := range repositories.Repositories {
			if !validRepository(repository.FullName, repository.Owner.Login) || !validDefaultBranch(repository.DefaultBranch) || !repository.Permissions["pull"] || repository.ID <= 0 {
				continue
			}
			result = append(result, RepositoryAccess{
				UserID:             user.ID,
				Login:              user.Login,
				InstallationID:     installation.ID,
				RepositoryID:       repository.ID,
				RepositoryFullName: repository.FullName,
				DefaultBranch:      repository.DefaultBranch,
			})
			if len(result) > maxDiscoveryResults {
				return nil, ErrGitHubListingLimit
			}
		}
		if len(repositories.Repositories) < 100 {
			break
		}
		if page == maxGitHubAccessPages {
			return nil, ErrGitHubListingLimit
		}
	}
	return result, nil
}

func consumeDiscoveryRequest(budget *int) bool {
	if budget == nil || *budget <= 0 {
		return false
	}
	(*budget)--
	return true
}
