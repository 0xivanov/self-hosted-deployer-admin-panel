// Package staticpublish provides a private, project-bound runtime transport.
// It must run on a management listener separate from the public content host.
package staticpublish

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/projectarchive"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/staticsite"
)

type Status struct {
	Release  string `json:"release"`
	Revision int64  `json:"revision"`
}

func validSecret(token string) bool {
	b, err := hex.DecodeString(token)
	return err == nil && len(b) == 32
}
func validProject(id string) bool { b, err := hex.DecodeString(id); return err == nil && len(b) == 32 }

func Handler(site *staticsite.Site, host, project, token string) (http.Handler, error) {
	if site == nil || host == "" || strings.ContainsAny(host, "/\\@ \r\n") || !validProject(project) || !validSecret(token) {
		return nil, errors.New("invalid runtime assignment")
	}
	slots := make(chan struct{}, 1)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.TLS == nil || r.Host != host || r.Header.Get("Origin") != "" {
			http.Error(w, "Management access denied", 403)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 || r.Header.Get("X-Project-ID") != project {
			http.Error(w, "Management access denied", 403)
			return
		}
		if r.URL.Path == "/status" && r.Method == "GET" {
			id, revision := site.Snapshot()
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(Status{id, revision})
			return
		}
		if r.URL.Path != "/publish" || r.Method != "POST" {
			http.NotFound(w, r)
			return
		}
		revision, err := strconv.ParseInt(r.Header.Get("X-Publication-Revision"), 10, 64)
		if err != nil || revision < 1 {
			http.Error(w, "Invalid publication revision", 400)
			return
		}
		typ, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || typ != "application/zip" {
			http.Error(w, "ZIP required", 415)
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			w.Header().Set("Retry-After", "1")
			http.Error(w, "Publication busy", 503)
			return
		}
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, projectarchive.MaxCompressed))
		if err != nil {
			http.Error(w, "Upload exceeds limit or was interrupted", 413)
			return
		}
		id, err := site.PublishRevision(r.Context(), revision, data)
		if err != nil {
			if errors.Is(err, staticsite.ErrStaleRevision) {
				http.Error(w, "Stale publication revision", 409)
			} else {
				http.Error(w, "Publication could not be acknowledged; reconcile runtime state", 503)
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(Status{id, revision})
	}), nil
}

type Client struct {
	endpoint, project, token string
	http                     *http.Client
}

// NewClient requires an operator-assigned HTTPS origin. Roots is optional and
// supports a dedicated management CA; certificate verification is never disabled.
func NewClient(endpoint, project, token string, roots *x509.CertPool) (*Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || !validProject(project) || !validSecret(token) {
		return nil, errors.New("invalid runtime client assignment")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
	return &Client{endpoint: endpoint, project: project, token: token, http: &http.Client{Transport: transport, Timeout: 25 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) Project() string { return c.project }
func (c *Client) Close()          { c.http.CloseIdleConnections() }
func (c *Client) Observe(ctx context.Context) (Status, error) {
	return c.request(ctx, "GET", "/status", 0, nil)
}
func (c *Client) Publish(ctx context.Context, revision int64, data []byte) (Status, error) {
	if revision < 1 || len(data) == 0 || len(data) > projectarchive.MaxCompressed {
		return Status{}, errors.New("invalid publication")
	}
	status, err := c.request(ctx, "POST", "/publish", revision, data)
	if err != nil {
		return Status{}, err
	}
	sum := sha256.Sum256(data)
	if status.Release != hex.EncodeToString(sum[:]) {
		return Status{}, errors.New("runtime archive mismatch")
	}
	return status, nil
}
func (c *Client) request(ctx context.Context, method, path string, revision int64, data []byte) (Status, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, bytes.NewReader(data))
	if err != nil {
		return Status{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-Project-ID", c.project)
	if method == "POST" {
		req.Header.Set("Content-Type", "application/zip")
		req.Header.Set("X-Publication-Revision", strconv.FormatInt(revision, 10))
	}
	res, err := c.http.Do(req)
	if err != nil {
		return Status{}, errors.New("runtime unavailable; publication outcome requires reconciliation")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return Status{}, errors.New("runtime did not acknowledge request")
	}
	decoder := json.NewDecoder(io.LimitReader(res.Body, 4096))
	decoder.DisallowUnknownFields()
	var status Status
	if err = decoder.Decode(&status); err != nil {
		return Status{}, errors.New("invalid runtime acknowledgement")
	}
	if err = decoder.Decode(&struct{}{}); err != io.EOF {
		return Status{}, errors.New("invalid runtime acknowledgement")
	}
	if status.Revision < 0 || (status.Release != "" && !validProject(status.Release)) || (status.Release == "" && status.Revision != 0) {
		return Status{}, errors.New("invalid runtime state")
	}
	if method == "POST" && status.Revision != revision {
		return Status{}, errors.New("runtime revision mismatch")
	}
	return status, nil
}
