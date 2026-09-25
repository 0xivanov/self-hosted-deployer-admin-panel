package githubdeploy

import (
	"context"
	"net/url"
	"strconv"
	"time"
)

// InstallationURL returns the canonical GitHub App installation URL. The
// provider slug is read from authenticated /app metadata and is never taken
// from an unverified html_url field.
func (a *App) InstallationURL(ctx context.Context) (string, error) {
	if a == nil || !validAppClientID(a.clientID) {
		return "", ErrGitHubAccess
	}
	jwt, err := a.appJWT(time.Now())
	if err != nil {
		return "", ErrGitHubAccess
	}
	var metadata struct {
		ID       int64  `json:"id"`
		ClientID string `json:"client_id"`
		Slug     string `json:"slug"`
	}
	if err := githubGETJSON(ctx, githubAPIClient, jwt, "/app", &metadata); err != nil || !appIdentityMatches(a.clientID, metadata.ID, metadata.ClientID) || !validAppSlug(metadata.Slug) {
		return "", ErrGitHubAccess
	}
	return "https://github.com/apps/" + url.PathEscape(metadata.Slug) + "/installations/new", nil
}

func appIdentityMatches(configured string, id int64, clientID string) bool {
	if id <= 0 || !validAppClientID(clientID) {
		return false
	}
	numericID, err := strconv.ParseInt(configured, 10, 64)
	if err == nil && numericID > 0 {
		return numericID == id
	}
	return configured == clientID
}

func validAppSlug(slug string) bool {
	if slug == "" || len(slug) > 100 || slug[0] == '-' || slug[len(slug)-1] == '-' {
		return false
	}
	for _, r := range slug {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return false
	}
	return true
}
