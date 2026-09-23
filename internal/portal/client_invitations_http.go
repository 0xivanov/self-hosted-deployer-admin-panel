package portal

import (
	"errors"
	"net/http"
)

func (h *HTTP) clientInvitationsHTTP(w http.ResponseWriter, r *http.Request, session string) {
	fail := func(err error) {
		switch {
		case errors.Is(err, ErrClientSignupApproval):
			httpError(w, 403, "This person needs approval for private-launch registration. Ask the operator to approve their email before inviting them.")
		case errors.Is(err, ErrClientInviteRate):
			httpError(w, 429, "Wait at least a minute before inviting the same person again. Each website supports 20 pending invitations and 50 sends per day.")
		case errors.Is(err, ErrClientLimit):
			httpError(w, 409, "This website already has 20 clients. Ask its owner to remove access before adding another.")
		case errors.Is(err, ErrExists):
			httpError(w, 409, "This person already has access. Workspace membership is managed in Team.")
		case errors.Is(err, ErrDenied):
			httpError(w, 403, "This invitation or website is unavailable to your account. Check the invited email, or ask the owner for a new invitation.")
		default:
			h.storeError(w, err)
		}
	}
	if r.URL.Path == "/api/client-invitations" && r.Method == "GET" {
		invites, err := h.store.ClientInvitations(r.Context(), session, r.URL.Query().Get("project"))
		if err != nil {
			fail(err)
			return
		}
		httpJSON(w, map[string]any{"invitations": invites})
		return
	}
	if r.Method != "POST" {
		httpError(w, 405, "Use POST")
		return
	}
	switch r.URL.Path {
	case "/api/client-invitations":
		if h.mail == nil {
			httpError(w, 503, "Client invitation email is not configured")
			return
		}
		var input struct {
			Project string `json:"project"`
			Email   string `json:"email"`
		}
		if !httpDecode(w, r, &input) {
			return
		}
		allowed := func(email string) bool { return h.signup && (h.signupAllowed == nil || h.signupAllowed(email)) }
		invite, err := h.mail.InviteClient(r.Context(), session, input.Project, input.Email, allowed)
		if err != nil {
			fail(err)
			return
		}
		httpJSON(w, invite)
	case "/api/client-invitations/revoke":
		var input struct {
			Project string `json:"project"`
			ID      string `json:"id"`
		}
		if !httpDecode(w, r, &input) {
			return
		}
		if err := h.store.RevokeClientInvitation(r.Context(), session, input.Project, input.ID); err != nil {
			fail(err)
			return
		}
		httpJSON(w, map[string]bool{"ok": true})
	case "/api/client-invitations/accept":
		var input struct {
			Token string `json:"token"`
		}
		if !httpDecode(w, r, &input) {
			return
		}
		project, err := h.store.AcceptClientInvitation(r.Context(), session, input.Token)
		if err != nil {
			fail(err)
			return
		}
		httpJSON(w, map[string]string{"project": project})
	default:
		httpError(w, 404, "Not found")
	}
}
