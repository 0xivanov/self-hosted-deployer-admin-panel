package portal

import "net/http"

func (h *HTTP) domainPurchaseHTTP(w http.ResponseWriter, r *http.Request, token, account string) {
	if h.domainPurchases == nil || r.URL.EscapedPath() != r.URL.Path {
		httpError(w, 404, "Not found")
		return
	}
	if r.Method == "GET" && r.URL.Path == "/api/domains/purchases" {
		purchases, err := h.domainPurchases.List(r.Context(), token, r.URL.Query().Get("workspace"))
		if err != nil {
			h.storeError(w, err)
			return
		}
		httpJSON(w, map[string]any{"purchases": purchases})
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
		ID        string `json:"id"`
		Project   string `json:"project"`
	}
	if !httpDecode(w, r, &input) {
		return
	}
	if !validMerchantOrderToken(input.ID) {
		httpError(w, 400, "Invalid order")
		return
	}
	if !h.allowDomainQuote(account) {
		httpError(w, 429, "Retry domain requests in a minute")
		return
	}
	var p DomainPurchase
	var err error
	switch r.URL.Path {
	case "/api/domains/checkout":
		p, err = h.domainPurchases.Start(r.Context(), token, input.Workspace, input.ID, input.Project)
	case "/api/domains/sync":
		p, err = h.domainPurchases.Sync(r.Context(), token, input.Workspace, input.ID)
	default:
		httpError(w, 404, "Not found")
		return
	}
	if err != nil {
		httpError(w, 409, "Unable to start this order. Check website access and request a fresh quote if the price changed.")
		return
	}
	httpJSON(w, p)
}
