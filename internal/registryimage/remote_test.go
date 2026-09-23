package registryimage

import (
	"context"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) *http.Response

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r), nil }
func response(status int, body string, headers map[string]string) *http.Response {
	h := make(http.Header)
	for k, v := range headers {
		h.Set(k, v)
	}
	return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(body))}
}

func TestPublicIPRejectsPrivateLinkLocalCGNATAndMappedAddresses(t *testing.T) {
	for _, raw := range []string{"10.0.0.1", "127.0.0.1", "169.254.1.1", "100.64.0.1", "192.0.2.1", "198.18.0.1", "203.0.113.1", "::ffff:127.0.0.1", "fc00::1", "fe80::1", "2001:db8::1", "168.63.129.16", "2002:7f00:1::", "3fff::1"} {
		ip := netip.MustParseAddr(raw)
		if publicIP(ip) {
			t.Errorf("publicIP(%s) accepted denied address", raw)
		}
	}
	for _, raw := range []string{"8.8.8.8", "::ffff:8.8.8.8", "2001:4860:4860::8888"} {
		if !publicIP(netip.MustParseAddr(raw)) {
			t.Errorf("publicIP(%s) rejected public address", raw)
		}
	}
}

func TestRemoteAuthorizeUsesFixedHostAndPullScope(t *testing.T) {
	r, _ := Parse("ghcr.io/acme/demo:tag")
	remote := newRemote(r)
	defer remote.client.CloseIdleConnections()
	var got *http.Request
	remote.client.Transport = roundTripFunc(func(req *http.Request) *http.Response { got = req; return response(200, `{"token":"secret"}`, nil) })
	if err := remote.authorize(context.Background(), Credentials{Username: "u", Password: "p"}); err != nil {
		t.Fatal(err)
	}
	if got.URL.Host != "ghcr.io" || got.URL.Path != "/token" {
		t.Fatalf("token endpoint escaped fixed host: %s", got.URL)
	}
	if got.URL.Query().Get("service") != "ghcr.io" || got.URL.Query().Get("scope") != "repository:acme/demo:pull" {
		t.Fatalf("unexpected token query: %s", got.URL.RawQuery)
	}
	if u, p, ok := got.BasicAuth(); !ok || u != "u" || p != "p" {
		t.Fatal("basic auth missing")
	}

	r, _ = Parse("ubuntu:tag")
	remote = newRemote(r)
	remote.client.Transport = roundTripFunc(func(req *http.Request) *http.Response { got = req; return response(200, `{"access_token":"x"}`, nil) })
	if err := remote.authorize(context.Background(), Credentials{}); err != nil {
		t.Fatal(err)
	}
	if got.URL.Host != "auth.docker.io" || got.URL.Query().Get("service") != "registry.docker.io" {
		t.Fatalf("unexpected Docker auth endpoint: %s", got.URL)
	}
}

func TestRemoteManifestAndConfigRedirectRules(t *testing.T) {
	r, _ := Parse("ghcr.io/acme/demo:tag")
	remote := newRemote(r)
	defer remote.client.CloseIdleConnections()
	remote.token = "bearer"
	var reqs []*http.Request
	remote.client.Transport = roundTripFunc(func(req *http.Request) *http.Response {
		reqs = append(reqs, req)
		if req.URL.Path == "/v2/acme/demo/manifests/tag" {
			return response(200, "manifest", nil)
		}
		return response(200, "config", nil)
	})
	if _, err := remote.Manifest(context.Background(), "tag"); err != nil {
		t.Fatal(err)
	}
	if reqs[0].Header.Get("Authorization") != "Bearer bearer" || reqs[0].URL.Host != "ghcr.io" {
		t.Fatal("manifest request auth/host mismatch")
	}

	remote.client.Transport = roundTripFunc(func(req *http.Request) *http.Response {
		return response(302, "", map[string]string{"Location": "https://cdn.example/blob"})
	})
	if _, err := remote.Manifest(context.Background(), "tag"); err == nil {
		t.Fatal("manifest redirect accepted")
	}

	reqs = nil
	remote.client.Transport = roundTripFunc(func(req *http.Request) *http.Response {
		reqs = append(reqs, req)
		if len(reqs) == 1 {
			return response(302, "", map[string]string{"Location": "https://cdn.example/blob"})
		}
		return response(200, "config", nil)
	})
	if _, err := remote.Config(context.Background(), "sha256:"+strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 2 || reqs[0].Header.Get("Authorization") != "Bearer bearer" || reqs[1].Header.Get("Authorization") != "" || reqs[1].URL.Host != "cdn.example" {
		t.Fatalf("redirect auth leakage: %#v", reqs)
	}
}

func TestRemoteRejectsTokenRedirectAndInvalidCredentials(t *testing.T) {
	r, _ := Parse("ghcr.io/acme/demo:tag")
	remote := newRemote(r)
	remote.client.Transport = roundTripFunc(func(req *http.Request) *http.Response {
		return response(302, "", map[string]string{"Location": "https://evil.example/token"})
	})
	if err := remote.authorize(context.Background(), Credentials{}); err == nil {
		t.Fatal("token redirect accepted")
	}
	for _, c := range []Credentials{{Username: "u"}, {Password: "p"}, {Username: "u:x", Password: "p"}, {Username: "u", Password: "p\nq"}} {
		if err := remote.authorize(context.Background(), c); err != ErrCredentials {
			t.Fatalf("credentials %#v: got %v", c, err)
		}
	}
}
