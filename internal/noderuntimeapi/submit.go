package noderuntimeapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodeartifact"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/portal"
)

var ErrSubmission = errors.New("Node runtime submission unavailable or invalid")

type acceptance struct {
	OperationID, ArtifactSHA256 string
	Revision                    int64
}

func validDeploymentRequest(r portal.NodeRuntimeRequest, project, runtime string) bool {
	return r.ProjectID == project && r.RuntimeID == runtime && validID(r.OperationID) && validID(r.DeploymentID) && validID(r.ReleaseID) && validID(r.ArtifactSHA256) && validID(r.ToolchainSHA256) && (r.Architecture == "arm64" || r.Architecture == "amd64") && r.Revision > 0 && r.ActivateBefore > time.Now().Unix() && r.ActivateBefore <= time.Now().Add(90*time.Second).Unix()
}

// DeploymentHandler adds authenticated submission to the existing read endpoint.
// provider must persist/deduplicate requests before returning success, enforce
// activation deadlines and use the runtime's installation/start/retirement gates.
// Serve on a private TLS listener with bounded HTTP read/write timeouts.
func DeploymentHandler(host, project, runtime, token string, reader portal.NodeRuntimeReader, provider portal.NodeRuntimeSubmitter) (http.Handler, error) {
	status, err := Handler(host, project, runtime, token, reader)
	if err != nil {
		return nil, err
	}
	if provider == nil {
		return nil, ErrAssignment
	}
	expected := sha256.Sum256([]byte("Bearer " + token))
	slots := make(chan struct{}, 2)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/deployments" {
			status.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		auth := sha256.Sum256([]byte(r.Header.Get("Authorization")))
		if r.TLS == nil || r.Host != host || len(r.Header.Values("Origin")) != 0 || r.Header.Get("Sec-Fetch-Site") != "" || len(r.Header.Values("Authorization")) != 1 || subtle.ConstantTimeCompare(auth[:], expected[:]) != 1 || r.Header.Get("X-Project-ID") != project || r.Header.Get("X-Runtime-ID") != runtime {
			http.Error(w, "Management access denied", 403)
			return
		}
		if r.Method != "POST" {
			w.Header().Set("Allow", "POST")
			http.Error(w, "Method not allowed", 405)
			return
		}
		if r.URL.RawPath != "" || r.URL.RawQuery != "" || r.URL.ForceQuery || r.Header.Get("Content-Type") != "application/zip" || r.Header.Get("Content-Encoding") != "" || r.ContentLength < 1 || r.ContentLength > nodeartifact.MaxCompressed || len(r.Header.Values("X-Deployment")) != 1 || len(r.Header.Get("X-Deployment")) > 4096 {
			http.Error(w, "Invalid deployment request", 400)
			return
		}
		raw, err := base64.RawURLEncoding.DecodeString(r.Header.Get("X-Deployment"))
		if err != nil {
			http.Error(w, "Invalid deployment request", 400)
			return
		}
		var request portal.NodeRuntimeRequest
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&request) != nil || decoder.Decode(&struct{}{}) != io.EOF || !validDeploymentRequest(request, project, runtime) {
			http.Error(w, "Invalid deployment request", 400)
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			http.Error(w, "Runtime busy", 503)
			return
		}
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, nodeartifact.MaxCompressed))
		if err != nil || int64(len(data)) != r.ContentLength {
			http.Error(w, "Invalid deployment archive", 400)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
		defer cancel()
		if _, err = nodeartifact.Validate(ctx, data, request.ArtifactSHA256); err != nil || !validDeploymentRequest(request, project, runtime) {
			http.Error(w, "Invalid deployment archive", 400)
			return
		}
		request.Archive = data
		if err = provider.SubmitNodeRuntime(ctx, request); err != nil || ctx.Err() != nil {
			http.Error(w, "Runtime submission unavailable; reconcile operation", 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(acceptance{request.OperationID, request.ArtifactSHA256, request.Revision})
	}), nil
}

var _ portal.NodeRuntimeSubmitter = (*Client)(nil)

func (c *Client) SubmitNodeRuntime(ctx context.Context, r portal.NodeRuntimeRequest) error {
	if !validDeploymentRequest(r, c.project, c.runtime) {
		return ErrAssignment
	}
	if _, err := nodeartifact.Validate(ctx, r.Archive, r.ArtifactSHA256); err != nil {
		return ErrAssignment
	}
	metadata, err := json.Marshal(r)
	if err != nil {
		return ErrAssignment
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.endpoint+"/v1/deployments", bytes.NewReader(r.Archive))
	if err != nil {
		return ErrAssignment
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-Project-ID", c.project)
	req.Header.Set("X-Runtime-ID", c.runtime)
	req.Header.Set("X-Deployment", base64.RawURLEncoding.EncodeToString(metadata))
	req.Header.Set("Content-Type", "application/zip")
	response, err := c.http.Do(req)
	if err != nil {
		return ErrSubmission
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted || response.Header.Get("Content-Type") != "application/json" || response.Header.Get("Content-Encoding") != "" {
		return ErrSubmission
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponse+1))
	if err != nil || len(raw) > maxResponse {
		return ErrSubmission
	}
	var result acceptance
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil || decoder.Decode(&struct{}{}) != io.EOF || result != (acceptance{r.OperationID, r.ArtifactSHA256, r.Revision}) {
		return ErrSubmission
	}
	return ctx.Err()
}
