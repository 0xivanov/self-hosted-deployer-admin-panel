package githubdeploy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

var ErrGitHubAccess = errors.New("GitHub repository access unavailable; check the app installation and repository access")

// InstallationToken is ephemeral. Never persist Value in project records or
// include it in an API response. It is restricted to one repository's contents.
type InstallationToken struct {
	Value        string    `json:"-"`
	RepositoryID int64     `json:"repository_id"`
	ExpiresAt    time.Time `json:"expires_at"`
}

func (InstallationToken) String() string   { return "[GitHub installation token redacted]" }
func (InstallationToken) GoString() string { return "[GitHub installation token redacted]" }

var githubAPIClient = &http.Client{
	Timeout:       20 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	Transport: &http.Transport{
		Proxy:                  nil,
		DialContext:            (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		TLSHandshakeTimeout:    5 * time.Second,
		ResponseHeaderTimeout:  10 * time.Second,
		DisableCompression:     true,
		MaxResponseHeaderBytes: 64 << 10,
		MaxIdleConns:           4,
		IdleConnTimeout:        30 * time.Second,
	},
}

// RepositoryToken requests only contents:read for an explicitly selected numeric
// repository ID. Callers must establish the portal user's installation/project
// authorization first; a successful app request alone does not prove ownership.
func (a *App) RepositoryToken(ctx context.Context, installationID, repositoryID int64) (InstallationToken, error) {
	if installationID <= 0 || repositoryID <= 0 {
		return InstallationToken{}, ErrGitHubAccess
	}
	if err := ctx.Err(); err != nil {
		return InstallationToken{}, err
	}
	now := time.Now()
	jwt, err := a.appJWT(now)
	if err != nil {
		return InstallationToken{}, err
	}
	return requestRepositoryToken(ctx, githubAPIClient, now, jwt, installationID, repositoryID)
}

func requestRepositoryToken(ctx context.Context, client *http.Client, now time.Time, jwt string, installationID, repositoryID int64) (InstallationToken, error) {
	empty := InstallationToken{}
	if client == nil || jwt == "" || installationID <= 0 || repositoryID <= 0 {
		return empty, ErrGitHubAccess
	}
	payload, _ := json.Marshal(struct {
		RepositoryIDs []int64           `json:"repository_ids"`
		Permissions   map[string]string `json:"permissions"`
	}{[]int64{repositoryID}, map[string]string{"contents": "read"}})
	endpoint := "https://api.github.com/app/installations/" + strconv.FormatInt(installationID, 10) + "/access_tokens"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return empty, ErrGitHubAccess
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	req.Header.Set("User-Agent", "Launchstead")
	// Refuse redirects even if a future caller supplies a differently configured
	// client. App credentials must only reach the fixed GitHub API endpoint.
	boundedClient := *client
	boundedClient.Timeout = 20 * time.Second
	boundedClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := boundedClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return empty, ctx.Err()
		}
		return empty, ErrGitHubAccess
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		return empty, ErrGitHubAccess
	}
	const maximum = 1 << 20
	raw, err := io.ReadAll(io.LimitReader(response.Body, maximum+1))
	if err != nil || len(raw) > maximum {
		return empty, ErrGitHubAccess
	}
	var result struct {
		Token        string            `json:"token"`
		ExpiresAt    time.Time         `json:"expires_at"`
		Permissions  map[string]string `json:"permissions"`
		Selection    string            `json:"repository_selection"`
		Repositories []struct {
			ID int64 `json:"id"`
		} `json:"repositories"`
	}
	if json.Unmarshal(raw, &result) != nil {
		return empty, ErrGitHubAccess
	}
	if result.Token == "" || len(result.Token) > 8192 || strings.ContainsFunc(result.Token, func(r rune) bool { return r < 0x21 || r > 0x7e }) || !result.ExpiresAt.After(now.Add(30*time.Second)) || result.ExpiresAt.After(now.Add(65*time.Minute)) {
		return empty, ErrGitHubAccess
	}
	if result.Selection != "selected" || len(result.Repositories) != 1 || result.Repositories[0].ID != repositoryID || result.Permissions["contents"] != "read" {
		return empty, ErrGitHubAccess
	}
	for name, level := range result.Permissions {
		if (name != "contents" && name != "metadata") || level != "read" {
			return empty, ErrGitHubAccess
		}
	}
	return InstallationToken{Value: result.Token, RepositoryID: repositoryID, ExpiresAt: result.ExpiresAt}, nil
}

// Compile-time checks keep ordinary and Go-syntax formatting redacted.
var _ fmt.Stringer = InstallationToken{}
var _ fmt.GoStringer = InstallationToken{}
