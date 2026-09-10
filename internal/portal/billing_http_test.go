//go:build integration

package portal

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestBillingHTTPAuthorizationAndRequestIdentity(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)
	owner, session := verifiedAccount(t, s, "billing-http@example.test")
	other, _ := verifiedAccount(t, s, "billing-foreign@example.test")
	h, err := NewHTTP(s, HTTPOptions{Origin: "https://portal.example.test", TestBilling: true})
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: h.cookie, Value: session.Token}
	csrf := csrfFor(session.Token)
	body := `{"workspace":"` + owner.WorkspaceID + `"}`
	for _, tc := range []struct {
		name, body, origin, csrf string
		cookie                   *http.Cookie
		code                     int
	}{
		{"anonymous", body, h.origin, csrf, nil, 401},
		{"missing csrf", body, h.origin, "", cookie, 403},
		{"foreign origin", body, "https://foreign.test", csrf, cookie, 403},
		{"foreign workspace", `{"workspace":"` + other.WorkspaceID + `"}`, h.origin, csrf, cookie, 403},
		{"injected customer", `{"workspace":"` + owner.WorkspaceID + `","customer_id":"cus_injected"}`, h.origin, csrf, cookie, 400},
		{"owner", body, h.origin, csrf, cookie, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := portalRequest(h, "POST", "/api/billing/customer", tc.body, tc.origin, tc.csrf, tc.cookie)
			if w.Code != tc.code {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
	request, err := s.RequestBillingCustomer(t.Context(), session.Token, owner.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	w := portalRequest(h, "GET", "/api/billing/customer?workspace="+owner.WorkspaceID, "", "", "", cookie)
	if w.Code != 200 || strings.Contains(w.Body.String(), request.RequestID) || strings.Contains(w.Body.String(), owner.ID) {
		t.Fatal("private worker identity exposed", w.Code, w.Body.String())
	}
	if err = s.BindBillingCustomer(t.Context(), request.RequestID, "cus_http"); err != nil {
		t.Fatal(err)
	}
	if err = s.ConfigureBillingPlan(t.Context(), "starter", "price_http", true); err != nil {
		t.Fatal(err)
	}
	if err = s.ConfigureBillingPlan(t.Context(), "disabled", "price_hidden", false); err != nil {
		t.Fatal(err)
	}
	w = portalRequest(h, "GET", "/api/billing/plans?workspace="+owner.WorkspaceID, "", "", "", cookie)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "starter") || strings.Contains(w.Body.String(), "disabled") || strings.Contains(w.Body.String(), "price_") {
		t.Fatal("unsafe plan catalog", w.Code, w.Body.String())
	}
	w = portalRequest(h, "POST", "/api/billing/checkout", `{"workspace":"`+owner.WorkspaceID+`","plan":"starter"}`, h.origin, csrf, cookie)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var checkout BillingCheckout
	if err = json.Unmarshal(w.Body.Bytes(), &checkout); err != nil {
		t.Fatal(err)
	}
	if checkout.State != "pending" || checkout.CustomerID != "" || checkout.PriceID != "" || checkout.PlanID != "starter" {
		t.Fatal(checkout)
	}
	w = portalRequest(h, "POST", "/api/billing/checkout", `{"workspace":"`+owner.WorkspaceID+`","plan":"starter","price":"price_cheap"}`, h.origin, csrf, cookie)
	if w.Code != 400 {
		t.Fatal("client price accepted", w.Code)
	}
	foreignCookie, foreignCSRF := httpLogin(t, h, other.Email)
	if _, err = s.db.Exec("INSERT INTO memberships VALUES(?,?,'developer')", other.ID, owner.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/billing/offers?workspace=" + owner.WorkspaceID, "/api/billing/plans?workspace=" + owner.WorkspaceID, "/api/billing/customer?workspace=" + owner.WorkspaceID, "/api/billing/checkout?workspace=" + owner.WorkspaceID + "&id=" + checkout.ID, "/api/billing/subscription?workspace=" + owner.WorkspaceID + "&id=sub_missing"} {
		w = portalRequest(h, "GET", path, "", "", "", foreignCookie)
		if w.Code != 403 {
			t.Fatal("developer read", path, w.Code)
		}
	}
	w = portalRequest(h, "POST", "/api/billing/checkout", `{"workspace":"`+owner.WorkspaceID+`","plan":"starter"}`, h.origin, foreignCSRF, foreignCookie)
	if w.Code != 403 {
		t.Fatal("developer purchase", w.Code)
	}
	disabled, err := NewHTTP(s, HTTPOptions{Origin: h.origin})
	if err != nil {
		t.Fatal(err)
	}
	w = portalRequest(disabled, "POST", "/api/billing/customer", body, h.origin, csrf, cookie)
	if w.Code != 404 {
		t.Fatal("billing enabled by default", w.Code)
	}
}
