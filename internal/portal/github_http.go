package portal

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/githubdeploy"
)

type GitHubAccessProvider interface {
	VerifyRepositoryAccess(context.Context, string, int64, int64) (githubdeploy.RepositoryAccess, error)
	ListAccessibleRepositories(context.Context, string) ([]githubdeploy.RepositoryAccess, error)
}
type GitHubOAuthProvider interface {
	AuthorizationURL(string, string) (string, error)
	Exchange(context.Context, string, string) (githubdeploy.OAuthToken, error)
}

// Temporary user tokens exist only in bounded process memory while selecting a
// repository. Restart/expiry asks the user to reconnect; never persist these tokens.
type githubBrowserFlow struct {
	project, sessionHash string
	expires              time.Time
	attempt              GitHubLinkAttempt
	userToken            string
}

func (githubBrowserFlow) String() string   { return "[GitHub browser flow redacted]" }
func (githubBrowserFlow) GoString() string { return "[GitHub browser flow redacted]" }

type githubBrowserFlows struct {
	sync.Mutex
	starts     map[string]githubBrowserFlow
	selections map[string]githubBrowserFlow
}

func (f *githubBrowserFlows) cleanup(now time.Time) {
	for key, flow := range f.starts {
		if !flow.expires.After(now) {
			delete(f.starts, key)
		}
	}
	for key, flow := range f.selections {
		if !flow.expires.After(now) {
			delete(f.selections, key)
		}
	}
}
func githubVerifier(session, state string) string {
	mac := hmac.New(sha256.New, []byte(session))
	mac.Write([]byte("launchstead-github-pkce:" + state))
	return hex.EncodeToString(mac.Sum(nil))
}
func githubSelectionKey(session, project string) string { return digest(session) + ":" + project }
func (h *HTTP) githubError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrGitHubImportLimit):
		httpError(w, 409, "GitHub import history is full. Contact support to archive old imports.")
	case errors.Is(err, ErrConflict):
		httpError(w, 409, "An import is already pending or this request belongs to an earlier connection. Refresh import status before retrying.")
	case errors.Is(err, ErrGitHubLink):
		httpError(w, 409, "GitHub connection expired or changed. Connect GitHub again.")
	case errors.Is(err, ErrGitHubLinkRate):
		httpError(w, 429, ErrGitHubLinkRate.Error())
	case errors.Is(err, githubdeploy.ErrGitHubListingLimit):
		httpError(w, 409, "Too many repositories to list. Limit the GitHub App to the repositories you want to deploy.")
	case errors.Is(err, githubdeploy.ErrGitHubAccess), errors.Is(err, githubdeploy.ErrGitHubOAuth):
		httpError(w, 502, "GitHub access could not be verified. Check the App's repository access and connect again.")
	default:
		h.storeError(w, err)
	}
}
func (h *HTTP) githubHTTP(w http.ResponseWriter, r *http.Request, session string) {
	if h.githubApp == nil || h.githubOAuth == nil {
		httpError(w, 404, "GitHub connections are not enabled")
		return
	}
	ctx := r.Context()
	if r.URL.Path == "/api/github/imports" {
		if _, ok := h.githubApp.(GitHubSourceProvider); !ok {
			httpError(w, 404, "GitHub imports are not enabled")
			return
		}
		if r.Method == "GET" {
			jobs, err := h.store.GitHubImports(ctx, session, r.URL.Query().Get("project"))
			if err != nil {
				h.githubError(w, err)
				return
			}
			httpJSON(w, map[string]any{"imports": jobs})
			return
		}
		var input struct {
			Project    string `json:"project"`
			RequestKey string `json:"request_key"`
		}
		if !httpDecode(w, r, &input) {
			return
		}
		job, err := h.store.RequestGitHubImport(ctx, session, input.Project, input.RequestKey)
		if err != nil {
			h.githubError(w, err)
			return
		}
		httpJSON(w, map[string]any{"import": job})
		return
	}
	if r.Method == "GET" {
		project := r.URL.Query().Get("project")
		connection, err := h.store.ProjectGitHubConnection(ctx, session, project)
		if err != nil {
			h.githubError(w, err)
			return
		}
		h.githubFlows.Lock()
		h.githubFlows.cleanup(time.Now())
		flow, pending := h.githubFlows.selections[githubSelectionKey(session, project)]
		h.githubFlows.Unlock()
		switch r.URL.Path {
		case "/api/github/connection":
			httpJSON(w, map[string]any{"connection": connection, "pending": pending})
		case "/api/github/repositories":
			if !pending {
				h.githubError(w, ErrGitHubLink)
				return
			}
			if err := h.store.githubCompletionCurrent(ctx, session, flow.attempt); err != nil {
				h.githubError(w, err)
				return
			}
			repos, err := h.githubApp.ListAccessibleRepositories(ctx, flow.userToken)
			if err != nil {
				h.githubError(w, err)
				return
			}
			httpJSON(w, map[string]any{"repositories": repos})
		default:
			httpError(w, 404, "Not found")
		}
		return
	}
	if r.Method != "POST" {
		httpError(w, 405, "Use GET or POST")
		return
	}
	switch r.URL.Path {
	case "/api/github/start":
		var input struct {
			Project string `json:"project"`
		}
		if !httpDecode(w, r, &input) {
			return
		}
		// Serialize starts so a successful old begin cannot replace a newer UI flow.
		h.githubFlows.Lock()
		defer h.githubFlows.Unlock()
		h.githubFlows.cleanup(time.Now())
		if len(h.githubFlows.starts)+len(h.githubFlows.selections) >= 128 {
			httpError(w, 429, "GitHub connections are busy. Retry shortly.")
			return
		}
		state, err := h.store.BeginGitHubLink(ctx, session, input.Project)
		if err != nil {
			h.githubError(w, err)
			return
		}
		target, err := h.githubOAuth.AuthorizationURL(state.State, githubVerifier(session, state.State))
		if err != nil {
			h.githubError(w, err)
			return
		}
		// The provider is operator-configured, but still prevent arbitrary redirects.
		if !strings.HasPrefix(target, "https://github.com/login/oauth/authorize?") {
			h.githubError(w, githubdeploy.ErrGitHubOAuth)
			return
		}
		for key, flow := range h.githubFlows.starts {
			if flow.project == input.Project {
				delete(h.githubFlows.starts, key)
			}
		}
		for key, flow := range h.githubFlows.selections {
			if flow.project == input.Project {
				delete(h.githubFlows.selections, key)
			}
		}
		h.githubFlows.starts[digest(state.State)] = githubBrowserFlow{project: input.Project, sessionHash: digest(session), expires: time.Unix(state.ExpiresAt, 0)}
		httpJSON(w, map[string]string{"url": target})
	case "/api/github/callback":
		var input struct {
			Code  string `json:"code"`
			State string `json:"state"`
		}
		if !httpDecode(w, r, &input) {
			return
		}
		h.githubFlows.Lock()
		h.githubFlows.cleanup(time.Now())
		flow, ok := h.githubFlows.starts[digest(input.State)]
		if ok && flow.sessionHash == digest(session) {
			delete(h.githubFlows.starts, digest(input.State))
		} else {
			ok = false
		}
		h.githubFlows.Unlock()
		if !ok {
			h.githubError(w, ErrGitHubLink)
			return
		}
		attempt, err := h.store.ConsumeGitHubLink(ctx, session, flow.project, input.State)
		if err != nil {
			h.githubError(w, err)
			return
		}
		userToken, err := h.githubOAuth.Exchange(ctx, input.Code, githubVerifier(session, input.State))
		if err != nil {
			h.githubError(w, err)
			return
		}
		flow.attempt = attempt
		flow.userToken = userToken.AccessToken
		// An in-flight exchange cannot extend its ten-minute browser authorization.
		if !flow.expires.After(time.Now()) {
			h.githubError(w, ErrGitHubLink)
			return
		}
		if !userToken.ExpiresAt.IsZero() && userToken.ExpiresAt.Before(flow.expires) {
			flow.expires = userToken.ExpiresAt
		}
		h.githubFlows.Lock()
		h.githubFlows.cleanup(time.Now())
		if len(h.githubFlows.starts)+len(h.githubFlows.selections) >= 128 {
			h.githubFlows.Unlock()
			httpError(w, 429, "GitHub connections are busy. Connect again shortly.")
			return
		}
		if err = h.store.githubCompletionCurrent(ctx, session, attempt); err != nil {
			h.githubFlows.Unlock()
			h.githubError(w, err)
			return
		}
		h.githubFlows.selections[githubSelectionKey(session, flow.project)] = flow
		h.githubFlows.Unlock()
		httpJSON(w, map[string]string{"project": flow.project, "workspace": attempt.WorkspaceID})
	case "/api/github/connect":
		var input struct {
			Project        string `json:"project"`
			InstallationID int64  `json:"installation_id"`
			RepositoryID   int64  `json:"repository_id"`
			Branch         string `json:"branch"`
			Directory      string `json:"directory"`
		}
		if !httpDecode(w, r, &input) {
			return
		}
		h.githubFlows.Lock()
		h.githubFlows.cleanup(time.Now())
		flow, ok := h.githubFlows.selections[githubSelectionKey(session, input.Project)]
		h.githubFlows.Unlock()
		if !ok {
			h.githubError(w, ErrGitHubLink)
			return
		}
		access, err := h.githubApp.VerifyRepositoryAccess(ctx, flow.userToken, input.InstallationID, input.RepositoryID)
		if err != nil {
			h.githubError(w, err)
			return
		}
		connection, err := h.store.SaveGitHubConnection(ctx, session, flow.attempt, access, input.Branch, input.Directory, false)
		if err != nil {
			h.githubError(w, err)
			return
		}
		h.githubFlows.Lock()
		key := githubSelectionKey(session, input.Project)
		if current, exists := h.githubFlows.selections[key]; exists && current.attempt.completion == flow.attempt.completion {
			delete(h.githubFlows.selections, key)
		}
		h.githubFlows.Unlock()
		httpJSON(w, map[string]any{"connection": connection})
	case "/api/github/disconnect":
		var input struct {
			Project string `json:"project"`
		}
		if !httpDecode(w, r, &input) {
			return
		}
		h.githubFlows.Lock()
		if err := h.store.DisconnectGitHub(ctx, session, input.Project); err != nil {
			h.githubFlows.Unlock()
			h.githubError(w, err)
			return
		}
		for key, flow := range h.githubFlows.starts {
			if flow.project == input.Project {
				delete(h.githubFlows.starts, key)
			}
		}
		for key, flow := range h.githubFlows.selections {
			if flow.project == input.Project {
				delete(h.githubFlows.selections, key)
			}
		}
		h.githubFlows.Unlock()
		httpJSON(w, map[string]bool{"ok": true})
	default:
		httpError(w, 404, "Not found")
	}
}
