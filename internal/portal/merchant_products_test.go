//go:build integration

package portal

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

func TestMerchantProductPricesRevisionsAndOwnerIsolation(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)
	ctx := t.Context()
	a, session := verifiedAccount(t, s, "products-owner@example.test")
	b, foreign := verifiedAccount(t, s, "products-other@example.test")
	active := true
	input := MerchantProductInput{Workspace: a.WorkspaceID, Key: randomToken(), Name: "Example product", Currency: "eur", AmountMinor: 1250, Active: &active}
	product, err := s.SaveMerchantProduct(ctx, session.Token, input)
	if err != nil || product.AmountMinor != 1250 || product.Revision != 1 {
		t.Fatal(product, err)
	}
	repeat, err := s.SaveMerchantProduct(ctx, session.Token, input)
	if err != nil || repeat.ID != product.ID {
		t.Fatal(repeat, err)
	}
	changed := input
	changed.AmountMinor = 1300
	if _, err = s.SaveMerchantProduct(ctx, session.Token, changed); !errors.Is(err, ErrMerchantProductConflict) {
		t.Fatal("same key changed price", err)
	}
	update := input
	update.Key = ""
	update.ID = product.ID
	update.Revision = 1
	update.AmountMinor = 1500
	disabled := false
	update.Active = &disabled
	saved, err := s.SaveMerchantProduct(ctx, session.Token, update)
	if err != nil || saved.Revision != 2 || saved.Active || saved.AmountMinor != 1500 {
		t.Fatal(saved, err)
	}
	update.AmountMinor = 1600
	if _, err = s.SaveMerchantProduct(ctx, session.Token, update); !errors.Is(err, ErrMerchantProductConflict) {
		t.Fatal("stale revision overwrite", err)
	}
	update.Workspace = b.WorkspaceID
	update.Revision = 2
	if _, err = s.SaveMerchantProduct(ctx, foreign.Token, update); !errors.Is(err, ErrDenied) {
		t.Fatal("foreign product edit", err)
	}
	if _, err = s.MerchantProducts(ctx, foreign.Token, a.WorkspaceID); !errors.Is(err, ErrDenied) {
		t.Fatal("foreign list", err)
	}
	if _, err = s.db.Exec("INSERT INTO memberships VALUES(?,?,'developer')", b.ID, a.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveMerchantProduct(ctx, foreign.Token, input); !errors.Is(err, ErrDenied) {
		t.Fatal("developer pricing", err)
	}
	products, err := s.MerchantProducts(ctx, session.Token, a.WorkspaceID)
	if err != nil || len(products) != 1 || products[0].AmountMinor != 1500 || products[0].Active {
		t.Fatal(products, err)
	}
	for name, change := range map[string]func(*MerchantProductInput){"fraction invalid range": func(v *MerchantProductInput) { v.AmountMinor = 49 }, "missing active": func(v *MerchantProductInput) { v.Active = nil }, "currency": func(v *MerchantProductInput) { v.Currency = "xyz" }, "control": func(v *MerchantProductInput) { v.Name = "bad\nname" }, "missing key": func(v *MerchantProductInput) { v.Key = "" }} {
		t.Run(name, func(t *testing.T) {
			bad := input
			change(&bad)
			if _, err = s.SaveMerchantProduct(ctx, session.Token, bad); !errors.Is(err, ErrInvalid) {
				t.Fatal(err)
			}
		})
	}
}

func TestMerchantProductHTTPAndBoundedCatalog(t *testing.T) {
	t.Parallel()
	s, a, session, p := onboardingFixture(t)
	ctx := t.Context()
	h, err := NewHTTP(s, HTTPOptions{Origin: "https://portal.example.test", Merchant: p, MerchantCountries: []string{"BG"}})
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: h.cookie, Value: session.Token}
	active := true
	input := MerchantProductInput{Workspace: a.WorkspaceID, Key: randomToken(), Name: "Product", Currency: "eur", AmountMinor: 50, Active: &active}
	raw, _ := json.Marshal(input)
	if w := portalRequest(h, "POST", "/api/merchant/products", string(raw), h.origin, "", cookie); w.Code != 403 {
		t.Fatal("missing csrf", w.Code)
	}
	if w := portalRequest(h, "POST", "/api/merchant/products", string(raw), h.origin, csrfFor(session.Token), cookie); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	for range 99 {
		input.Key = randomToken()
		if _, err = s.SaveMerchantProduct(ctx, session.Token, input); err != nil {
			t.Fatal(err)
		}
	}
	input.Key = randomToken()
	if _, err = s.SaveMerchantProduct(ctx, session.Token, input); !errors.Is(err, ErrMerchantProductConflict) {
		t.Fatal("unbounded catalog", err)
	}
	list, err := s.MerchantProducts(ctx, session.Token, a.WorkspaceID)
	if err != nil || len(list) != 100 {
		t.Fatal(len(list), err)
	}
	h.merchant = nil
	if w := portalRequest(h, "GET", "/api/merchant/products?workspace="+a.WorkspaceID, "", "", "", cookie); w.Code != 404 {
		t.Fatal(w.Code)
	}
}
