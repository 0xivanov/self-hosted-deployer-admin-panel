package npmfetch

import (
	"context"
	"crypto/sha512"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"time"
)

var ErrFetch = errors.New("registry tarball download failed validation")

type Client struct{ http *http.Client }

func NewClient() *Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = (&net.Dialer{Timeout: 10 * time.Second}).DialContext
	transport.ResponseHeaderTimeout = 10 * time.Second
	transport.DisableCompression = true
	return &Client{http: &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// Fetch returns only checksum-verified bytes. It never decompresses a package,
// executes scripts or accepts credentials. Caller must bound aggregate storage
// and concurrency and isolate the fetch service from private networks at the VM
// firewall; hostname validation is not a replacement for network isolation.
func (c *Client) Fetch(ctx context.Context, t Tarball) ([]byte, error) {
	if c == nil || c.http == nil || !validTarball(t) {
		return nil, ErrDependency
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, t.URL, nil)
	if err != nil {
		return nil, ErrFetch
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, ErrFetch
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.ContentLength > MaxTarballBytes || response.Header.Get("Content-Encoding") != "" {
		return nil, ErrFetch
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, MaxTarballBytes+1))
	if err != nil || len(data) > MaxTarballBytes {
		return nil, ErrFetch
	}
	sum := sha512.Sum512(data)
	if "sha512-"+base64.StdEncoding.EncodeToString(sum[:]) != t.Integrity {
		return nil, ErrFetch
	}
	return data, nil
}
