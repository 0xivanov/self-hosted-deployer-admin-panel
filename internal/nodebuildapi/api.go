// Package nodebuildapi provides the private HTTPS transport used by the Node
// build worker. It carries trusted control metadata and bounded ZIP bytes only.
package nodebuildapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodeartifact"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodebuild"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/portal"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/projectarchive"
)

const maxMessage = 16384
const maxMetadata = 8192

var ErrAssignment = errors.New("invalid Node build API assignment")
var ErrTransport = errors.New("Node build executor unavailable")

type observationMessage struct {
	Nonce       string
	Observation portal.NodeExecutionObservation
}

func validID(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && hex.EncodeToString(b) == s
}
func validHost(host string) bool {
	u, e := url.Parse("https://" + host)
	return e == nil && u.Host == host && u.Hostname() != "" && u.User == nil && u.Path == "" && u.RawQuery == "" && !u.ForceQuery && u.Fragment == ""
}
func validArch(s string) bool { return s == "amd64" || s == "arm64" }
func validRequest(q portal.NodeExecutionRequest, project, toolchain, arch string) bool {
	return validID(q.ExecutionID) && validID(q.BuildID) && q.ProjectID == project && q.ToolchainSHA256 == toolchain && q.Plan.Architecture == arch && q.Plan.SourceSHA256 != "" && validID(q.Plan.SourceSHA256) && q.Bundle.ManifestSHA256 != "" && validID(q.Bundle.ManifestSHA256) && q.NotAfter > time.Now().Unix() && q.NotAfter <= time.Now().Add(time.Minute).Unix() && strings.HasPrefix(q.Bundle.Directory, "dependencies-") && validID(strings.TrimPrefix(q.Bundle.Directory, "dependencies-"))
}
func validObservation(o portal.NodeExecutionObservation, execution, project, toolchain, arch string) bool {
	return o.ProjectID == project && o.ExecutionID == execution && validID(execution) && validID(project) && o.SourceSHA256 != "" && validID(o.SourceSHA256) && o.ToolchainSHA256 == toolchain && o.Architecture == arch && (o.Outcome == "running" || o.Outcome == "succeeded" || o.Outcome == "failed" || o.Outcome == "cancelled")
}
func validArtifact(o portal.NodeArtifactObservation, execution, project, toolchain, arch string, manifest string, archive []byte) bool {
	return validObservation(o.NodeExecutionObservation, execution, project, toolchain, arch) && o.Outcome == "succeeded" && o.Retired && (manifest == "" || o.DependencyManifestSHA256 == manifest) && validID(o.DependencyManifestSHA256) && validID(o.ArtifactSHA256) && (archive == nil || (len(archive) > 0 && len(archive) <= nodeartifact.MaxCompressed))
}

// Handler serves only the private management API. provider is the trusted
// executor implementation; this package does not pretend to provide VM
// isolation itself.
func Handler(host, project, toolchain, architecture, token string, provider portal.NodeBuildExecutor) (http.Handler, error) {
	if !validHost(host) || !validID(project) || !validID(toolchain) || !validArch(architecture) || !validID(token) || provider == nil {
		return nil, ErrAssignment
	}
	expected := sha256.Sum256([]byte("Bearer " + token))
	slots := make(chan struct{}, 4)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		auth := sha256.Sum256([]byte(r.Header.Get("Authorization")))
		if r.TLS == nil || r.Host != host || r.Header.Get("X-Project-ID") != project || r.Header.Get("X-Toolchain-SHA256") != toolchain || r.Header.Get("X-Architecture") != architecture || len(r.Header.Values("Authorization")) != 1 || subtle.ConstantTimeCompare(expected[:], auth[:]) != 1 || len(r.Header.Values("Origin")) != 0 || r.Header.Get("Sec-Fetch-Site") != "" {
			http.Error(w, "Management access denied", http.StatusForbidden)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/v1/executions/")
		if !strings.HasPrefix(r.URL.Path, "/v1/executions/") || !validID(id) || r.URL.RawPath != "" || r.URL.RawQuery != "" || r.URL.ForceQuery {
			http.Error(w, "Invalid build request", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
		defer cancel()
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			http.Error(w, "Build executor unavailable", http.StatusServiceUnavailable)
			return
		}
		switch r.Method {
		case http.MethodPost:
			if r.Header.Get("Content-Encoding") != "" {
				http.Error(w, "Invalid build request", http.StatusBadRequest)
				return
			}
			if r.Header.Get("Content-Type") == "application/zip" {
				if len(r.Header.Get("X-Execution-Request")) > maxMetadata {
					http.Error(w, "Invalid metadata", 400)
					return
				}
				meta, e := base64.RawStdEncoding.DecodeString(r.Header.Get("X-Execution-Request"))
				var q portal.NodeExecutionRequest
				if e != nil || strictJSON(meta, &q) != nil || q.ExecutionID != id || !validRequest(q, project, toolchain, architecture) || r.ContentLength < 1 || r.ContentLength > projectarchive.MaxCompressed {
					http.Error(w, "Invalid build request", http.StatusBadRequest)
					return
				}
				q.Archive = nil
				raw, e := io.ReadAll(io.LimitReader(r.Body, projectarchive.MaxCompressed+1))
				if e != nil || len(raw) > projectarchive.MaxCompressed || len(raw) == 0 || !archiveMatches(raw, q.Plan.SourceSHA256) {
					http.Error(w, "Invalid build request", http.StatusBadRequest)
					return
				}
				q.Archive = raw
				if !validSource(ctx, q) {
					http.Error(w, "Invalid build plan", 400)
					return
				}
				if e = provider.SubmitNodeExecution(ctx, q); e != nil {
					http.Error(w, "Build executor unavailable", http.StatusServiceUnavailable)
					return
				}
				w.WriteHeader(http.StatusAccepted)
				return
			}
			http.Error(w, "Expected a ZIP archive", http.StatusBadRequest)
		case http.MethodGet:
			if r.ContentLength != 0 || len(r.TransferEncoding) != 0 || r.Header.Get("Content-Encoding") != "" {
				http.Error(w, "Unexpected request body", 400)
				return
			}
			nonce := r.Header.Get("X-Observation-Nonce")
			if !validID(nonce) {
				http.Error(w, "Invalid observation request", http.StatusBadRequest)
				return
			}
			started := time.Now()
			o, err := provider.InspectNodeExecution(ctx, id)
			if err != nil || !fresh(o.ObservedAt, started) || !validObservation(o, id, project, toolchain, architecture) {
				http.Error(w, "Build observation unavailable", http.StatusServiceUnavailable)
				return
			}
			o.ObservedAt = time.Time{}
			writeJSON(w, observationMessage{Nonce: nonce, Observation: o})
		case http.MethodPut:
			if r.ContentLength != 0 || len(r.TransferEncoding) != 0 || r.Header.Get("Content-Encoding") != "" || r.Header.Get("Accept") != "application/zip" {
				http.Error(w, "Invalid artifact request", 400)
				return
			}
			nonce := r.Header.Get("X-Artifact-Nonce")
			if !validID(nonce) {
				http.Error(w, "Invalid artifact request", http.StatusBadRequest)
				return
			}
			started := time.Now()
			o, archive, err := provider.ReadNodeArtifact(ctx, id)
			if err != nil || !fresh(o.ObservedAt, started) || !validArtifact(o, id, project, toolchain, architecture, "", archive) || !archiveMatches(archive, o.ArtifactSHA256) {
				http.Error(w, "Build artifact unavailable", http.StatusServiceUnavailable)
				return
			}
			if _, err = nodeartifact.Validate(ctx, archive, o.ArtifactSHA256); err != nil {
				http.Error(w, "Invalid artifact", 503)
				return
			}
			o.ObservedAt = time.Time{}
			if r.Header.Get("Accept") == "application/zip" {
				raw, _ := json.Marshal(o)
				w.Header().Set("Content-Type", "application/zip")
				w.Header().Set("X-Artifact-Observation", base64.RawStdEncoding.EncodeToString(raw))
				w.Header().Set("X-Artifact-Nonce", nonce)
				_, _ = w.Write(archive)
				return
			}

		default:
			w.Header().Set("Allow", "GET, POST, PUT")
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}), nil
}
func writeJSON(w http.ResponseWriter, v any) {
	raw, err := json.Marshal(v)
	if err != nil || len(raw) > maxMessage {
		http.Error(w, "Build response unavailable", 503)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}
func fresh(t, started time.Time) bool {
	n := time.Now()
	return !t.IsZero() && !t.Before(started) && !t.After(n)
}
func archiveMatches(raw []byte, expected string) bool {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]) == expected
}

type Client struct {
	endpoint, project, toolchain, architecture, token string
	http                                              *http.Client
}

var _ portal.NodeBuildExecutor = (*Client)(nil)

func NewClient(endpoint, project, toolchain, architecture, token string, roots *x509.CertPool) (*Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || !validHost(u.Host) || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || !validID(project) || !validID(toolchain) || !validArch(architecture) || !validID(token) {
		return nil, ErrAssignment
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	t.DisableCompression = true
	t.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
	t.DialContext = (&net.Dialer{Timeout: 3 * time.Second}).DialContext
	t.ResponseHeaderTimeout = 35 * time.Second
	t.MaxResponseHeaderBytes = 16384
	return &Client{endpoint: endpoint, project: project, toolchain: toolchain, architecture: architecture, token: token, http: &http.Client{Transport: t, Timeout: 40 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) Close() {
	if c != nil && c.http != nil {
		c.http.CloseIdleConnections()
	}
}
func (c *Client) request(ctx context.Context, method, id string, body any, nonce, manifest string) (*http.Response, error) {
	var data io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil || len(raw) > maxMessage {
			return nil, ErrTransport
		}
		data = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint+"/v1/executions/"+id, data)
	if err != nil {
		return nil, ErrTransport
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-Project-ID", c.project)
	req.Header.Set("X-Toolchain-SHA256", c.toolchain)
	req.Header.Set("X-Architecture", c.architecture)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Cache-Control", "no-cache, no-store")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if nonce != "" {
		req.Header.Set("X-Observation-Nonce", nonce)
		req.Header.Set("X-Artifact-Nonce", nonce)
	}
	if manifest != "" {
		req.Header.Set("X-Manifest-SHA256", manifest)
	}
	response, err := c.http.Do(req)
	if err != nil {
		return nil, ErrTransport
	}
	return response, nil
}
func decodeResponse(response *http.Response, out any, limit int64) error {
	defer response.Body.Close()
	typ, _, e := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if response.StatusCode != http.StatusOK || e != nil || typ != "application/json" || response.Header.Get("Content-Encoding") != "" || response.ContentLength > limit {
		return ErrTransport
	}
	raw, e := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if e != nil || int64(len(raw)) > limit {
		return ErrTransport
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || d.Decode(&struct{}{}) != io.EOF {
		return ErrTransport
	}
	return nil
}
func (c *Client) SubmitNodeExecution(ctx context.Context, q portal.NodeExecutionRequest) error {
	if !validRequest(q, c.project, c.toolchain, c.architecture) || len(q.Archive) == 0 || len(q.Archive) > projectarchive.MaxCompressed || !validSource(ctx, q) {
		return ErrAssignment
	}
	meta := q
	meta.Archive = nil
	raw, e := json.Marshal(meta)
	if e != nil || len(base64.RawStdEncoding.EncodeToString(raw)) > maxMetadata {
		return ErrTransport
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/v1/executions/"+q.ExecutionID, bytes.NewReader(q.Archive))
	if e != nil {
		return ErrTransport
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-Project-ID", c.project)
	req.Header.Set("X-Toolchain-SHA256", c.toolchain)
	req.Header.Set("X-Architecture", c.architecture)
	req.Header.Set("X-Execution-Request", base64.RawStdEncoding.EncodeToString(raw))
	req.Header.Set("Content-Type", "application/zip")
	response, e := c.http.Do(req)
	if e != nil {
		return ErrTransport
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		return ErrTransport
	}
	return nil
}
func (c *Client) InspectNodeExecution(ctx context.Context, id string) (portal.NodeExecutionObservation, error) {
	var zero portal.NodeExecutionObservation
	if !validID(id) {
		return zero, ErrAssignment
	}
	var entropy [32]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return zero, ErrTransport
	}
	nonce := hex.EncodeToString(entropy[:])
	response, err := c.request(ctx, http.MethodGet, id, nil, nonce, "")
	if err != nil {
		return zero, err
	}
	var m observationMessage
	if err = decodeResponse(response, &m, maxMessage); err != nil || m.Nonce != nonce || !validObservation(m.Observation, id, c.project, c.toolchain, c.architecture) {
		return zero, ErrTransport
	}
	m.Observation.ObservedAt = time.Now()
	return m.Observation, nil
}
func (c *Client) ReadNodeArtifact(ctx context.Context, id string) (portal.NodeArtifactObservation, []byte, error) {
	var zero portal.NodeArtifactObservation
	if !validID(id) {
		return zero, nil, ErrAssignment
	}
	var entropy [32]byte
	if _, e := rand.Read(entropy[:]); e != nil {
		return zero, nil, ErrTransport
	}
	nonce := hex.EncodeToString(entropy[:])
	req, e := http.NewRequestWithContext(ctx, http.MethodPut, c.endpoint+"/v1/executions/"+id, nil)
	if e != nil {
		return zero, nil, ErrTransport
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-Project-ID", c.project)
	req.Header.Set("X-Toolchain-SHA256", c.toolchain)
	req.Header.Set("X-Architecture", c.architecture)
	req.Header.Set("X-Artifact-Nonce", nonce)
	req.Header.Set("Accept", "application/zip")
	response, e := c.http.Do(req)
	if e != nil {
		return zero, nil, ErrTransport
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "application/zip" || response.Header.Get("Content-Encoding") != "" || response.ContentLength > nodeartifact.MaxCompressed || len(response.Header.Get("X-Artifact-Observation")) > maxMetadata {
		return zero, nil, ErrTransport
	}
	meta, e := base64.RawStdEncoding.DecodeString(response.Header.Get("X-Artifact-Observation"))
	if e != nil {
		return zero, nil, ErrTransport
	}
	var o portal.NodeArtifactObservation
	d := json.NewDecoder(bytes.NewReader(meta))
	d.DisallowUnknownFields()
	if d.Decode(&o) != nil || d.Decode(&struct{}{}) != io.EOF || o.Outcome != "succeeded" || !o.Retired || response.Header.Get("X-Artifact-Nonce") != nonce || !validObservation(o.NodeExecutionObservation, id, c.project, c.toolchain, c.architecture) || !validID(o.DependencyManifestSHA256) || !validID(o.ArtifactSHA256) {
		return zero, nil, ErrTransport
	}
	archive, e := io.ReadAll(io.LimitReader(response.Body, nodeartifact.MaxCompressed+1))
	if e != nil || len(archive) > nodeartifact.MaxCompressed || len(archive) == 0 || !archiveMatches(archive, o.ArtifactSHA256) {
		return zero, nil, ErrTransport
	}
	if _, e = nodeartifact.Validate(ctx, archive, o.ArtifactSHA256); e != nil {
		return zero, nil, ErrTransport
	}
	o.ObservedAt = time.Now()
	return o, archive, nil
}

func strictJSON(raw []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || d.Decode(&struct{}{}) != io.EOF {
		return ErrAssignment
	}
	return nil
}
func validSource(ctx context.Context, q portal.NodeExecutionRequest) bool {
	for _, skip := range []bool{false, true} {
		p, err := nodebuild.Prepare(ctx, q.Archive, q.Plan.SourceSHA256, nodebuild.Settings{Architecture: q.Plan.Architecture, SkipBuild: skip})
		if err != nil {
			return false
		}
		if reflect.DeepEqual(p, q.Plan) {
			return true
		}
	}
	return false
}
