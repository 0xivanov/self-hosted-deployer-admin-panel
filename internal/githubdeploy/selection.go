package githubdeploy

import "strings"

// ValidSelection checks persisted source identifiers; it does not prove access.
func ValidSelection(access RepositoryAccess, branch, directory string) bool {
	owner, _, ok := strings.Cut(access.RepositoryFullName, "/")
	return access.UserID > 0 && validGitHubName(access.Login, 39) && access.InstallationID > 0 && access.RepositoryID > 0 && ok && validRepository(access.RepositoryFullName, owner) && validBranchRef("refs/heads/"+branch) && len(directory) <= 1024 && (directory == "" || directory == "." || archivePath(directory))
}
