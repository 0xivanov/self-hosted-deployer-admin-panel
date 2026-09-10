package portal

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func portalRequest(h *HTTP, method, path, body, origin, csrf string, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "https://portal.example.test"+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", origin)
	r.Header.Set("X-CSRF-Token", csrf)
	r.RemoteAddr = "192.0.2.5:12345"
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func httpLogin(t *testing.T, h *HTTP, email string) (*http.Cookie, string) {
	t.Helper()
	w := portalRequest(h, "POST", "/api/login", `{"email":"`+email+`","password":"`+testPassword+`"}`, h.origin, "", nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var body struct {
		CSRF string `json:"csrf"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal(cookies)
	}
	return cookies[0], body.CSRF
}
func TestHTTPAuthenticationAndTenantBoundary(t *testing.T) {
	t.Parallel()
	store, _ := newStore(t)
	alice, _ := verifiedAccount(t, store, "alice@example.test")
	bob, _ := verifiedAccount(t, store, "bob@example.test")
	h, err := NewHTTP(store, HTTPOptions{Origin: "https://portal.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf := httpLogin(t, h, alice.Email)
	if !cookie.Secure || !cookie.HttpOnly || cookie.Path != "/" || cookie.Domain != "" || cookie.SameSite != http.SameSiteStrictMode || cookie.Name != "__Host-portal-session" {
		t.Fatal("unsafe cookie", cookie)
	}
	if strings.Contains(csrf, cookie.Value) || csrf == "" {
		t.Fatal("invalid csrf")
	}
	for _, tc := range []struct {
		name, origin, csrf, workspace string
		code                          int
	}{
		{"own workspace", h.origin, csrf, alice.WorkspaceID, 200},
		{"other workspace", h.origin, csrf, bob.WorkspaceID, 403},
		{"missing csrf", h.origin, "", alice.WorkspaceID, 403},
		{"missing origin", "", csrf, alice.WorkspaceID, 403},
		{"foreign origin", "https://evil.test", csrf, alice.WorkspaceID, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := portalRequest(h, "POST", "/api/projects", `{"workspace":"`+tc.workspace+`","name":"project","kind":"static"}`, tc.origin, tc.csrf, cookie)
			if w.Code != tc.code {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
	w := portalRequest(h, "GET", "/api/projects?workspace="+bob.WorkspaceID, "", "", "", cookie)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	w = portalRequest(h, "GET", "/api/session", "", "", "", cookie)
	if w.Code != 200 || strings.Contains(w.Body.String(), bob.WorkspaceID) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = portalRequest(h, "POST", "/api/logout", `{}`, h.origin, csrf, cookie)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	w = portalRequest(h, "GET", "/api/session", "", "", "", cookie)
	if w.Code != 401 {
		t.Fatal("session survives logout", w.Code)
	}
}
func TestHTTPRejectsRebindingAndPlaintext(t *testing.T) {
	t.Parallel()
	store, _ := newStore(t)
	h, err := NewHTTP(store, HTTPOptions{Origin: "https://portal.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*http.Request)
	}{
		{"host", func(r *http.Request) { r.Host = "evil.test" }},
		{"plaintext", func(r *http.Request) { r.TLS = nil }},
		{"cross-site", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", h.origin+"/", nil)
			tc.mutate(r)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 403 {
				t.Fatal(w.Code)
			}
		})
	}
}
func TestHTTPBoundsAndClosedRegistration(t *testing.T) {
	t.Parallel()
	store, _ := newStore(t)
	h, _ := NewHTTP(store, HTTPOptions{Origin: "https://portal.example.test"})
	for _, tc := range []struct{ name, body string }{
		{"extra fields", `{"email":"a@example.test","password":"long password","admin":true}`},
		{"oversize", `{"email":"` + strings.Repeat("a", 17000) + `"}`},
		{"multiple objects", `{} {}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := portalRequest(h, "POST", "/api/login", tc.body, h.origin, "", nil)
			if w.Code != 400 {
				t.Fatal(w.Code)
			}
		})
	}
	w := portalRequest(h, "POST", "/api/register", `{}`, h.origin, "", nil)
	if w.Code != 401 {
		t.Fatal("registration exposed", w.Code)
	}
	for range 10 {
		w = portalRequest(h, "POST", "/api/login", `{}`, h.origin, "", nil)
	}
	if w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatal("rate limit absent", w.Code)
	}
}
