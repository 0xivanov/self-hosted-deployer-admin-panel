package githubdeploy

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestOAuthStartAuthorizationUsesPKCEAndFixedCallback(t *testing.T) {
	oauth, err := NewOAuth(&App{clientID: "client-1"}, "client-secret", "https://portal.example.test/github/callback")
	if err != nil {
		t.Fatal(err)
	}
	start, err := oauth.StartAuthorization()
	if err != nil {
		t.Fatalf("StartAuthorization: %v", err)
	}
	parsed, err := url.Parse(start.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "github.com" || parsed.Path != "/login/oauth/authorize" {
		t.Fatalf("authorization URL = %s", start.URL)
	}
	query := parsed.Query()
	if query.Get("client_id") != "client-1" || query.Get("redirect_uri") != "https://portal.example.test/github/callback" || query.Get("state") != start.State || query.Get("code_challenge_method") != "S256" {
		t.Fatalf("authorization query = %v", query)
	}
	digest := sha256.Sum256([]byte(start.CodeVerifier))
	if query.Get("code_challenge") != base64.RawURLEncoding.EncodeToString(digest[:]) {
		t.Fatal("authorization challenge does not match verifier")
	}
	if len(start.State) < 32 || len(start.CodeVerifier) < 32 {
		t.Fatal("authorization secrets are too short")
	}
	if strings.Contains(start.String(), start.State) || strings.Contains(start.String(), start.CodeVerifier) {
		t.Fatal("authorization values leaked through formatting")
	}
	data, _ := json.Marshal(start)
	if strings.Contains(string(data), start.State) || strings.Contains(string(data), start.CodeVerifier) {
		t.Fatal("authorization values leaked through JSON")
	}
	if strings.Contains(oauth.String(), "client-1") || strings.Contains(oauth.String(), "client-secret") {
		t.Fatal("OAuth credentials leaked through formatting")
	}
}

func TestNewOAuthRejectsUnsafeCallbackOrSecret(t *testing.T) {
	for name, callback := range map[string]string{
		"http":     "http://portal.example.test/callback",
		"userinfo": "https://user:pass@portal.example.test/callback",
		"fragment": "https://portal.example.test/callback#state",
		"query":    "https://portal.example.test/callback?x=1",
		"relative": "/github/callback",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewOAuth(&App{clientID: "client-1"}, "secret", callback); !errors.Is(err, ErrGitHubOAuth) {
				t.Fatalf("error = %v, want ErrGitHubOAuth", err)
			}
		})
	}
	for _, secret := range []string{"", "has space", strings.Repeat("x", maxOAuthSecretBytes+1)} {
		if _, err := NewOAuth(&App{clientID: "client-1"}, secret, "https://portal.example.test/callback"); !errors.Is(err, ErrGitHubOAuth) {
			t.Fatalf("secret accepted: %v", err)
		}
	}
}

func TestExchangeOAuthCodeSendsPKCEAndRedactsToken(t *testing.T) {
	client := &http.Client{Transport: oauthTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.String() != "https://github.com/login/oauth/access_token" || r.Header.Get("Accept") != "application/json" {
			t.Fatal("unexpected OAuth exchange request")
		}
		if err := r.ParseForm(); err != nil || r.Form.Get("client_id") != "client-1" || r.Form.Get("client_secret") != "secret-value" || r.Form.Get("code") != "code-value" || r.Form.Get("redirect_uri") != "https://portal.example.test/callback" || r.Form.Get("code_verifier") != strings.Repeat("v", 43) {
			t.Fatal("OAuth exchange parameters were not bound correctly")
		}
		return jsonResponse(http.StatusOK, map[string]any{"access_token": "ghu_private-token", "token_type": "bearer", "scope": "read:user", "expires_in": 3600, "refresh_token": "refresh-private-token", "refresh_token_expires_in": 7200}), nil
	})}
	token, err := exchangeOAuthCode(context.Background(), client, "client-1", "secret-value", "https://portal.example.test/callback", "code-value", strings.Repeat("v", 43))
	if err != nil || token.AccessToken != "ghu_private-token" || token.RefreshToken != "refresh-private-token" || token.Scope != "read:user" || token.ExpiresAt.IsZero() || token.RefreshExpiresAt.IsZero() {
		t.Fatalf("exchange result = %+v err=%v", token, err)
	}
	for _, formatted := range []string{token.String(), token.GoString()} {
		if strings.Contains(formatted, "private-token") {
			t.Fatal("token leaked through formatting")
		}
	}
	data, _ := json.Marshal(token)
	if strings.Contains(string(data), "private-token") {
		t.Fatal("token leaked through JSON")
	}
}

func TestExchangeOAuthCodeRejectsRedirectAndMalformedResponses(t *testing.T) {
	for name, response := range map[string]*http.Response{
		"redirect":                     {StatusCode: http.StatusFound, Header: http.Header{"Location": []string{"https://evil.example/steal"}}, Body: io.NopCloser(strings.NewReader("secret detail"))},
		"trailing":                     {StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"access_token":"token","token_type":"bearer"}{}`))},
		"wrong type":                   {StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"access_token":"token","token_type":"basic"}`))},
		"expiry overflow":              {StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"access_token":"token","token_type":"bearer","expires_in":31536001}`))},
		"refresh expiry without token": {StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"access_token":"token","token_type":"bearer","refresh_token_expires_in":60}`))},
		"oversize":                     {StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(strings.Repeat("x", maxOAuthResponse+1)))},
	} {
		t.Run(name, func(t *testing.T) {
			client := &http.Client{Transport: oauthTransport(func(*http.Request) (*http.Response, error) { return response, nil })}
			token, err := exchangeOAuthCode(context.Background(), client, "client-1", "secret-value", "https://portal.example.test/callback", "code-value", strings.Repeat("v", 43))
			if !errors.Is(err, ErrGitHubOAuth) || token.AccessToken != "" || strings.Contains(err.Error(), "secret detail") {
				t.Fatalf("response accepted or leaked: token=%+v err=%v", token, err)
			}
		})
	}
}

func TestOAuthRejectsInvalidInputs(t *testing.T) {
	oauth, err := NewOAuth(&App{clientID: "client-1"}, "secret", "https://portal.example.test/callback")
	if err != nil {
		t.Fatal(err)
	}
	for name, verifier := range map[string]string{
		"short":    strings.Repeat("v", 42),
		"space":    strings.Repeat("v", 42) + " ",
		"reserved": strings.Repeat("v", 42) + "+",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := oauth.AuthorizationURL(strings.Repeat("s", 32), verifier); !errors.Is(err, ErrGitHubOAuth) {
				t.Fatalf("invalid PKCE verifier accepted: %v", err)
			}
		})
	}
	if _, err := oauth.Exchange(context.Background(), "", strings.Repeat("v", 43)); !errors.Is(err, ErrGitHubOAuth) {
		t.Fatalf("empty code accepted: %v", err)
	}
}

type oauthTransport func(*http.Request) (*http.Response, error)

func (f oauthTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
