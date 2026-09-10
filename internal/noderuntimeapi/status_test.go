//go:build integration

package noderuntimeapi

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/noderouter"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/portal"
)

var projectID = strings.Repeat("a", 64)
var runtimeID = strings.Repeat("b", 64)
var operationID = strings.Repeat("c", 64)
var token = strings.Repeat("d", 64)

type providerFunc func(context.Context, string, string, string) (portal.NodeRuntimeObservation, error)

func (f providerFunc) InspectNodeRuntime(ctx context.Context, runtime, project, operation string) (portal.NodeRuntimeObservation, error) {
	return f(ctx, runtime, project, operation)
}
func observation() portal.NodeRuntimeObservation {
	candidate := noderouter.Candidate{ProjectID: projectID, RuntimeID: runtimeID, DeploymentID: strings.Repeat("e", 64), OperationID: operationID, Revision: 1, ArtifactSHA256: strings.Repeat("f", 64), Backend: "blue"}
	return portal.NodeRuntimeObservation{Routing: noderouter.State{Fence: candidate, Status: "active", Active: &candidate}, ToolchainSHA256: strings.Repeat("1", 64), Architecture: "arm64", Healthy: true, Settled: true, ObservedAt: time.Now().Add(-time.Hour)}
}
func start(t *testing.T, provider portal.NodeRuntimeReader) (*httptest.Server, *Client) {
	t.Helper()
	server := httptest.NewUnstartedServer(nil)
	handler, err := Handler(server.Listener.Addr().String(), projectID, runtimeID, token, provider)
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	server.Config.Handler = handler
	server.StartTLS()
	t.Cleanup(server.Close)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	client, err := NewClient(server.URL, projectID, runtimeID, token, roots)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return server, client
}
func TestRuntimeObservationTLSAndScope(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server, client := start(t, providerFunc(func(ctx context.Context, runtime, project, operation string) (portal.NodeRuntimeObservation, error) {
		calls.Add(1)
		if runtime != runtimeID || project != projectID || operation != operationID {
			t.Fatal("wrong provider scope")
		}
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 10*time.Second {
			t.Fatal("unbounded provider read")
		}
		return observation(), nil
	}))
	before := time.Now()
	got, err := client.InspectNodeRuntime(t.Context(), runtimeID, projectID, operationID)
	if err != nil || got.Routing.Active == nil || got.Routing.Active.OperationID != operationID || got.ObservedAt.Before(before) || calls.Load() != 1 {
		t.Fatal(got, err, calls.Load())
	}
	if _, err = client.InspectNodeRuntime(t.Context(), strings.Repeat("2", 64), projectID, operationID); !errors.Is(err, ErrAssignment) || calls.Load() != 1 {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	wrong, err := NewClient(server.URL, projectID, runtimeID, strings.Repeat("3", 64), roots)
	if err != nil {
		t.Fatal(err)
	}
	defer wrong.Close()
	if _, err = wrong.InspectNodeRuntime(t.Context(), runtimeID, projectID, operationID); !errors.Is(err, ErrObservation) || calls.Load() != 1 {
		t.Fatal(err)
	}
	untrusted, err := NewClient(server.URL, projectID, runtimeID, token, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer untrusted.Close()
	if _, err = untrusted.InspectNodeRuntime(t.Context(), runtimeID, projectID, operationID); !errors.Is(err, ErrObservation) || calls.Load() != 1 {
		t.Fatal("certificate verification bypassed", err)
	}
	if client.http.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("environment proxy enabled")
	}
}
func validRequest() *http.Request {
	r := httptest.NewRequest("GET", "https://management.test/v1/operations/"+operationID, nil)
	r.TLS = &tls.ConnectionState{}
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("X-Project-ID", projectID)
	r.Header.Set("X-Runtime-ID", runtimeID)
	r.Header.Set("X-Observation-Nonce", strings.Repeat("4", 64))
	return r
}
func TestManagementRequestGuards(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	handler, err := Handler("management.test", projectID, runtimeID, token, providerFunc(func(context.Context, string, string, string) (portal.NodeRuntimeObservation, error) {
		calls.Add(1)
		return observation(), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		status int
		change func(*http.Request)
	}{
		{"plain", 403, func(r *http.Request) { r.TLS = nil }},
		{"host", 403, func(r *http.Request) { r.Host = "content.test" }},
		{"origin", 403, func(r *http.Request) { r.Header.Set("Origin", "https://content.test") }},
		{"fetch", 403, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-origin") }},
		{"token", 403, func(r *http.Request) { r.Header.Del("Authorization") }},
		{"duplicate_auth", 403, func(r *http.Request) { r.Header.Add("Authorization", "Bearer "+token) }},
		{"project", 403, func(r *http.Request) { r.Header.Set("X-Project-ID", strings.Repeat("5", 64)) }},
		{"runtime", 403, func(r *http.Request) { r.Header.Set("X-Runtime-ID", strings.Repeat("5", 64)) }},
		{"nonce", 400, func(r *http.Request) { r.Header.Del("X-Observation-Nonce") }},
		{"query", 400, func(r *http.Request) { r.URL.RawQuery = "token=" + token }},
		{"encoded_path", 400, func(r *http.Request) { r.URL.RawPath = "/v1/%6fperations/" + operationID }},
		{"body", 400, func(r *http.Request) { r.ContentLength = 1; r.Body = io.NopCloser(strings.NewReader("x")) }},
		{"mutation", 405, func(r *http.Request) { r.Method = "POST" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := validRequest()
			tc.change(r)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.status || calls.Load() != 0 || strings.Contains(w.Body.String(), token) {
				t.Fatal(w.Code, w.Body.String(), calls.Load())
			}
		})
	}
}
func TestClientRejectsInvalidAndReplayedObservations(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"nonce", "identity", "unknown", "trailing", "oversize", "encoding", "content_type", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			var redirected atomic.Int32
			destination := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
			defer destination.Close()
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				message := envelope{Nonce: r.Header.Get("X-Observation-Nonce"), Observation: observation()}
				switch mode {
				case "nonce":
					message.Nonce = strings.Repeat("0", 64)
				case "identity":
					message.Observation.Routing.Fence.OperationID = strings.Repeat("9", 64)
				case "encoding":
					w.Header().Set("Content-Encoding", "gzip")
				case "content_type":
					w.Header().Set("Content-Type", "text/plain")
				case "redirect":
					http.Redirect(w, r, destination.URL, 302)
					return
				}
				raw, _ := json.Marshal(message)
				if mode == "unknown" {
					raw = append(raw[:len(raw)-1], []byte(`,"Unexpected":true}`)...)
				}
				if mode == "trailing" {
					raw = append(raw, []byte("{}")...)
				}
				if mode == "oversize" {
					w.(http.Flusher).Flush()
					raw = append(raw, []byte(strings.Repeat(" ", maxResponse))...)
				}
				w.Write(raw)
			}))
			defer server.Close()
			roots := x509.NewCertPool()
			roots.AddCert(server.Certificate())
			roots.AddCert(destination.Certificate())
			client, err := NewClient(server.URL, projectID, runtimeID, token, roots)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			if _, err = client.InspectNodeRuntime(t.Context(), runtimeID, projectID, operationID); !errors.Is(err, ErrObservation) {
				t.Fatal(err)
			}
			if redirected.Load() != 0 {
				t.Fatal("redirect followed")
			}
		})
	}
}
func TestManagementConcurrencyIsBounded(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}, 4), make(chan struct{})
	handler, err := Handler("management.test", projectID, runtimeID, token, providerFunc(func(ctx context.Context, _, _, _ string) (portal.NodeRuntimeObservation, error) {
		entered <- struct{}{}
		select {
		case <-release:
			return observation(), nil
		case <-ctx.Done():
			return portal.NodeRuntimeObservation{}, ctx.Err()
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan int, 4)
	for range 4 {
		wg.Go(func() { w := httptest.NewRecorder(); handler.ServeHTTP(w, validRequest()); results <- w.Code })
	}
	for range 4 {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			close(release)
			t.Fatal("provider did not start")
		}
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, validRequest())
	if w.Code != 503 {
		close(release)
		t.Fatal(w.Code)
	}
	close(release)
	wg.Wait()
	close(results)
	for code := range results {
		if code != 200 {
			t.Fatal(code)
		}
	}
}

func TestRuntimeClientRequiresBareHTTPSOrigin(t *testing.T) {
	t.Parallel()
	for _, endpoint := range []string{"http://runtime.test", "https://user:secret@runtime.test", "https://runtime.test/path", "https://runtime.test?token=secret", "https://runtime.test#", "https://runtime.test#fragment"} {
		t.Run(endpoint, func(t *testing.T) {
			client, err := NewClient(endpoint, projectID, runtimeID, token, nil)
			if client != nil {
				client.Close()
			}
			if !errors.Is(err, ErrAssignment) {
				t.Fatal(err)
			}
		})
	}
}
func TestProviderErrorsAreNotExposed(t *testing.T) {
	t.Parallel()
	handler, err := Handler("management.test", projectID, runtimeID, token, providerFunc(func(context.Context, string, string, string) (portal.NodeRuntimeObservation, error) {
		return portal.NodeRuntimeObservation{}, errors.New("private provider detail " + token)
	}))
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, validRequest())
	if w.Code != 503 || strings.Contains(w.Body.String(), token) || strings.Contains(w.Body.String(), "private provider") {
		t.Fatal(w.Code, w.Body.String())
	}
}
