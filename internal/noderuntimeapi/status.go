// Package noderuntimeapi provides project/runtime-bound management transport.
// It must use a private listener separate from customer content. Handler exposes no
// deployment mutations by default; DeploymentHandler adds trusted submission.
package noderuntimeapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/noderouter"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/portal"
)

const maxResponse = 16 << 10

var ErrAssignment = errors.New("invalid Node runtime API assignment")
var ErrObservation = errors.New("Node runtime observation unavailable or invalid")

func validID(s string) bool {
	v, e := hex.DecodeString(s)
	return e == nil && len(v) == 32 && hex.EncodeToString(v) == s
}
func validHost(host string) bool {
	u, e := url.Parse("https://" + host)
	return e == nil && u.Host == host && u.Hostname() != "" && u.User == nil && u.Path == "" && u.RawQuery == "" && !u.ForceQuery && u.Fragment == ""
}
func validCandidate(c noderouter.Candidate, project, runtime string) bool {
	return c.ProjectID == project && c.RuntimeID == runtime && validID(c.DeploymentID) && validID(c.OperationID) && validID(c.ArtifactSHA256) && c.Revision > 0 && len(c.Backend) > 0 && len(c.Backend) <= 64
}
func validObservation(o portal.NodeRuntimeObservation, project, runtime, operation string) bool {
	if !validCandidate(o.Routing.Fence, project, runtime) || o.Routing.Fence.OperationID != operation || !validID(o.ToolchainSHA256) || (o.Architecture != "arm64" && o.Architecture != "amd64") {
		return false
	}
	active := o.Routing.Active
	if active != nil && (!validCandidate(*active, project, runtime) || active.Revision > o.Routing.Fence.Revision) {
		return false
	}
	switch o.Routing.Status {
	case "active":
		return active != nil && *active == o.Routing.Fence
	case "pending", "failed":
		return (active == nil || active.Revision < o.Routing.Fence.Revision) && (o.Routing.Status != "pending" || !o.Settled)
	default:
		return false
	}
}

type envelope struct {
	Nonce       string
	Observation portal.NodeRuntimeObservation
}

// Handler authenticates a private status read and invokes the provider afresh.
// The provider must check process/artifact binding, current health and operation
// settlement; wrapping an old router snapshot alone cannot supply these proofs.
func Handler(host, project, runtime, token string, provider portal.NodeRuntimeReader) (http.Handler, error) {
	if !validHost(host) || !validID(project) || !validID(runtime) || !validID(token) || provider == nil {
		return nil, ErrAssignment
	}
	expected := sha256.Sum256([]byte("Bearer " + token))
	slots := make(chan struct{}, 4)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		auth := sha256.Sum256([]byte(r.Header.Get("Authorization")))
		if r.TLS == nil || r.Host != host || len(r.Header.Values("Origin")) != 0 || r.Header.Get("Sec-Fetch-Site") != "" || len(r.Header.Values("Authorization")) != 1 || subtle.ConstantTimeCompare(expected[:], auth[:]) != 1 || r.Header.Get("X-Project-ID") != project || r.Header.Get("X-Runtime-ID") != runtime {
			http.Error(w, "Management access denied", http.StatusForbidden)
			return
		}
		operation := strings.TrimPrefix(r.URL.Path, "/v1/operations/")
		nonce := r.Header.Get("X-Observation-Nonce")
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/v1/operations/") || !validID(operation) || !validID(nonce) || r.URL.RawPath != "" || r.URL.RawQuery != "" || r.URL.ForceQuery || r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
			http.Error(w, "Invalid observation request", http.StatusBadRequest)
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			w.Header().Set("Retry-After", "1")
			http.Error(w, "Runtime busy", http.StatusServiceUnavailable)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		observation, err := provider.InspectNodeRuntime(ctx, runtime, project, operation)
		if err != nil || ctx.Err() != nil || !validObservation(observation, project, runtime, operation) {
			http.Error(w, "Runtime observation unavailable", http.StatusServiceUnavailable)
			return
		}
		// The worker stamps receipt time in its own clock domain after validating the
		// fresh nonce. Remote wall-clock skew must not fabricate observation freshness.
		observation.ObservedAt = time.Time{}
		raw, err := json.Marshal(envelope{Nonce: nonce, Observation: observation})
		if err != nil || len(raw) > maxResponse {
			http.Error(w, "Runtime observation unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(raw)
	}), nil
}

type Client struct {
	endpoint, project, runtime, token string
	http                              *http.Client
}

var _ portal.NodeRuntimeReader = (*Client)(nil)

// NewClient requires an operator-assigned HTTPS origin and validates certificates.
// Roots optionally pins a dedicated management CA. Redirects, proxy inheritance,
// automatic decompression and response bodies over 16 KiB are rejected.
func NewClient(endpoint, project, runtime, token string, roots *x509.CertPool) (*Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || !validHost(u.Host) || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(endpoint, "#") || u.Opaque != "" || !validID(project) || !validID(runtime) || !validID(token) {
		return nil, ErrAssignment
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DisableCompression = true
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
	transport.DialContext = (&net.Dialer{Timeout: 3 * time.Second}).DialContext
	transport.ResponseHeaderTimeout = 12 * time.Second
	return &Client{endpoint: endpoint, project: project, runtime: runtime, token: token, http: &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) Close() { c.http.CloseIdleConnections() }
func (c *Client) InspectNodeRuntime(ctx context.Context, runtime, project, operation string) (portal.NodeRuntimeObservation, error) {
	var empty portal.NodeRuntimeObservation
	if runtime != c.runtime || project != c.project || !validID(operation) {
		return empty, ErrAssignment
	}
	var entropy [32]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return empty, err
	}
	nonce := hex.EncodeToString(entropy[:])
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+"/v1/operations/"+operation, nil)
	if err != nil {
		return empty, ErrAssignment
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-Project-ID", project)
	req.Header.Set("X-Runtime-ID", runtime)
	req.Header.Set("X-Observation-Nonce", nonce)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Cache-Control", "no-cache, no-store")
	response, err := c.http.Do(req)
	if err != nil {
		return empty, ErrObservation
	}
	defer response.Body.Close()
	typ, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if response.StatusCode != http.StatusOK || err != nil || typ != "application/json" || response.Header.Get("Content-Encoding") != "" || response.ContentLength > maxResponse {
		return empty, ErrObservation
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponse+1))
	if err != nil || len(raw) > maxResponse {
		return empty, ErrObservation
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var message envelope
	if decoder.Decode(&message) != nil || decoder.Decode(&struct{}{}) != io.EOF || message.Nonce != nonce || !validObservation(message.Observation, project, runtime, operation) {
		return empty, ErrObservation
	}
	if err = ctx.Err(); err != nil {
		return empty, err
	}
	message.Observation.ObservedAt = time.Now()
	return message.Observation, nil
}
