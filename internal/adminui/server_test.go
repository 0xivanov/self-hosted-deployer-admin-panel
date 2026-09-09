package adminui

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	cli "github.com/0xivanov/self-hosted-deployer-admin-panel/internal/client"
)

func panel(t *testing.T, writes bool) (*Server, *Demo) {
	t.Helper()
	d := NewDemo()
	s, e := New(d, Options{Host: "127.0.0.1:8787", Identity: "local-demo", AllowWrites: writes, Demo: true})
	if e != nil {
		t.Fatal(e)
	}
	return s, d
}
func request(s *Server, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1:8787"+path, strings.NewReader(body))
	r.Header.Set("X-Deployer-UI", s.token)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func TestBrowserBoundary(t *testing.T) {
	s, d := panel(t, true)
	for _, tc := range []struct{ name, host, origin, token, site string }{
		{name: "missing session", host: s.options.Host},
		{name: "foreign origin", host: s.options.Host, origin: "https://evil.test", token: s.token},
		{name: "DNS rebinding", host: "evil.test:8787", token: s.token},
		{name: "cross site navigation", host: s.options.Host, token: s.token, site: "cross-site"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "http://"+tc.host+"/api/deploy", strings.NewReader(`{"yaml":"name: unauthorized\nimage: nginx"}`))
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("X-Deployer-UI", tc.token)
			r.Header.Set("Sec-Fetch-Site", tc.site)
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != 403 {
				t.Fatalf("status %d", w.Code)
			}
		})
	}
	apps, _ := d.ListApps(context.Background())
	if len(apps) != 1 {
		t.Fatal("untrusted request deployed app")
	}
}
func TestReadOnlyAndIdentityMismatch(t *testing.T) {
	s, d := panel(t, false)
	if w := request(s, "POST", "/api/deploy", `{"yaml":"name: other\nimage: nginx"}`); w.Code != 403 {
		t.Fatal(w.Code)
	}
	s.options.AllowWrites = true
	s.options.Identity = "another-server"
	if w := request(s, "POST", "/api/deploy", `{"yaml":"name: other\nimage: nginx"}`); w.Code != 409 {
		t.Fatal(w.Code)
	}
	apps, _ := d.ListApps(context.Background())
	if len(apps) != 1 {
		t.Fatal("mutation escaped guard")
	}
}
func TestUpdateRollbackAndExternalChangeConflict(t *testing.T) {
	s, d := panel(t, true)
	original, _ := d.InspectApp(context.Background(), "hello-world")
	data := `{"yaml":"name: hello-world\nimage: nginx:alpine\nservice:\n  port: 80\ndeploy:\n  replicas: 1\n"}`
	if w := request(s, "POST", "/api/deploy", data); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w := request(s, "GET", "/api/apps/hello-world", "")
	var detail struct {
		Rollback struct {
			ID string `json:"deployment_id"`
		} `json:"rollback"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Rollback.ID == "" {
		t.Fatal("no rollback snapshot")
	}
	rollback := `{"deployment_id":"` + detail.Rollback.ID + `"}`
	if w = request(s, "POST", "/api/apps/hello-world/rollback", rollback); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	restored, _ := d.InspectApp(context.Background(), "hello-world")
	if fingerprint(restored.App.DesiredState) != fingerprint(original.App.DesiredState) {
		t.Fatal("restore changed configuration")
	}
	request(s, "POST", "/api/deploy", data)
	id := s.snapshots["hello-world"].ID
	_, err := d.DeployApp(context.Background(), "name: hello-world\nimage: nginx:other\n")
	if err != nil {
		t.Fatal(err)
	}
	if w = request(s, "POST", "/api/apps/hello-world/rollback", `{"deployment_id":"`+id+`"}`); w.Code != 409 {
		t.Fatal(w.Body.String())
	}
}
func TestSnapshotNotChangedAfterBackendFailure(t *testing.T) {
	s, d := panel(t, true)
	s.backend = failingBackend{d}
	w := request(s, "POST", "/api/deploy", `{"yaml":"name: hello-world\nimage: nginx:new\n"}`)
	if w.Code != 502 || len(s.snapshots) != 0 {
		t.Fatal("failed deployment created rollback state")
	}
}

type failingBackend struct{ *Demo }

func (f failingBackend) DeployApp(context.Context, string) (cli.DeployResult, error) {
	return cli.DeployResult{}, context.DeadlineExceeded
}
func TestStaticAndJSONValidation(t *testing.T) {
	s, _ := panel(t, true)
	for _, path := range []string{"/", "/app.js", "/styles.css", "/api/overview", "/api/apps/hello-world", "/api/apps/hello-world/logs"} {
		w := request(s, "GET", path, "")
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("cache enabled")
		}
	}
	for _, body := range []string{`{"yaml":"x","unknown":true}`, `{} {}`, strings.Repeat("x", (1<<20)+1)} {
		if w := request(s, "POST", "/api/deploy", body); w.Code != 400 {
			t.Fatal(w.Code)
		}
	}
	if w := request(s, "GET", "/api/apps/--token", ""); w.Code != 400 {
		t.Fatal("CLI flag accepted as app name")
	}
}

func TestRemoteAuthentication(t *testing.T) {
	opts := Options{Host: "192.0.2.1:8787", PublicURL: "https://192.0.2.1:8787", Identity: "local-demo", Username: "admin", Password: strings.Repeat("x", 32)}
	s, err := New(NewDemo(), opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path, origin, password string
		secure                       bool
		want                         int
	}{
		{"anonymous index", "/", "", "", true, 401},
		{"anonymous API", "/api/overview", "", "", true, 401},
		{"wrong password", "/", "", "wrong", true, 401},
		{"plaintext", "/", "", opts.Password, false, 403},
		{"authenticated index", "/", "", opts.Password, true, 200},
		{"authenticated API", "/api/overview", opts.PublicURL, opts.Password, true, 200},
		{"foreign origin", "/api/overview", "https://evil.test", opts.Password, true, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", opts.PublicURL+tc.path, nil)
			r.TLS = nil
			if tc.secure {
				r.TLS = &tls.ConnectionState{}
			}
			if tc.password != "" {
				r.SetBasicAuth(opts.Username, tc.password)
			}
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("X-Deployer-UI", s.token)
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("got %d want %d", w.Code, tc.want)
			}
			if tc.want == 401 && strings.Contains(w.Body.String(), s.token) {
				t.Fatal("session leaked")
			}
		})
	}
	for _, origin := range []string{"http://192.0.2.1:8787", "https://other.test:8787", opts.PublicURL + "/path", opts.PublicURL + "?"} {
		invalid := opts
		invalid.PublicURL = origin
		if _, err := New(NewDemo(), invalid); err == nil {
			t.Fatalf("accepted %s", origin)
		}
	}
	opts.Password = "weak"
	if _, err := New(NewDemo(), opts); err == nil {
		t.Fatal("accepted weak credential")
	}
}

func TestRemoteDefaultHTTPSPort(t *testing.T) {
	opts := Options{Host: "admin.example.com", PublicURL: "https://admin.example.com", Identity: "local-demo", Username: "admin", Password: strings.Repeat("x", 32)}
	s, err := New(NewDemo(), opts)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", opts.PublicURL+"/api/overview", nil)
	r.SetBasicAuth(opts.Username, opts.Password)
	r.Header.Set("Origin", opts.PublicURL)
	r.Header.Set("X-Deployer-UI", s.token)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}
