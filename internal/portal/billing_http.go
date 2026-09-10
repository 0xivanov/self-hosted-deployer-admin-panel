package portal

import (
	"errors"
	"net/http"
)

// billingHTTP is reached only after TLS/host, session, origin and CSRF checks.
// This is an opt-in test interface. Provider acknowledgements and webhook intake
// are deliberately absent from the browser API.
func (h *HTTP) billingHTTP(w http.ResponseWriter, r *http.Request, token string) {
	if !h.testBilling {
		httpError(w, 404, "Billing is not enabled")
		return
	}
	fail := func(err error) {
		if errors.Is(err, ErrBillingConflict) {
			httpError(w, 409, "Billing request needs reconciliation before continuing")
		} else {
			h.storeError(w, err)
		}
	}
	switch {
	case r.URL.Path == "/api/billing/plans" && r.Method == "GET":
		plans, err := h.store.AvailableBillingPlans(r.Context(), token, r.URL.Query().Get("workspace"))
		if err != nil {
			fail(err)
			return
		}
		httpJSON(w, map[string]any{"test_mode": true, "plans": plans})
	case r.URL.Path == "/api/billing/customer" && r.Method == "POST":
		var input struct {
			Workspace string `json:"workspace"`
		}
		if !httpDecode(w, r, &input) {
			return
		}
		customer, err := h.store.RequestBillingCustomer(r.Context(), token, input.Workspace)
		if err != nil {
			fail(err)
			return
		}
		httpJSON(w, customer)
	case r.URL.Path == "/api/billing/customer" && r.Method == "GET":
		customer, err := h.store.BillingCustomer(r.Context(), token, r.URL.Query().Get("workspace"))
		if err != nil {
			fail(err)
			return
		}
		httpJSON(w, customer)
	case r.URL.Path == "/api/billing/checkout" && r.Method == "POST":
		var input struct {
			Workspace string `json:"workspace"`
			Plan      string `json:"plan"`
		}
		if !httpDecode(w, r, &input) {
			return
		}
		checkout, err := h.store.RequestBillingCheckout(r.Context(), token, input.Workspace, input.Plan)
		if err != nil {
			fail(err)
			return
		}
		httpJSON(w, checkout)
	case r.URL.Path == "/api/billing/checkout" && r.Method == "GET":
		checkout, err := h.store.BillingCheckout(r.Context(), token, r.URL.Query().Get("workspace"), r.URL.Query().Get("id"))
		if err != nil {
			fail(err)
			return
		}
		httpJSON(w, checkout)
	case r.URL.Path == "/api/billing/subscription" && r.Method == "GET":
		snapshot, err := h.store.BillingSubscriptionSnapshot(r.Context(), token, r.URL.Query().Get("workspace"), r.URL.Query().Get("id"))
		if err != nil {
			fail(err)
			return
		}
		httpJSON(w, map[string]any{"test_mode": true, "observation": snapshot})
	default:
		httpError(w, 404, "Not found")
	}
}
