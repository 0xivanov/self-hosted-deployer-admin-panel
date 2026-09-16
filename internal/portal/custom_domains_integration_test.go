//go:build integration

package portal

import (
	"context"
	"errors"
	"net"
	"testing"
)

type integrationDNS struct {
	txt, a, aaaa []string
	aaaaErr      error
	calls        int
}

func (d *integrationDNS) LookupTXT(context.Context, string) ([]string, error) {
	d.calls++
	return d.txt, nil
}
func (d *integrationDNS) LookupA(context.Context, string) ([]string, error) {
	d.calls++
	return d.a, nil
}
func (d *integrationDNS) LookupAAAA(context.Context, string) ([]string, error) {
	d.calls++
	return d.aaaa, d.aaaaErr
}

func TestCustomDomainStoreSecurity(t *testing.T) {
	s, _ := newStore(t)
	owner, session := verifiedAccount(t, s, "domains-owner@example.test")
	p, err := s.CreateProject(t.Context(), session.Token, owner.WorkspaceID, "site", "static")
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.CreateCustomDomain(t.Context(), session.Token, p.ID, "customer.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateCustomDomain(t.Context(), session.Token, p.ID, "CUSTOMER.example.com"); !errors.Is(err, ErrExists) {
		t.Fatalf("global uniqueness: %v", err)
	}
	if err = s.RemoveCustomDomain(t.Context(), session.Token, p.ID, d.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.VerifyCustomDomain(t.Context(), session.Token, p.ID, d.ID, []string{"launchstead-verification=" + d.Token}, []string{customDomainTarget}, nil); !errors.Is(err, ErrDenied) {
		t.Fatalf("removing domain resurrected: %v", err)
	}
}

func TestCustomDomainHTTPTimeoutAAAAFailsClosed(t *testing.T) {
	s, _ := newStore(t)
	a, session := verifiedAccount(t, s, "dns-owner@example.test")
	p, err := s.CreateProject(t.Context(), session.Token, a.WorkspaceID, "site", "static")
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.CreateCustomDomain(t.Context(), session.Token, p.ID, "customer.example.net")
	if err != nil {
		t.Fatal(err)
	}
	dns := &integrationDNS{txt: []string{"launchstead-verification=" + d.Token}, a: []string{customDomainTarget}, aaaaErr: &net.DNSError{Err: "timeout"}}
	h, err := NewHTTP(s, HTTPOptions{Origin: "https://portal.example.test", CustomDomainResolver: dns})
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf := httpLogin(t, h, a.Email)
	w := portalRequest(h, "POST", "/api/project-domains/verify", `{"project":"`+p.ID+`","id":"`+d.ID+`"}`, h.origin, csrf, cookie)
	if w.Code != 409 {
		t.Fatalf("AAAA timeout code=%d body=%s", w.Code, w.Body.String())
	}
	got, err := s.CustomDomains(t.Context(), session.Token, p.ID)
	if err != nil || len(got) != 1 || got[0].State != "pending" {
		t.Fatalf("state after timeout: %+v %v", got, err)
	}
}

func TestCustomDomainOwnershipAndProof(t *testing.T) {
	s, _ := newStore(t)
	owner, session := verifiedAccount(t, s, "custom-owner@example.test")
	other, _ := verifiedAccount(t, s, "custom-other@example.test")
	p, err := s.CreateProject(t.Context(), session.Token, owner.WorkspaceID, "site", "static")
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.CreateCustomDomain(t.Context(), session.Token, p.ID, "owned.example.net")
	if err != nil {
		t.Fatal(err)
	}
	dns := &integrationDNS{txt: []string{"launchstead-verification=" + d.Token}, a: []string{customDomainTarget}}
	h, err := NewHTTP(s, HTTPOptions{Origin: "https://portal.example.test", CustomDomainResolver: dns})
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf := httpLogin(t, h, other.Email)
	body := `{"project":"` + p.ID + `","id":"` + d.ID + `"}`
	if w := portalRequest(h, "GET", "/api/project-domains?project="+p.ID, "", "", "", cookie); w.Code != 403 {
		t.Fatal("foreign read", w.Code)
	}
	if w := portalRequest(h, "POST", "/api/project-domains/verify", body, h.origin, csrf, cookie); w.Code != 403 {
		t.Fatal("foreign verify", w.Code)
	}
	if _, err = s.db.Exec("INSERT INTO memberships VALUES(?,?,'viewer')", other.ID, owner.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if w := portalRequest(h, "POST", "/api/project-domains/verify", body, h.origin, csrf, cookie); w.Code != 403 {
		t.Fatal("viewer verify", w.Code)
	}
	if dns.calls != 0 {
		t.Fatal("unauthorized DNS calls", dns.calls)
	}
	for _, values := range []struct{ txt, a, aaaa []string }{{[]string{"wrong"}, dns.a, nil}, {dns.txt, []string{"192.0.2.1"}, nil}, {dns.txt, dns.a, []string{"::1"}}} {
		if _, err = s.VerifyCustomDomain(t.Context(), session.Token, p.ID, d.ID, values.txt, values.a, values.aaaa); !errors.Is(err, ErrDomainDNS) {
			t.Fatal("invalid DNS accepted", err)
		}
	}
	rows, err := s.CustomDomains(t.Context(), session.Token, p.ID)
	if err != nil || len(rows) != 1 || rows[0].Token != d.Token || rows[0].State != "pending" {
		t.Fatal("proof or state lost", err)
	}
	cookie, csrf = httpLogin(t, h, owner.Email)
	if w := portalRequest(h, "POST", "/api/project-domains/verify", body, h.origin, "", cookie); w.Code != 403 {
		t.Fatal("missing CSRF", w.Code)
	}
	if w := portalRequest(h, "POST", "/api/project-domains/verify", body, h.origin, csrf, cookie); w.Code != 200 {
		t.Fatal("valid DNS", w.Code, w.Body.String())
	}
	rows, err = s.CustomDomains(t.Context(), session.Token, p.ID)
	if err != nil || rows[0].State != "verified" {
		t.Fatal("not queued", err)
	}
}
