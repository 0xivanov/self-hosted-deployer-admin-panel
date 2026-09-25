package githubdeploy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type tokenTransport func(*http.Request) (*http.Response, error)

func (f tokenTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func tokenResponse(now time.Time) string {
	return fmt.Sprintf(`{"token":"private-fixture-token","expires_at":%q,"permissions":{"contents":"read","metadata":"read"},"repository_selection":"selected","repositories":[{"id":42}]}`, now.Add(time.Hour).Format(time.RFC3339))
}
func TestRepositoryTokenRequestsAndValidatesNarrowAccess(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	client := &http.Client{Transport: tokenTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != "POST" || r.URL.String() != "https://api.github.com/app/installations/7/access_tokens" || r.Header.Get("Authorization") != "Bearer fixture-jwt" {
			t.Fatal("unexpected token destination or authentication")
		}
		var body struct {
			RepositoryIDs []int64           `json:"repository_ids"`
			Permissions   map[string]string `json:"permissions"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || len(body.RepositoryIDs) != 1 || body.RepositoryIDs[0] != 42 || len(body.Permissions) != 1 || body.Permissions["contents"] != "read" {
			t.Fatal("token request widened repository/permission scope")
		}
		return &http.Response{StatusCode: 201, Body: io.NopCloser(strings.NewReader(tokenResponse(now))), Header: make(http.Header)}, nil
	})}
	token, err := requestRepositoryToken(context.Background(), client, now, "fixture-jwt", 7, 42)
	if err != nil || token.Value != "private-fixture-token" || token.RepositoryID != 42 {
		t.Fatal("scoped token unavailable", err)
	}
	data, _ := json.Marshal(token)
	for _, s := range []string{string(data), fmt.Sprintf("%v", token), fmt.Sprintf("%+v", token), fmt.Sprintf("%#v", token)} {
		if strings.Contains(s, token.Value) {
			t.Fatal("token exposed through metadata/formatting")
		}
	}
}
func TestRepositoryTokenRejectsWidenedOrInvalidResponse(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	good := tokenResponse(now)
	for name, body := range map[string]string{
		"other repo":         strings.Replace(good, `"id":42`, `"id":43`, 1),
		"all repos":          strings.Replace(good, `"selected"`, `"all"`, 1),
		"write":              strings.Replace(good, `"contents":"read"`, `"contents":"write"`, 1),
		"extra permission":   strings.Replace(good, `"metadata":"read"`, `"issues":"read"`, 1),
		"no repo proof":      strings.Replace(good, `[{"id":42}]`, `[]`, 1),
		"expired":            strings.Replace(good, now.Add(time.Hour).Format(time.RFC3339), now.Add(-time.Minute).Format(time.RFC3339), 1),
		"unbounded lifetime": strings.Replace(good, now.Add(time.Hour).Format(time.RFC3339), now.Add(24*time.Hour).Format(time.RFC3339), 1),
		"trailing":           good + `{}`,
		"oversize":           strings.Repeat("x", (1<<20)+1),
	} {
		t.Run(name, func(t *testing.T) {
			client := &http.Client{Transport: tokenTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 201, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			})}
			token, err := requestRepositoryToken(context.Background(), client, now, "fixture-jwt", 7, 42)
			if err != ErrGitHubAccess || token.Value != "" {
				t.Fatal("invalid provider response accepted")
			}
		})
	}
}
func TestRepositoryTokenNeverFollowsRedirectOrEchoesResponse(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: tokenTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls > 1 {
			t.Fatal("followed token redirect")
		}
		return &http.Response{StatusCode: 307, Header: http.Header{"Location": []string{"https://example.com/secret"}}, Body: io.NopCloser(strings.NewReader("private-provider-detail"))}, nil
	})}
	_, err := requestRepositoryToken(context.Background(), client, time.Now(), "fixture-jwt", 7, 42)
	if err != ErrGitHubAccess || calls != 1 || strings.Contains(err.Error(), "private-provider-detail") {
		t.Fatal("redirect/error boundary failed")
	}
}
