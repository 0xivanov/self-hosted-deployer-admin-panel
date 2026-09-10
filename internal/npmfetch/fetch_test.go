package npmfetch

import (
	"context"
	"crypto/sha512"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func integrity(data string) string {
	s := sha512.Sum512([]byte(data))
	return "sha512-" + base64.StdEncoding.EncodeToString(s[:])
}
func TestFetchVerifiedBytesAndNetworkPolicy(t *testing.T) {
	t.Parallel()
	c := NewClient()
	if c.http.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("ambient proxy enabled")
	}
	calls := 0
	c.http.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "registry.npmjs.org" || r.Method != "GET" || r.Header.Get("Authorization") != "" {
			t.Fatal("unexpected request")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("package")), ContentLength: 7, Request: r}, nil
	})
	tarball := Tarball{"https://registry.npmjs.org/example/-/example-1.0.0.tgz", integrity("package")}
	data, err := c.Fetch(t.Context(), tarball)
	if err != nil || string(data) != "package" {
		t.Fatal(string(data), err)
	}
	for _, url := range []string{"http://registry.npmjs.org/a/-/a.tgz", "https://127.0.0.1/a/-/a.tgz", "https://registry.npmjs.org.evil.test/a/-/a.tgz", "https://user:secret@registry.npmjs.org/a/-/a.tgz", "https://registry.npmjs.org:443/a/-/a.tgz", "https://registry.npmjs.org/a/-/a.tgz?token=secret", "https://registry.npmjs.org/a/../-/a.tgz"} {
		if _, err = c.Fetch(t.Context(), Tarball{url, tarball.Integrity}); !errors.Is(err, ErrDependency) {
			t.Fatal(url, err)
		}
	}
	if calls != 1 {
		t.Fatal("invalid requests contacted network", calls)
	}
	tarball.Integrity = integrity("tampered")
	if data, err = c.Fetch(t.Context(), tarball); !errors.Is(err, ErrFetch) || data != nil {
		t.Fatal("unverified bytes returned", err)
	}
}
func TestFetchRejectsRedirectErrorAndOversize(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		status   int
		size     int64
		encoding string
		body     string
		failure  bool
	}{
		{name: "redirect", status: 302}, {name: "provider error", status: 500},
		{name: "oversize header", status: 200, size: MaxTarballBytes + 1},
		{name: "oversize chunked", status: 200, size: -1, body: strings.Repeat("x", MaxTarballBytes+1)},
		{name: "encoded", status: 200, encoding: "gzip"}, {name: "transport failure", failure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := NewClient()
			calls := 0
			c.http.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if tc.failure {
					return nil, errors.New("secret transport details")
				}
				h := make(http.Header)
				h.Set("Location", "https://169.254.169.254/")
				if tc.encoding != "" {
					h.Set("Content-Encoding", tc.encoding)
				}
				return &http.Response{StatusCode: tc.status, Header: h, ContentLength: tc.size, Body: io.NopCloser(strings.NewReader(tc.body)), Request: r}, nil
			})
			data, err := c.Fetch(context.Background(), Tarball{"https://registry.npmjs.org/a/-/a.tgz", integrity(tc.body)})
			if !errors.Is(err, ErrFetch) || data != nil || calls != 1 {
				t.Fatal(err, calls)
			}
		})
	}
}
