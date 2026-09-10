//go:build integration

package staticpublish

import (
	"archive/zip"
	"bytes"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/staticsite"
)

func TestAuthenticatedRuntimeTransport(t *testing.T) {
	t.Parallel()
	site, err := staticsite.Open(filepath.Join(t.TempDir(), "site"))
	if err != nil {
		t.Fatal(err)
	}
	defer site.Close()
	project, secret := strings.Repeat("a", 64), strings.Repeat("b", 64)
	server := httptest.NewUnstartedServer(nil)
	defer server.Close()
	handler, err := Handler(site, server.Listener.Addr().String(), project, secret)
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = handler
	server.StartTLS()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	client, err := NewClient(server.URL, project, secret, roots)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	f, err := z.Create("index.html")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write([]byte("Transport fixture")); err != nil {
		t.Fatal(err)
	}
	if err = z.Close(); err != nil {
		t.Fatal(err)
	}
	state, err := client.Publish(t.Context(), 2, b.Bytes())
	if err != nil || state.Revision != 2 || state.Release == "" {
		t.Fatal(state, err)
	}
	got, err := client.Observe(t.Context())
	if err != nil || got != state {
		t.Fatal(got, err)
	}
	if _, err = client.Publish(t.Context(), 1, b.Bytes()); err == nil {
		t.Fatal("stale publication accepted")
	}
	for _, tc := range []struct{ name, project, secret string }{
		{"wrong project", strings.Repeat("c", 64), secret}, {"wrong key", project, strings.Repeat("d", 64)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := NewClient(server.URL, tc.project, tc.secret, roots)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if _, err = c.Publish(t.Context(), 3, b.Bytes()); err == nil {
				t.Fatal("unauthorized publication")
			}
			if _, err = c.Observe(t.Context()); err == nil {
				t.Fatal("unauthorized observation")
			}
		})
	}
	untrusted, err := NewClient(server.URL, project, secret, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer untrusted.Close()
	if _, err = untrusted.Observe(t.Context()); err == nil {
		t.Fatal("untrusted TLS accepted")
	}
	u, _ := url.Parse(server.URL)
	for _, tc := range []struct {
		name, origin, host string
		plain              bool
	}{
		{"browser origin", "https://customer.test", u.Host, false}, {"foreign host", "", "another.test", false}, {"plaintext", "", u.Host, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scheme := "https"
			if tc.plain {
				scheme = "http"
			}
			r := httptest.NewRequest("GET", scheme+"://"+tc.host+"/status", nil)
			r.Header.Set("Authorization", "Bearer "+secret)
			r.Header.Set("X-Project-ID", project)
			r.Header.Set("Origin", tc.origin)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != 403 {
				t.Fatal(w.Code)
			}
		})
	}
	if site.Revision() != 2 {
		t.Fatal("denied request changed runtime")
	}
}
func TestClientRejectsRedirect(t *testing.T) {
	t.Parallel()
	reached := false
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirected" {
			reached = true
		}
		http.Redirect(w, r, "/redirected", 307)
	}))
	defer server.Close()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	c, err := NewClient(server.URL, strings.Repeat("a", 64), strings.Repeat("b", 64), roots)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err = c.Observe(t.Context()); err == nil || reached {
		t.Fatal("followed runtime redirect", err)
	}
}
