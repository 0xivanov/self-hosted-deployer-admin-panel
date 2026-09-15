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

// Reached after host/TLS, session, origin and CSRF checks. Orders do not register domains.
func (h *HTTP) domainHTTP(w http.ResponseWriter, r *http.Request, token, account string) {
	if h.domainQuotes == nil || (r.URL.Path != "/api/domains/quote" && r.URL.Path != "/api/domains/orders" && r.URL.Path != "/api/domains/orders/cancel") || r.URL.EscapedPath() != r.URL.Path {
		httpError(w, 404, "Not found")
		return
	}
	fail := func(err error) {
		switch {
		case errors.Is(err, domains.ErrDomain):
			httpError(w, 400, "Enter a supported domain name")
		case errors.Is(err, ErrDomainOrderConflict):
			httpError(w, 409, "Quote expired, price changed, or an active order already exists. Refresh saved orders or request a new quote.")
		case errors.Is(err, ErrInvalid):
			h.storeError(w, err)
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
	if r.URL.Path != "/api/domains/quote" {
		if r.Method == "GET" && r.URL.Path == "/api/domains/orders" {
			orders, err := h.store.DomainOrders(r.Context(), token, r.URL.Query().Get("workspace"))
			if err != nil {
				fail(err)
				return
			}
			httpJSON(w, map[string]any{"orders": orders})
			return
		}
		if r.Method != "POST" {
			httpError(w, 405, "Method not allowed")
			return
		}
		if r.URL.RawQuery != "" || r.URL.ForceQuery {
			httpError(w, 400, "Unexpected query parameters")
			return
		}
		var input struct {
			Workspace string `json:"workspace"`
			Quote     string `json:"quote"`
			ID        string `json:"id"`
		}
		if !httpDecode(w, r, &input) {
			return
		}
		var order DomainOrder
		var err error
		if r.URL.Path == "/api/domains/orders" {
			if input.ID != "" || !validMerchantOrderToken(input.Quote) {
				httpError(w, 400, "Invalid domain quote")
				return
			}
			if !h.allowDomainQuote(account) {
				w.Header().Set("Retry-After", "60")
				httpError(w, 429, "Retry domain requests in a minute")
				return
			}
			order, err = h.store.RequestDomainOrder(r.Context(), h.domainQuotes, token, input.Workspace, input.Quote)
		} else {
			if input.Quote != "" || !validMerchantOrderToken(input.ID) {
				httpError(w, 400, "Invalid domain order")
				return
			}
			order, err = h.store.CancelDomainOrder(r.Context(), token, input.Workspace, input.ID)
		}
		if err != nil {
			fail(err)
			return
		}
		httpJSON(w, order)
		return
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
