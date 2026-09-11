package portal

import (
	"encoding/hex"
	"errors"
	"net/http"
)

// NodeProjectConfig is operator-selected. Browsers cannot choose a runtime or
// toolchain. Configure a project only when its build/deployment workers exist.
type NodeProjectConfig struct {
	Build     NodeBuildAssignment `json:"build"`
	RuntimeID string              `json:"runtime_id"`
}

func copyNodeProjects(input map[string]NodeProjectConfig) (map[string]NodeProjectConfig, error) {
	output := map[string]NodeProjectConfig{}
	seen := map[string]bool{}
	valid := func(s string) bool {
		v, e := hex.DecodeString(s)
		return e == nil && len(v) == 32 && hex.EncodeToString(v) == s
	}
	for project, assignment := range input {
		if !valid(project) || !valid(assignment.RuntimeID) || !valid(assignment.Build.ToolchainSHA256) || (assignment.Build.Settings.Architecture != "arm64" && assignment.Build.Settings.Architecture != "amd64") || seen[assignment.RuntimeID] {
			return nil, errors.New("invalid or shared Node runtime assignment")
		}
		seen[assignment.RuntimeID] = true
		output[project] = assignment
	}
	return output, nil
}
func (h *HTTP) nodeHTTP(w http.ResponseWriter, r *http.Request, token string) {
	if r.Method == "GET" && r.URL.Path == "/api/node" {
		project := r.URL.Query().Get("project")
		p, err := h.store.GetProject(r.Context(), token, project)
		if err != nil {
			h.storeError(w, err)
			return
		}
		if p.Kind != "node" {
			httpError(w, 400, "Choose a Node.js project")
			return
		}
		builds, err := h.store.NodeBuilds(r.Context(), token, project)
		if err != nil {
			h.storeError(w, err)
			return
		}
		releases, err := h.store.NodeReleases(r.Context(), token, project)
		if err != nil {
			h.storeError(w, err)
			return
		}
		deployments, err := h.store.NodeDeployments(r.Context(), token, project)
		if err != nil {
			h.storeError(w, err)
			return
		}
		active, err := h.store.ActiveNodeDeployment(r.Context(), token, project)
		if err != nil {
			h.storeError(w, err)
			return
		}
		// Present customer status, not internal build commands or toolchain pins.
		type buildView struct {
			ID        string `json:"id"`
			UploadID  string `json:"upload_id"`
			State     string `json:"state"`
			CreatedAt int64  `json:"created_at"`
		}
		views := make([]buildView, 0, len(builds))
		for _, b := range builds {
			views = append(views, buildView{b.ID, b.UploadID, b.State, b.CreatedAt})
		}
		_, available := h.nodeProjects[project]
		httpJSON(w, map[string]any{"available": available, "builds": views, "releases": releases, "deployments": deployments, "active": active, "site": h.publicationSites[project]})
		return
	}
	if r.Method != "POST" {
		httpError(w, 404, "Not found")
		return
	}
	var input struct {
		Project string `json:"project"`
		Upload  string `json:"upload"`
		Release string `json:"release"`
		Key     string `json:"key"`
		ID      string `json:"id"`
	}
	if !httpDecode(w, r, &input) {
		return
	}
	p, err := h.store.GetProject(r.Context(), token, input.Project)
	if err != nil {
		h.storeError(w, err)
		return
	}
	if p.Kind != "node" {
		httpError(w, 400, "Choose a Node.js project")
		return
	}
	assignment, available := h.nodeProjects[input.Project]
	if (r.URL.Path == "/api/node/builds" || r.URL.Path == "/api/node/deployments") && !available {
		httpError(w, 403, "Hosting is not enabled for this project yet")
		return
	}
	var result any = map[string]bool{"ok": true}
	switch r.URL.Path {
	case "/api/node/builds":
		result, err = h.store.RequestNodeBuild(r.Context(), token, input.Project, input.Upload, input.Key, assignment.Build)
	case "/api/node/builds/cancel":
		err = h.store.CancelNodeBuild(r.Context(), token, input.Project, input.ID)
	case "/api/node/deployments":
		result, err = h.store.RequestNodeDeployment(r.Context(), token, input.Project, input.Release, input.Key, assignment.RuntimeID)
	case "/api/node/deployments/cancel":
		err = h.store.CancelNodeDeployment(r.Context(), token, input.Project, input.ID)
	case "/api/node/releases/delete":
		err = h.store.DeleteNodeRelease(r.Context(), token, input.Project, input.ID)
	default:
		httpError(w, 404, "Not found")
		return
	}
	if err != nil {
		switch {
		case errors.Is(err, ErrBuildConflict), errors.Is(err, ErrPublishing):
			httpError(w, 409, "An operation is already pending or cannot be cancelled. Refresh its status.")
		case errors.Is(err, ErrBuildQuota):
			httpError(w, 409, "Saved build or release limit reached. Remove unused releases first.")
		default:
			h.storeError(w, err)
		}
		return
	}
	httpJSON(w, result)
}
