package portal

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"strings"
	"testing"
)

const testGitHubWebhookSecret = "synthetic-github-webhook-secret-000001"

func githubWebhookSignature(body []byte) string {
	m := hmac.New(sha256.New, []byte(testGitHubWebhookSecret))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}
func TestGitHubWebhookMountAndDurableReceipt(t *testing.T) {
	s, _, session, _, _, project := projectClientFixture(t)
	if _, err := s.SaveGitHubConnection(t.Context(), session.Token, githubConnectionFixture(t, s, session.Token, project.ID), githubAccessFixture(), "main", "", true); err != nil {
		t.Fatal(err)
	}
	provider := &githubHTTPProvider{}
	h, err := NewHTTP(s, HTTPOptions{Origin: "https://portal.example.test", GitHubApp: provider, GitHubOAuth: provider, GitHubWebhookSecret: testGitHubWebhookSecret})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"ref":"refs/heads/main","before":"` + strings.Repeat("a", 40) + `","after":"` + strings.Repeat("b", 40) + `","repository":{"id":30,"full_name":"developer/website","owner":{"login":"developer"}},"installation":{"id":20}}`)
	for _, tc := range []struct {
		name, url, method, origin, contentType, event, delivery string
		signed                                                  bool
		want                                                    int
	}{
		{"unsigned", h.origin + "/webhooks/github", "POST", "", "application/json", "push", "1", false, 403},
		{"browser origin", h.origin + "/webhooks/github", "POST", h.origin, "application/json", "push", "1", true, 403},
		{"wrong host", "https://foreign.test/webhooks/github", "POST", "", "application/json", "push", "1", true, 403},
		{"plaintext", "http://portal.example.test/webhooks/github", "POST", "", "application/json", "push", "1", true, 403},
		{"wrong method", h.origin + "/webhooks/github", "GET", "", "application/json", "push", "1", true, 405},
		{"query alias", h.origin + "/webhooks/github?x=1", "POST", "", "application/json", "push", "1", true, 404},
		{"encoded alias", h.origin + "/webhooks/%67ithub", "POST", "", "application/json", "push", "1", true, 404},
		{"wrong content type", h.origin + "/webhooks/github", "POST", "", "text/plain", "push", "1", true, 415},
		{"unsigned event header cannot suppress push", h.origin + "/webhooks/github", "POST", "", "application/json", "ping", "1", true, 204},
		{"changed delivery header cannot duplicate", h.origin + "/webhooks/github", "POST", "", "application/json", "push", "different", true, 204},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.url, bytes.NewReader(body))
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("Content-Type", tc.contentType)
			r.Header.Set("X-GitHub-Event", tc.event)
			r.Header.Set("X-GitHub-Delivery", tc.delivery)
			if tc.signed {
				r.Header.Set("X-Hub-Signature-256", githubWebhookSignature(body))
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
	var count int
	if err = s.db.QueryRow("SELECT count(*) FROM github_push_events WHERE project_id=?", project.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("durable receipt", count, err)
	}
	if err = s.db.QueryRow("SELECT count(*) FROM github_imports").Scan(&count); err != nil || count != 0 {
		t.Fatal("intake unexpectedly started import", count, err)
	}
	disabled, err := NewHTTP(s, HTTPOptions{Origin: h.origin})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	disabled.ServeHTTP(w, httptest.NewRequest("POST", h.origin+"/webhooks/github", bytes.NewReader(body)))
	if w.Code != 404 {
		t.Fatal("unconfigured endpoint enabled", w.Code)
	}
}
func TestGitHubWebhookPingAndUnsupportedEventsAreAuthenticated(t *testing.T) {
	s, _ := newStore(t)
	h, err := GitHubWebhookHandler(s, "portal.example.test", testGitHubWebhookSecret)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{"zen":"Keep it simple","hook_id":1,"hook":{},"app":{}}`, `{"action":"deleted","installation":{"id":20}}`} {
		r := httptest.NewRequest("POST", "https://portal.example.test/webhooks/github", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Hub-Signature-256", githubWebhookSignature([]byte(body)))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 204 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	var count int
	if err = s.db.QueryRow("SELECT count(*) FROM github_push_events").Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}
