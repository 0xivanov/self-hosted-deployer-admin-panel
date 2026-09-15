package portal

import (
	"errors"
	"net/http"
	"slices"
)

func (h *HTTP) merchantHTTP(w http.ResponseWriter, r *http.Request, token string) {
	if h.merchant == nil {
		httpError(w, 404, "Merchant setup is unavailable")
		return
	}
	fail := func(err error) {
		if errors.Is(err, ErrMerchantRateLimited) {
			w.Header().Set("Retry-After", "60")
			httpError(w, 429, "Wait a minute before requesting another merchant update")
		} else if errors.Is(err, ErrDenied) || errors.Is(err, ErrInvalid) {
			h.storeError(w, err)
		} else if errors.Is(err, ErrBillingConflict) {
			httpError(w, 409, "Merchant request cannot proceed. Refresh status; submitted account setup requires operator reconciliation.")
		} else {
			httpError(w, 503, "Merchant service unavailable. Refresh status before retrying.")
		}
	}
	if r.URL.Path == "/api/merchant/orders" {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			httpError(w, 405, "Method not allowed")
			return
		}
		orders, err := h.store.MerchantOrders(r.Context(), token, r.URL.Query().Get("workspace"))
		if err != nil {
			fail(err)
			return
		}
		httpJSON(w, map[string]any{"orders": orders})
		return
	}
	if r.URL.Path == "/api/merchant/products" {
		if r.Method == "GET" {
			products, err := h.store.MerchantProducts(r.Context(), token, r.URL.Query().Get("workspace"))
			if err != nil {
				fail(err)
				return
			}
			httpJSON(w, map[string]any{"products": products})
			return
		}
		if r.Method == "POST" {
			var input MerchantProductInput
			if !httpDecode(w, r, &input) {
				return
			}
			product, err := h.store.SaveMerchantProduct(r.Context(), token, input)
			if errors.Is(err, ErrMerchantProductConflict) {
				httpError(w, 409, "Product changed, request conflicts, or the 100-product limit was reached. Refresh the catalog before retrying.")
				return
			}
			if err != nil {
				fail(err)
				return
			}
			httpJSON(w, product)
			return
		}
		httpError(w, 405, "Method not allowed")
		return
	}
	if r.Method == "GET" && r.URL.Path == "/api/merchant/account" {
		status, err := h.store.MerchantStatus(r.Context(), token, r.URL.Query().Get("workspace"))
		if err != nil {
			fail(err)
			return
		}
		httpJSON(w, status)
		return
	}
	if r.Method != "POST" || (r.URL.Path != "/api/merchant/account" && r.URL.Path != "/api/merchant/onboarding" && r.URL.Path != "/api/merchant/refresh") {
		httpError(w, 404, "Not found")
		return
	}
	var input struct {
		Workspace string `json:"workspace"`
		Country   string `json:"country"`
	}
	if !httpDecode(w, r, &input) {
		return
	}
	if r.URL.Path != "/api/merchant/account" && input.Country != "" {
		httpError(w, 400, "Country is only accepted for initial setup")
		return
	}
	switch r.URL.Path {
	case "/api/merchant/account":
		if !slices.Contains(h.merchantCountries, input.Country) {
			httpError(w, 400, "Select a configured merchant country")
			return
		}
		intent, err := h.store.RequestMerchantAccount(r.Context(), token, input.Workspace, input.Country)
		if err != nil {
			fail(err)
			return
		}
		if _, err = h.store.DispatchMerchantAccount(r.Context(), intent.RequestID, h.merchant); err != nil {
			fail(err)
			return
		}
	case "/api/merchant/onboarding":
		link, err := h.store.MerchantOnboardingLink(r.Context(), token, input.Workspace, h.merchant)
		if err != nil {
			fail(err)
			return
		}
		httpJSON(w, map[string]any{"url": link.URL, "expires_at": link.ExpiresAt})
		return
	case "/api/merchant/refresh":
		if err := h.store.RefreshMerchantAccount(r.Context(), token, input.Workspace, h.merchant); err != nil {
			fail(err)
			return
		}
	}
	status, err := h.store.MerchantStatus(r.Context(), token, input.Workspace)
	if err != nil {
		fail(err)
		return
	}
	httpJSON(w, status)
}
