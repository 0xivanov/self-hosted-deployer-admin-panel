package githubdeploy

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	maxOAuthSecretBytes = 8192
	maxOAuthCodeBytes   = 2048
	maxOAuthResponse    = 1 << 20
	maxOAuthExpiry      = 365 * 24 * 60 * 60
)

var ErrGitHubOAuth = errors.New("GitHub OAuth is unavailable")

// OAuth holds the server-side values needed for GitHub's authorization-code
// flow. It does not retain the App private key.
type OAuth struct {
	clientID     string
	clientSecret string
	callback     string
}

// OAuthAuthorization contains the one-time values the caller must bind to a
// portal session before redirecting a user. Keep it server-side or in a
// secure, short-lived session; state and the verifier are credentials for the
// in-progress flow.
type OAuthAuthorization struct {
	URL          string `json:"-"`
	State        string `json:"-"`
	CodeVerifier string `json:"-"`
}

func (a OAuthAuthorization) String() string   { return "[GitHub OAuth authorization redacted]" }
func (a OAuthAuthorization) GoString() string { return a.String() }

// OAuthToken contains user credentials returned by GitHub. AccessToken and
// RefreshToken are intentionally excluded from JSON and generic formatting.
type OAuthToken struct {
	AccessToken      string    `json:"-"`
	RefreshToken     string    `json:"-"`
	Scope            string    `json:"scope,omitempty"`
	ExpiresAt        time.Time `json:"expires_at,omitempty"`
	RefreshExpiresAt time.Time `json:"refresh_expires_at,omitempty"`
}

func (o OAuth) String() string      { return "[GitHub OAuth credentials redacted]" }
func (o OAuth) GoString() string    { return o.String() }
func (OAuthToken) String() string   { return "[GitHub OAuth token redacted]" }
func (OAuthToken) GoString() string { return "[GitHub OAuth token redacted]" }

var githubOAuthClient = &http.Client{
	Timeout:       30 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	Transport: &http.Transport{
		Proxy:                  nil,
		DialContext:            (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		TLSHandshakeTimeout:    5 * time.Second,
		ResponseHeaderTimeout:  10 * time.Second,
		DisableCompression:     true,
		MaxResponseHeaderBytes: 64 << 10,
	},
}

func NewOAuth(app *App, clientSecret, callback string) (*OAuth, error) {
	if app == nil || !validAppClientID(app.clientID) || !validOAuthSecret(clientSecret) || !validOAuthCallback(callback) {
		return nil, ErrGitHubOAuth
	}
	return &OAuth{clientID: app.clientID, clientSecret: clientSecret, callback: callback}, nil
}

// AuthorizationURL validates caller-provided state and verifier and builds a
// fixed GitHub authorization URL using PKCE S256.
func (o *OAuth) AuthorizationURL(state, verifier string) (string, error) {
	if o == nil || !validAppClientID(o.clientID) || !validOAuthSecret(o.clientSecret) || !validOAuthCallback(o.callback) || !validOAuthState(state) || !validOAuthVerifier(verifier) {
		return "", ErrGitHubOAuth
	}
	digest := sha256.Sum256([]byte(verifier))
	query := url.Values{}
	query.Set("client_id", o.clientID)
	query.Set("redirect_uri", o.callback)
	query.Set("state", state)
	query.Set("code_challenge", base64.RawURLEncoding.EncodeToString(digest[:]))
	query.Set("code_challenge_method", "S256")
	return "https://github.com/login/oauth/authorize?" + query.Encode(), nil
}

// StartAuthorization generates fresh state and PKCE verifier values and
// returns the URL plus the values that must be bound to the caller's session.
func (o *OAuth) StartAuthorization() (OAuthAuthorization, error) {
	state, err := randomOAuthValue(32)
	if err != nil {
		return OAuthAuthorization{}, ErrGitHubOAuth
	}
	verifier, err := randomOAuthValue(32)
	if err != nil {
		return OAuthAuthorization{}, ErrGitHubOAuth
	}
	authorizationURL, err := o.AuthorizationURL(state, verifier)
	if err != nil {
		return OAuthAuthorization{}, err
	}
	return OAuthAuthorization{URL: authorizationURL, State: state, CodeVerifier: verifier}, nil
}

// Exchange exchanges a single-use authorization code for a user token. The
// caller must verify the callback state before invoking this method.
func (o *OAuth) Exchange(ctx context.Context, code, verifier string) (OAuthToken, error) {
	if o == nil || !validOAuthCode(code) || !validOAuthVerifier(verifier) {
		return OAuthToken{}, ErrGitHubOAuth
	}
	checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return exchangeOAuthCode(checkCtx, githubOAuthClient, o.clientID, o.clientSecret, o.callback, code, verifier)
}

func exchangeOAuthCode(ctx context.Context, client *http.Client, clientID, clientSecret, callback, code, verifier string) (OAuthToken, error) {
	if client == nil || !validAppClientID(clientID) || !validOAuthSecret(clientSecret) || !validOAuthCallback(callback) || !validOAuthCode(code) || !validOAuthVerifier(verifier) {
		return OAuthToken{}, ErrGitHubOAuth
	}
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	form.Set("code", code)
	form.Set("redirect_uri", callback)
	form.Set("code_verifier", verifier)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://github.com/login/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return OAuthToken{}, ErrGitHubOAuth
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Launchstead")
	bounded := *client
	bounded.Timeout = 30 * time.Second
	bounded.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := bounded.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return OAuthToken{}, ctx.Err()
		}
		return OAuthToken{}, ErrGitHubOAuth
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return OAuthToken{}, ErrGitHubOAuth
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxOAuthResponse+1))
	if err != nil || len(raw) > maxOAuthResponse {
		return OAuthToken{}, ErrGitHubOAuth
	}
	var result struct {
		AccessToken      string `json:"access_token"`
		TokenType        string `json:"token_type"`
		Scope            string `json:"scope"`
		ExpiresIn        int64  `json:"expires_in"`
		RefreshToken     string `json:"refresh_token"`
		RefreshExpiresIn int64  `json:"refresh_token_expires_in"`
		Error            string `json:"error"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&result); err != nil {
		return OAuthToken{}, ErrGitHubOAuth
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return OAuthToken{}, ErrGitHubOAuth
	}
	if result.Error != "" || !validOAuthSecret(result.AccessToken) || !strings.EqualFold(result.TokenType, "bearer") || result.ExpiresIn < 0 || result.ExpiresIn > maxOAuthExpiry || result.RefreshExpiresIn < 0 || result.RefreshExpiresIn > maxOAuthExpiry || (result.RefreshExpiresIn > 0 && result.RefreshToken == "") {
		return OAuthToken{}, ErrGitHubOAuth
	}
	now := time.Now()
	token := OAuthToken{AccessToken: result.AccessToken, RefreshToken: result.RefreshToken, Scope: result.Scope}
	if result.ExpiresIn > 0 {
		token.ExpiresAt = now.Add(time.Duration(result.ExpiresIn) * time.Second)
	}
	if result.RefreshExpiresIn > 0 {
		token.RefreshExpiresAt = now.Add(time.Duration(result.RefreshExpiresIn) * time.Second)
	}
	if result.RefreshToken != "" && !validOAuthSecret(result.RefreshToken) {
		return OAuthToken{}, ErrGitHubOAuth
	}
	return token, nil
}

func validOAuthCallback(callback string) bool {
	parsed, err := url.Parse(callback)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil && parsed.Fragment == "" && parsed.RawQuery == "" && parsed.Opaque == "" && validPrintable(callback, 2048)
}

func validOAuthSecret(value string) bool { return validPrintable(value, maxOAuthSecretBytes) }
func validOAuthCode(value string) bool   { return validPrintable(value, maxOAuthCodeBytes) }
func validOAuthState(value string) bool {
	return len(value) >= 32 && len(value) <= 256 && validOAuthUnreserved(value)
}

func validOAuthVerifier(value string) bool {
	return len(value) >= 43 && len(value) <= 128 && validOAuthUnreserved(value)
}

func validOAuthUnreserved(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '.' || r == '_' || r == '~') {
			return false
		}
	}
	return true
}

func validPrintable(value string, maxBytes int) bool {
	if value == "" || len(value) > maxBytes {
		return false
	}
	for _, r := range value {
		if r < 0x21 || r > 0x7e {
			return false
		}
	}
	return true
}

func randomOAuthValue(size int) (string, error) {
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
