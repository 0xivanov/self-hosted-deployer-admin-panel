package portal

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestClientInvitationsHTTPRequiresCSRFAndOwnerScope(t *testing.T) {
	s, m, owner, _, p := clientInvitationFixture(t)
	recipient, _ := verifiedAccount(t, s, "http-client@example.test")
	h, err := NewHTTP(s, HTTPOptions{Origin: m.origin, Mail: m, Signup: false})
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf := httpLogin(t, h, owner.Email)
	body := `{"project":"` + p.ID + `","email":"` + recipient.Email + `"}`
	if w := portalRequest(h, "POST", "/api/client-invitations", body, h.origin, "", cookie); w.Code != 403 {
		t.Fatalf("missing csrf: %d %s", w.Code, w.Body.String())
	}
	if w := portalRequest(h, "POST", "/api/client-invitations", body, h.origin, csrf, cookie); w.Code != 200 {
		t.Fatalf("owner invitation: %d %s", w.Code, w.Body.String())
	}
	outsider, _ := verifiedAccount(t, s, "http-outsider@example.test")
	outsiderCookie, outsiderCSRF := httpLogin(t, h, outsider.Email)
	if w := portalRequest(h, "GET", "/api/client-invitations?project="+p.ID, "", "", "", outsiderCookie); w.Code != 403 {
		t.Fatalf("outsider invitation list: %d %s", w.Code, w.Body.String())
	}
	if w := portalRequest(h, "POST", "/api/client-invitations/revoke", `{"project":"`+p.ID+`","id":"missing"}`, h.origin, outsiderCSRF, outsiderCookie); w.Code != 403 {
		t.Fatalf("outsider invitation revoke: %d %s", w.Code, w.Body.String())
	}
}

func TestClientInvitationsHTTPListRevokeAndAccept(t *testing.T) {
	s, m, owner, _, p := clientInvitationFixture(t)
	recipient, _ := verifiedAccount(t, s, "http-recipient@example.test")
	h, err := NewHTTP(s, HTTPOptions{Origin: m.origin, Mail: m, Signup: false})
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf := httpLogin(t, h, owner.Email)
	w := portalRequest(h, "POST", "/api/client-invitations", `{"project":"`+p.ID+`","email":"`+recipient.Email+`"}`, h.origin, csrf, cookie)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var invitation ClientInvitation
	if err = json.Unmarshal(w.Body.Bytes(), &invitation); err != nil || invitation.ID == "" {
		t.Fatalf("invitation response: %#v %v", invitation, err)
	}
	w = portalRequest(h, "GET", "/api/client-invitations?project="+p.ID, "", "", "", cookie)
	if w.Code != 200 || !json.Valid(w.Body.Bytes()) || !containsInvitation(w.Body.Bytes(), invitation.ID) {
		t.Fatalf("invitation list: %d %s", w.Code, w.Body.String())
	}
	w = portalRequest(h, "POST", "/api/client-invitations/revoke", `{"project":"`+p.ID+`","id":"`+invitation.ID+`"}`, h.origin, csrf, cookie)
	if w.Code != 200 {
		t.Fatalf("invitation revoke: %d %s", w.Code, w.Body.String())
	}
	if err := s.RevokeClientInvitation(t.Context(), ownerSessionForTest(t, s, owner.Email), p.ID, invitation.ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("revocation replay: %v", err)
	}
	if worked, err := m.DeliverOne(t.Context(), senderFunc(func(context.Context, Mail) error { t.Fatal("revoked invitation was sent"); return nil })); err != nil || !worked {
		t.Fatalf("discard revoked HTTP invitation: worked=%v err=%v", worked, err)
	}
	base := s.now()
	s.now = func() time.Time { return base.Add(61 * time.Second) }
	w = portalRequest(h, "POST", "/api/client-invitations", `{"project":"`+p.ID+`","email":"`+recipient.Email+`"}`, h.origin, csrf, cookie)
	if w.Code != 200 {
		t.Fatalf("second invitation: %d %s", w.Code, w.Body.String())
	}
	recipientCookie, recipientCSRF := httpLogin(t, h, recipient.Email)
	token := clientInvitationToken(t, m)
	w = portalRequest(h, "POST", "/api/client-invitations/accept", `{"token":"`+token+`"}`, h.origin, recipientCSRF, recipientCookie)
	if w.Code != 200 || !strings.Contains(w.Body.String(), p.ID) {
		t.Fatalf("invitation accept: %d %s", w.Code, w.Body.String())
	}
}

func containsInvitation(body []byte, id string) bool {
	return len(body) > 0 && json.Valid(body) && strings.Contains(string(body), id)
}

func ownerSessionForTest(t *testing.T, s *Store, email string) string {
	t.Helper()
	session, err := s.Login(t.Context(), email, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	return session.Token
}
