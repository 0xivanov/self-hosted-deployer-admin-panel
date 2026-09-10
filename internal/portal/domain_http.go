package portal

import (
	"errors"
	"net/http"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/domains"
)

// Limit provider lookups by authenticated account, across sessions and workspaces.
// The bounded in-memory map is separate from login limits; restarting resets it.
func (h *HTTP) allowDomainQuote(account string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.store.now()
	for key, v := range h.domainAttempts {
		if !now.Before(v.start.Add(time.Minute)) {
			delete(h.domainAttempts, key)
		}
	}
	v, exists := h.domainAttempts[account]
	if !exists {
		if len(h.domainAttempts) >= 4096 {
			return false
		}
		v.start = now
	}
	if v.count >= 5 {
		return false
	}
	v.count++
	h.domainAttempts[account] = v
	return true
}

// Reached after host/TLS, session, origin and CSRF checks. No purchase route exists.
func (h *HTTP) domainHTTP(w http.ResponseWriter, r *http.Request, token, account string) {
	if h.domainQuotes == nil || r.URL.Path != "/api/domains/quote" || r.URL.EscapedPath() != r.URL.Path {
		httpError(w, 404, "Not found")
		return
	}
	fail := func(err error) {
		switch {
		case errors.Is(err, domains.ErrDomain):
			httpError(w, 400, "Enter a supported domain name")
		case errors.Is(err, domains.ErrQuote):
			httpError(w, 409, "A current standard-price quote is unavailable")
		case errors.Is(err, ErrDomainQuoteLimit):
			httpError(w, 409, "Saved domain quote limit reached")
		case errors.Is(err, ErrDenied):
			h.storeError(w, err)
		default:
			httpError(w, 503, "Domain quotes are temporarily unavailable")
		}
	}
	if r.Method == "POST" {
		if r.URL.RawQuery != "" || r.URL.ForceQuery {
			httpError(w, 400, "Unexpected query parameters")
			return
		}
		var input struct {
			Workspace string `json:"workspace"`
			Domain    string `json:"domain"`
		}
		if !httpDecode(w, r, &input) {
			return
		}
		if !h.allowDomainQuote(account) {
			w.Header().Set("Retry-After", "60")
			httpError(w, 429, "Retry domain quotes in a minute")
			return
		}
		quote, err := h.store.RequestDomainQuote(r.Context(), h.domainQuotes, token, input.Workspace, input.Domain, h.domainMarkupMinor)
		if err != nil {
			fail(err)
			return
		}
		httpJSON(w, quote)
		return
	}
	if r.Method == "GET" {
		quote, err := h.store.DomainQuote(r.Context(), token, r.URL.Query().Get("workspace"), r.URL.Query().Get("id"))
		if err != nil {
			fail(err)
			return
		}
		httpJSON(w, quote)
		return
	}
	httpError(w, 405, "Method not allowed")
}
