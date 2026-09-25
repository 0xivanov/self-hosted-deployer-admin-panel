package portal

import (
	"crypto/subtle"
	"database/sql"
	"errors"
	"net"
	"net/http"
	"time"
)

const merchantBuyerCookie = "__Host-merchant-buyer"

func (h *HTTP) merchantBuyerCookieName() string {
	if h.store.merchantModeValue() == "live" {
		return merchantBuyerCookie + "-live"
	}
	return merchantBuyerCookie
}

type shopProduct struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Currency    string `json:"currency"`
	AmountMinor int64  `json:"amount_minor"`
	Revision    int64  `json:"revision"`
}

type shopRefund struct {
	State       string `json:"state"`
	Currency    string `json:"currency"`
	AmountMinor int64  `json:"amount_minor"`
	ObservedAt  int64  `json:"observed_at"`
}

type shopOrder struct {
	FulfilledAt   int64       `json:"fulfilled_at"`
	Refund        *shopRefund `json:"refund,omitempty"`
	ID            string      `json:"id"`
	Name          string      `json:"name"`
	Currency      string      `json:"currency"`
	AmountMinor   int64       `json:"amount_minor"`
	State         string      `json:"state"`
	PaymentStatus string      `json:"payment_status"`
	CreatedAt     int64       `json:"created_at"`
	ObservedAt    int64       `json:"observed_at"`
	URL           string      `json:"url,omitempty"`
}

func (h *HTTP) allowShop(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address
	}
	now := time.Now()
	h.mu.Lock()
	defer h.mu.Unlock()
	for key, value := range h.shopAttempts {
		if now.Sub(value.start) >= time.Minute {
			delete(h.shopAttempts, key)
		}
	}
	w, exists := h.shopAttempts[host]
	if !exists {
		if len(h.shopAttempts) >= 4096 {
			return false
		}
		w.start = now
	}
	if w.count >= 30 {
		return false
	}
	w.count++
	h.shopAttempts[host] = w
	return true
}

func (h *HTTP) shopProduct(r *http.Request) (shopProduct, error) {
	id := r.URL.Query().Get("product")
	if !validMerchantOrderToken(id) {
		return shopProduct{}, ErrInvalid
	}
	var product shopProduct
	err := h.store.db.QueryRowContext(r.Context(), "SELECT id,name,currency,amount_minor,revision FROM merchant_products WHERE mode=? AND id=? AND active=1", h.store.merchantModeValue(), id).Scan(&product.ID, &product.Name, &product.Currency, &product.AmountMinor, &product.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return shopProduct{}, ErrDenied
	}
	return product, err
}

func shopOrderView(order MerchantOrder) shopOrder {
	view := shopOrder{FulfilledAt: order.FulfilledAt, ID: order.ID, Name: order.Name, Currency: order.Currency, AmountMinor: order.AmountMinor, State: order.State, PaymentStatus: order.PaymentStatus, CreatedAt: order.CreatedAt, ObservedAt: order.ObservedAt}
	if order.State == "open" {
		view.URL = order.CheckoutURL
	}
	return view
}

func (h *HTTP) shopBuyer(r *http.Request) (string, error) {
	cookie, err := r.Cookie(h.merchantBuyerCookieName())
	if err != nil || !validMerchantOrderToken(cookie.Value) {
		return "", ErrDenied
	}
	if err = h.store.AuthenticateMerchantBuyer(r.Context(), cookie.Value); err != nil {
		return "", err
	}
	return cookie.Value, nil
}

func (h *HTTP) shopCSRF(r *http.Request, token string) bool {
	return subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(csrfFor(token))) == 1
}

func (h *HTTP) shopHTTP(w http.ResponseWriter, r *http.Request) {
	if h.merchant == nil {
		httpError(w, http.StatusNotFound, "Not found")
		return
	}
	if _, ok := h.merchant.(MerchantCheckoutProvider); !ok {
		httpError(w, http.StatusNotFound, "Not found")
		return
	}
	if !h.allowShop(r.RemoteAddr) {
		w.Header().Set("Retry-After", "60")
		httpError(w, http.StatusTooManyRequests, "Please retry later")
		return
	}
	switch r.URL.Path {
	case "/api/shop/recovery-code":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			httpError(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		h.shopIssueRecoveryCode(w, r)
		return
	case "/api/shop/recover":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			httpError(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		h.shopRecoverOrder(w, r)
		return
	case "/api/shop/product":
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			httpError(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		product, err := h.shopProduct(r)
		if errors.Is(err, ErrInvalid) {
			httpError(w, http.StatusBadRequest, "Invalid product")
			return
		}
		if errors.Is(err, ErrDenied) {
			httpError(w, http.StatusNotFound, "Not found")
			return
		}
		if err != nil {
			httpError(w, http.StatusServiceUnavailable, "Shop unavailable")
			return
		}
		httpJSON(w, product)
		return
	case "/api/shop/session":
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			httpError(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		token, err := h.shopBuyer(r)
		if err != nil {
			if !errors.Is(err, ErrDenied) {
				httpError(w, 503, "Shop session unavailable")
				return
			}
			session, err := h.store.CreateMerchantBuyerSession(r.Context())
			if err != nil {
				httpError(w, 503, "Shop session unavailable")
				return
			}
			token = session.Token
			http.SetCookie(w, &http.Cookie{Name: h.merchantBuyerCookieName(), Value: token, Path: "/", MaxAge: 30 * 24 * 60 * 60, Expires: session.ExpiresAt, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
		}
		httpJSON(w, map[string]string{"csrf": csrfFor(token)})
		return
	case "/api/shop/logout":
		if r.Method != "POST" {
			httpError(w, 405, "Method not allowed")
			return
		}
		token, err := h.shopBuyer(r)
		if err != nil {
			httpError(w, 401, "Shop session required")
			return
		}
		if !h.shopCSRF(r, token) {
			httpError(w, 403, "Reload the shop and retry")
			return
		}
		if err = h.store.RevokeMerchantBuyer(r.Context(), token); err != nil {
			httpError(w, 503, "Shop session unavailable")
			return
		}
		http.SetCookie(w, &http.Cookie{Name: h.merchantBuyerCookieName(), Value: "", Path: "/", MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
		w.WriteHeader(204)
		return
	case "/api/shop/orders":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			httpError(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		h.shopCreateOrder(w, r)
		return
	case "/api/shop/order":
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			httpError(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		h.shopGetOrder(w, r)
		return
	case "/api/shop/refresh":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			httpError(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		h.shopRefreshOrder(w, r)
		return
	default:
		httpError(w, http.StatusNotFound, "Not found")
		return
	}
}

func (h *HTTP) shopIssueRecoveryCode(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		httpError(w, http.StatusBadRequest, "Invalid request")
		return
	}
	token, err := h.shopBuyer(r)
	if err != nil {
		httpError(w, http.StatusUnauthorized, "Shop session required")
		return
	}
	if !h.shopCSRF(r, token) {
		httpError(w, http.StatusForbidden, "Reload the shop and retry")
		return
	}
	var input struct {
		Order string `json:"order"`
	}
	if !httpDecode(w, r, &input) {
		return
	}
	recovery, err := h.store.IssueMerchantOrderRecoveryCode(r.Context(), token, input.Order)
	if errors.Is(err, ErrInvalid) {
		httpError(w, http.StatusBadRequest, "Invalid order")
		return
	}
	if errors.Is(err, ErrDenied) {
		httpError(w, http.StatusNotFound, "Not found")
		return
	}
	if err != nil {
		httpError(w, http.StatusServiceUnavailable, "Order service unavailable")
		return
	}
	httpJSON(w, map[string]any{"code": recovery.Code, "expires_at": recovery.ExpiresAt.Unix()})
}

func (h *HTTP) shopRecoverOrder(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		httpError(w, http.StatusBadRequest, "Invalid request")
		return
	}
	token, err := h.shopBuyer(r)
	if err != nil {
		httpError(w, http.StatusUnauthorized, "Shop session required")
		return
	}
	if !h.shopCSRF(r, token) {
		httpError(w, http.StatusForbidden, "Reload the shop and retry")
		return
	}
	var input struct {
		Code string `json:"code"`
	}
	if !httpDecode(w, r, &input) {
		return
	}
	order, err := h.store.RedeemMerchantOrderRecoveryCode(r.Context(), token, input.Code)
	if errors.Is(err, ErrInvalid) || errors.Is(err, ErrDenied) {
		httpError(w, http.StatusNotFound, "Recovery code is invalid, expired or unavailable.")
		return
	}
	if err != nil {
		httpError(w, http.StatusServiceUnavailable, "Order service unavailable")
		return
	}
	h.writeShopOrder(w, r, order)
}

func (h *HTTP) shopCreateOrder(w http.ResponseWriter, r *http.Request) {
	token, err := h.shopBuyer(r)
	if err != nil {
		httpError(w, http.StatusUnauthorized, "Shop session required")
		return
	}
	if !h.shopCSRF(r, token) {
		httpError(w, http.StatusForbidden, "Reload the shop and retry")
		return
	}
	var input struct {
		Product  string `json:"product"`
		Revision int64  `json:"revision"`
		Key      string `json:"key"`
	}
	if !httpDecode(w, r, &input) {
		return
	}
	if !validMerchantOrderToken(input.Product) {
		httpError(w, 400, "Invalid product")
		return
	}
	order, err := h.store.RequestMerchantOrder(r.Context(), token, input.Product, input.Revision, input.Key)
	if err != nil {
		if errors.Is(err, ErrInvalid) {
			httpError(w, http.StatusBadRequest, "Invalid order")
			return
		}
		if errors.Is(err, ErrDenied) {
			httpError(w, http.StatusForbidden, "Order unavailable")
			return
		}
		httpError(w, http.StatusConflict, "Order request conflicts")
		return
	}
	if order.State == "requested" {
		provider, _ := h.merchant.(MerchantCheckoutProvider)
		if _, dispatchErr := h.store.DispatchMerchantOrder(r.Context(), order.ID, provider); dispatchErr != nil {
			saved, readErr := h.store.BuyerMerchantOrder(r.Context(), token, order.ID)
			if readErr == nil {
				h.writeShopOrder(w, r, saved)
				return
			}
			httpError(w, http.StatusServiceUnavailable, "Order service unavailable")
			return
		}
		order, err = h.store.BuyerMerchantOrder(r.Context(), token, order.ID)
		if err != nil {
			httpError(w, http.StatusServiceUnavailable, "Order service unavailable")
			return
		}
	}
	h.writeShopOrder(w, r, order)
}

func (h *HTTP) shopGetOrder(w http.ResponseWriter, r *http.Request) {
	token, err := h.shopBuyer(r)
	if err != nil {
		httpError(w, http.StatusUnauthorized, "Shop session required")
		return
	}
	id := r.URL.Query().Get("order")
	if !validMerchantOrderToken(id) {
		httpError(w, http.StatusBadRequest, "Invalid order")
		return
	}
	order, err := h.store.BuyerMerchantOrder(r.Context(), token, id)
	if errors.Is(err, ErrDenied) {
		httpError(w, http.StatusNotFound, "Not found")
		return
	}
	if err != nil {
		httpError(w, http.StatusServiceUnavailable, "Order service unavailable")
		return
	}
	h.writeShopOrder(w, r, order)
}

func (h *HTTP) shopRefreshOrder(w http.ResponseWriter, r *http.Request) {
	token, err := h.shopBuyer(r)
	if err != nil {
		httpError(w, http.StatusUnauthorized, "Shop session required")
		return
	}
	if !h.shopCSRF(r, token) {
		httpError(w, http.StatusForbidden, "Reload the shop and retry")
		return
	}
	var input struct {
		Order string `json:"order"`
	}
	if !httpDecode(w, r, &input) {
		return
	}
	if !validMerchantOrderToken(input.Order) {
		httpError(w, 400, "Invalid order")
		return
	}
	order, err := h.store.BuyerMerchantOrder(r.Context(), token, input.Order)
	if errors.Is(err, ErrDenied) {
		httpError(w, http.StatusNotFound, "Not found")
		return
	}
	if err != nil {
		httpError(w, http.StatusServiceUnavailable, "Order service unavailable")
		return
	}
	if order.State == "requested" {
		provider, _ := h.merchant.(MerchantCheckoutProvider)
		if _, err := h.store.DispatchMerchantOrder(r.Context(), order.ID, provider); err != nil {
			httpError(w, http.StatusServiceUnavailable, "Checkout cannot proceed yet. Retry after the merchant updates availability.")
			return
		}
		order, err = h.store.BuyerMerchantOrder(r.Context(), token, order.ID)
		if err != nil {
			httpError(w, 503, "Order service unavailable")
			return
		}
		h.writeShopOrder(w, r, order)
		return
	}
	if order.SessionID != "" {
		provider, _ := h.merchant.(MerchantCheckoutProvider)
		if _, reconcileErr := h.store.ReconcileMerchantOrder(r.Context(), order.ID, order.SessionID, provider); reconcileErr != nil && !errors.Is(reconcileErr, ErrBillingConflict) {
			httpError(w, http.StatusServiceUnavailable, "Order service unavailable")
			return
		}
		order, err = h.store.BuyerMerchantOrder(r.Context(), token, order.ID)
		if err != nil {
			httpError(w, http.StatusServiceUnavailable, "Order service unavailable")
			return
		}
	}
	h.writeShopOrder(w, r, order)
}

// Only return refund evidence for the same privately authenticated buyer order.
func (h *HTTP) writeShopOrder(w http.ResponseWriter, r *http.Request, order MerchantOrder) {
	token, err := h.shopBuyer(r)
	if err != nil {
		httpError(w, 401, "Shop session required")
		return
	}
	var refund shopRefund
	mode := h.store.merchantModeValue()
	err = h.store.db.QueryRowContext(r.Context(), `SELECT f.state,f.currency,f.amount_minor,f.observed_at FROM merchant_refunds f JOIN merchant_orders o ON o.mode=f.mode AND o.id=f.order_id WHERE f.mode=? AND o.id=? AND (o.buyer_hash=? OR EXISTS (SELECT 1 FROM merchant_order_recovery_grants g JOIN merchant_buyer_sessions bs ON bs.mode=g.mode AND bs.token_hash=g.session_hash WHERE g.mode=? AND g.order_id=o.id AND g.session_hash=? AND bs.expires_at>?))`, mode, order.ID, digest(token), mode, digest(token), h.store.now().Unix()).Scan(&refund.State, &refund.Currency, &refund.AmountMinor, &refund.ObservedAt)
	view := shopOrderView(order)
	if err == nil {
		view.Refund = &refund
	} else if !errors.Is(err, sql.ErrNoRows) {
		httpError(w, 503, "Order service unavailable")
		return
	}
	httpJSON(w, view)
}
