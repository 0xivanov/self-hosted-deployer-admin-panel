package portal

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPAccountLifecycleWithQueuedMail(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)
	m := testMailer(t, s)
	h, err := NewHTTP(s, HTTPOptions{Origin: m.origin, Mail: m, Signup: true})
	if err != nil {
		t.Fatal(err)
	}
	request := func(path, body string) *httptest.ResponseRecorder {
		return portalRequest(h, "POST", path, body, h.origin, "", nil)
	}
	signup := `{"email":"flow@example.test","password":"` + testPassword + `","workspace":"Workspace"}`
	w := request("/api/register", signup)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	first := w.Body.String()
	w = request("/api/register", signup)
	if w.Code != 200 || w.Body.String() != first {
		t.Fatal("account enumeration response", w.Code, w.Body.String())
	}
	var token string
	_, err = m.DeliverOne(context.Background(), senderFunc(func(_ context.Context, msg Mail) error {
		token = strings.Split(strings.Split(msg.Text, "/#verify=")[1], "\n")[0]
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if token == "" || strings.Contains(first, token) {
		t.Fatal("token exposed or not queued")
	}
	w = request("/api/verify", `{"token":"`+token+`"}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	cookie, _ := httpLogin(t, h, "flow@example.test")
	w = request("/api/password/forgot", `{"email":"flow@example.test"}`)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	existing := w.Body.String()
	w = request("/api/password/forgot", `{"email":"missing@example.test"}`)
	if w.Body.String() != existing {
		t.Fatal("reset enumeration")
	}
	_, err = m.DeliverOne(context.Background(), senderFunc(func(_ context.Context, msg Mail) error {
		token = strings.Split(strings.Split(msg.Text, "/#reset=")[1], "\n")[0]
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"token": token, "password": "changed synthetic password"})
	w = request("/api/password/reset", string(body))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = portalRequest(h, "GET", "/api/session", "", "", "", cookie)
	if w.Code != 401 {
		t.Fatal("reset did not revoke cookie", w.Code)
	}
}
func TestEmailLinkNavigationIsAllowedWithoutMutation(t *testing.T) {
	s, _ := newStore(t)
	h, _ := NewHTTP(s, HTTPOptions{Origin: "https://portal.example.test"})
	r := httptest.NewRequest("GET", h.origin+"/", nil)
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal("email link landing blocked", w.Code)
	}
}
