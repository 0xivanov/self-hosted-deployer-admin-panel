package portal

import (
	"context"
	"encoding/hex"
	"errors"
	"net/http"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/registryimage"
)

type ContainerRegistryResolver func(context.Context, string, registryimage.Credentials) (registryimage.Candidate, error)

// ContainerProjectConfig is an operator assignment, never browser input.
type ContainerProjectConfig struct {
	RuntimeID string `json:"runtime_id"`
}

func copyContainerProjects(input map[string]ContainerProjectConfig) (map[string]ContainerProjectConfig, error) {
	out := map[string]ContainerProjectConfig{}
	seen := map[string]bool{}
	valid := func(s string) bool {
		b, e := hex.DecodeString(s)
		return e == nil && len(b) == 32 && hex.EncodeToString(b) == s
	}
	for project, assignment := range input {
		if !valid(project) || !valid(assignment.RuntimeID) || seen[assignment.RuntimeID] {
			return nil, errors.New("invalid or shared container runtime assignment")
		}
		seen[assignment.RuntimeID] = true
		out[project] = assignment
	}
	return out, nil
}
func (h *HTTP) containerProjectSnapshot() map[string]ContainerProjectConfig {
	if h.containerProjectLookup != nil {
		snapshot, err := copyContainerProjects(h.containerProjectLookup())
		if err != nil {
			return map[string]ContainerProjectConfig{}
		}
		return snapshot
	}
	return h.containerProjects
}
func (h *HTTP) containerHTTP(w http.ResponseWriter, r *http.Request, token string) {
	if !h.containerHosting {
		h.storeError(w, ErrContainerUnavailable)
		return
	}
	assignments := h.containerProjectSnapshot()
	if r.Method == "GET" && r.URL.Path == "/api/container" {
		project := r.URL.Query().Get("project")
		releases, err := h.store.ContainerReleases(r.Context(), token, project)
		if err != nil {
			h.storeError(w, err)
			return
		}
		deployments, err := h.store.ContainerDeployments(r.Context(), token, project)
		if err != nil {
			h.storeError(w, err)
			return
		}
		active, err := h.store.ActiveContainerDeployment(r.Context(), token, project)
		if err != nil {
			h.storeError(w, err)
			return
		}
		site, err := h.projectSite(r.Context(), token, project, h.publicationSiteSnapshot()[project])
		if err != nil {
			h.storeError(w, err)
			return
		}
		var credentials []ContainerCredential
		if h.containerCredentials != nil {
			credentials, err = h.containerCredentials.List(r.Context(), token, project)
			if err != nil {
				h.storeError(w, err)
				return
			}
		}
		_, available := assignments[project]
		httpJSON(w, map[string]any{"private_images": h.containerCredentials != nil, "credentials": credentials, "available": available, "releases": releases, "deployments": deployments, "active": active, "site": site})
		return
	}
	if r.Method != "POST" {
		httpError(w, 404, "Not found")
		return
	}
	var input struct {
		CredentialID string `json:"credential_id"`
		Label        string `json:"label"`
		Registry     string `json:"registry"`
		Username     string `json:"username"`
		Password     string `json:"password"`
		Project      string `json:"project"`
		Key          string `json:"key"`
		Reference    string `json:"reference"`
		Port         int    `json:"port"`
		HealthPath   string `json:"health_path"`
		Release      string `json:"release"`
		ID           string `json:"id"`
	}
	if !httpDecode(w, r, &input) {
		return
	}
	// Authorize before revealing assignment state or contacting a registry.
	tx, err := h.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		h.storeError(w, err)
		return
	}
	p, _, err := h.store.uploadProject(r.Context(), tx, token, input.Project, true)
	_ = tx.Rollback()
	if err != nil {
		h.storeError(w, err)
		return
	}
	if p.Kind != "container" {
		h.storeError(w, ErrInvalid)
		return
	}
	var result any = map[string]bool{"ok": true}
	switch r.URL.Path {
	case "/api/container/credentials":
		if h.containerCredentials == nil {
			httpError(w, 409, "Private registry access is not enabled.")
			return
		}
		result, err = h.containerCredentials.Create(r.Context(), token, input.Project, input.Key, input.Label, input.Registry, registryimage.Credentials{Username: input.Username, Password: input.Password})
		if errors.Is(err, ErrContainerCredentialLimit) {
			httpError(w, 409, "This website has reached the limit of 20 saved registry credentials. Contact support before adding another.")
			return
		}
	case "/api/container/releases":
		resolve := h.containerResolver
		if input.CredentialID != "" {
			if h.containerCredentials == nil {
				httpError(w, 409, "Private registry access is not enabled.")
				return
			}
			resolve = func(ctx context.Context, project, reference string) (registryimage.Candidate, error) {
				credentials, e := h.containerCredentials.Resolve(ctx, project, input.CredentialID, reference)
				if e != nil {
					return registryimage.Candidate{}, ErrInvalid
				}
				reader := h.containerRegistryResolver
				if reader == nil {
					reader = registryimage.Resolve
				}
				candidate, e := reader(ctx, reference, credentials)
				return candidate, containerRegistryError(e)
			}
		}
		result, err = h.store.PrepareContainerRelease(r.Context(), token, input.Project, input.Key, ContainerReleaseInput{CredentialID: input.CredentialID, Reference: input.Reference, Port: input.Port, HealthPath: input.HealthPath}, resolve)
	case "/api/container/deployments":
		assignment, available := assignments[input.Project]
		if !available {
			httpError(w, 409, "Hosting setup is not complete. Your saved image is ready to publish once setup finishes.")
			return
		}
		result, err = h.store.RequestContainerDeployment(r.Context(), token, input.Project, input.Release, input.Key, assignment.RuntimeID)
	case "/api/container/deployments/cancel":
		err = h.store.CancelContainerDeployment(r.Context(), token, input.Project, input.ID)
	default:
		httpError(w, 404, "Not found")
		return
	}
	if err != nil {
		switch {
		case errors.Is(err, ErrContainerCredentialInvalid):
			httpError(w, 400, "Use a label, Docker Hub or GHCR, a registry username and a valid access token.")
		case errors.Is(err, ErrQuota):
			httpError(w, 409, "This website has reached the limit of 50 saved image releases. Contact support before adding another.")
		case errors.Is(err, ErrContainerRegistry):
			httpError(w, 502, "Could not check this image. Confirm the tag exists and the selected registry access can read it, then retry.")
		case errors.Is(err, registryimage.ErrReference):
			httpError(w, 400, "Use a Docker Hub or GHCR image with an explicit tag or SHA-256 digest.")
		case errors.Is(err, registryimage.ErrPlatform):
			httpError(w, 400, "This image needs one Linux ARM64 version for our hosting nodes.")
		case errors.Is(err, registryimage.ErrSize):
			httpError(w, 400, "This image exceeds the 512 MiB compressed layer or metadata limits.")
		case errors.Is(err, registryimage.ErrManifest):
			httpError(w, 400, "The registry returned unsupported or invalid image metadata.")
		default:
			h.storeError(w, err)
		}
		return
	}
	httpJSON(w, result)
}

func publicContainerResolver(ctx context.Context, project, reference string) (registryimage.Candidate, error) {
	candidate, err := registryimage.Resolve(ctx, reference, registryimage.Credentials{})
	return candidate, containerRegistryError(err)
}
func containerRegistryError(err error) error {
	if err != nil && !errors.Is(err, registryimage.ErrReference) && !errors.Is(err, registryimage.ErrPlatform) && !errors.Is(err, registryimage.ErrSize) && !errors.Is(err, registryimage.ErrManifest) {
		return ErrContainerRegistry
	}
	return err
}

var ErrContainerRegistry = errors.New("registry image could not be checked")
