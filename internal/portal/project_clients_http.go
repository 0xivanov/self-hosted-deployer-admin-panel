package portal

import (
	"errors"
	"net/http"
)

func (h *HTTP) projectClientsHTTP(w http.ResponseWriter, r *http.Request, token string) {
	if r.Method == "GET" && r.URL.Path == "/api/shared-websites" {
		sites, err := h.store.SharedWebsites(r.Context(), token)
		if err != nil {
			h.storeError(w, err)
			return
		}
		httpJSON(w, map[string]any{"websites": sites})
		return
	}
	if r.URL.Path != "/api/project-clients" {
		httpError(w, 404, "Not found")
		return
	}
	if r.Method == "GET" {
		clients, err := h.store.ProjectClients(r.Context(), token, r.URL.Query().Get("project"))
		if err != nil {
			h.storeError(w, err)
			return
		}
		history, err := h.store.ClientAccessHistory(r.Context(), token, r.URL.Query().Get("project"))
		if err != nil {
			h.storeError(w, err)
			return
		}
		httpJSON(w, map[string]any{"clients": clients, "history": history})
		return
	}
	if r.Method != "POST" {
		httpError(w, 405, "Use GET or POST")
		return
	}
	var input struct {
		Project string `json:"project"`
		Email   string `json:"email"`
		Grant   *bool  `json:"grant"`
	}
	if !httpDecode(w, r, &input) {
		return
	}
	if input.Grant == nil {
		httpError(w, 400, "Choose grant or revoke")
		return
	}
	if err := h.store.ChangeProjectClient(r.Context(), token, input.Project, input.Email, *input.Grant); err != nil {
		if errors.Is(err, ErrClientUnavailable) {
			httpError(w, 409, "Use a verified account without existing access to this workspace. Manage workspace members in Team.")
		} else if errors.Is(err, ErrClientLimit) {
			httpError(w, 409, "This website already has 20 clients. Remove access before adding another.")
		} else {
			h.storeError(w, err)
		}
		return
	}
	httpJSON(w, map[string]bool{"ok": true})
}
