package registryimage

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

var ErrRemote = errors.New("registry metadata could not be read; check the image reference and registry access")
var ErrCredentials = errors.New("registry credentials are incomplete or invalid")

// Credentials are used only for the selected registry's fixed token endpoint.
// Callers must never include them in logs, project metadata or API responses.
type Credentials struct {
	Username string `json:"-"`
	Password string `json:"-"`
}

func (Credentials) String() string   { return "[registry credentials redacted]" }
func (Credentials) GoString() string { return "[registry credentials redacted]" }

type remote struct {
	client *http.Client
	ref    Reference
	token  string
	host   string
}

var excluded = func() []netip.Prefix {
	out := []netip.Prefix{}
	for _, s := range []string{"0.0.0.0/8", "100.64.0.0/10", "168.63.129.16/32", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001::/23", "2001:db8::/32", "2002::/16", "3fff::/20"} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

func publicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	if ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip) {
		return false
	}
	for _, p := range excluded {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}
func dialPublic(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || port != "443" {
		return nil, ErrRemote
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(ips) == 0 {
		return nil, ErrRemote
	}
	for _, ip := range ips {
		if !publicIP(ip) {
			return nil, ErrRemote
		}
	}
	d := net.Dialer{Timeout: 5 * time.Second}
	for _, ip := range ips {
		conn, e := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if e == nil {
			return conn, nil
		}
		if ctx.Err() != nil {
			break
		}
	}
	return nil, ErrRemote
}
func validURL(u *url.URL) bool {
	return u != nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.Fragment == "" && (u.Port() == "" || u.Port() == "443") && len(u.String()) <= 8192
}
func newRemote(r Reference) *remote {
	t := &http.Transport{Proxy: nil, DialContext: dialPublic, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 8 * time.Second, DisableCompression: true, MaxResponseHeaderBytes: 64 << 10, MaxIdleConns: 4, IdleConnTimeout: 15 * time.Second}
	host := "ghcr.io"
	if r.Registry == "docker.io" {
		host = "registry-1.docker.io"
	}
	return &remote{ref: r, host: host, client: &http.Client{Transport: t, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (r *remote) authorize(ctx context.Context, c Credentials) error {
	if (c.Username == "") != (c.Password == "") || len(c.Username) > 254 || len(c.Password) > 8192 || strings.ContainsAny(c.Username, ":\r\n") || strings.ContainsAny(c.Password, "\r\n") {
		return ErrCredentials
	}
	endpoint := "https://ghcr.io/token"
	service := "ghcr.io"
	if r.ref.Registry == "docker.io" {
		endpoint = "https://auth.docker.io/token"
		service = "registry.docker.io"
	}
	u, _ := url.Parse(endpoint)
	q := u.Query()
	q.Set("service", service)
	q.Set("scope", "repository:"+r.ref.Repository+":pull")
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return ErrRemote
	}
	if c.Username != "" {
		req.SetBasicAuth(c.Username, c.Password)
	}
	response, err := r.client.Do(req)
	if err != nil {
		return ErrRemote
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return ErrRemote
	}
	b, err := io.ReadAll(io.LimitReader(response.Body, ConfigLimit+1))
	if err != nil || len(b) > ConfigLimit {
		return ErrRemote
	}
	var data struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if json.Unmarshal(b, &data) != nil {
		return ErrRemote
	}
	if data.Token == "" {
		data.Token = data.AccessToken
	}
	if data.Token == "" || len(data.Token) > 16384 || strings.ContainsAny(data.Token, "\r\n") {
		return ErrRemote
	}
	r.token = data.Token
	return nil
}
func (r *remote) fetch(ctx context.Context, kind, version string, limit int) ([]byte, error) {
	u, err := url.Parse("https://" + r.host + "/v2/" + r.ref.Repository + "/" + kind + "/" + version)
	if err != nil {
		return nil, ErrRemote
	}
	for redirects := 0; redirects <= 3; redirects++ {
		if !validURL(u) {
			return nil, ErrRemote
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, ErrRemote
		}
		req.Header.Set("Accept", Accept)
		// No bearer tokens on redirected requests, even when a redirect returns to
		// the registry. Signed public CDN URLs must authorize themselves.
		if redirects == 0 {
			req.Header.Set("Authorization", "Bearer "+r.token)
		}
		response, err := r.client.Do(req)
		if err != nil {
			return nil, ErrRemote
		}
		if kind == "blobs" && (response.StatusCode == 301 || response.StatusCode == 302 || response.StatusCode == 303 || response.StatusCode == 307 || response.StatusCode == 308) {
			location := response.Header.Get("Location")
			response.Body.Close()
			if location == "" {
				return nil, ErrRemote
			}
			u, err = u.Parse(location)
			if err != nil {
				return nil, ErrRemote
			}
			continue
		}
		if response.StatusCode != 200 {
			response.Body.Close()
			return nil, ErrRemote
		}
		b, err := io.ReadAll(io.LimitReader(response.Body, int64(limit)+1))
		response.Body.Close()
		if err != nil {
			return nil, ErrRemote
		}
		if len(b) > limit {
			return nil, ErrSize
		}
		return b, nil
	}
	return nil, ErrRemote
}
func (r *remote) Manifest(ctx context.Context, version string) ([]byte, error) {
	if !tagPattern.MatchString(version) && !digestPattern.MatchString(version) {
		return nil, ErrReference
	}
	return r.fetch(ctx, "manifests", version, ManifestLimit)
}
func (r *remote) Config(ctx context.Context, digest string) ([]byte, error) {
	if !digestPattern.MatchString(digest) {
		return nil, ErrManifest
	}
	return r.fetch(ctx, "blobs", digest, ConfigLimit)
}

// Resolve authenticates for pull-only metadata access and pins a linux/arm64
// manifest. Layers are not downloaded or verified here: the import/deployment
// path must still enforce image limits, runtime policy, readiness and rollback.
func Resolve(ctx context.Context, image string, c Credentials) (Candidate, error) {
	ref, err := Parse(image)
	if err != nil {
		return Candidate{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	r := newRemote(ref)
	defer r.client.CloseIdleConnections()
	if err = r.authorize(ctx, c); err != nil {
		return Candidate{}, err
	}
	return Validate(ctx, ref, r)
}
