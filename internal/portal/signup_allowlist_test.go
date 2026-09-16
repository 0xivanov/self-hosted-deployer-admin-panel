package portal

import (
	"os"
	"testing"
)

func TestSignupAllowlistNormalizesEmails(t *testing.T) {
	path := t.TempDir() + "/signup.json"
	if err := os.WriteFile(path, []byte(`[" Invited@Example.COM "]`), 0600); err != nil {
		t.Fatal(err)
	}
	if ok := SignupAllowlist(path)("invited@example.com"); !ok {
		t.Fatal("normalized allowlist email was denied")
	}
	if SignupAllowlist(path)("other@example.com") {
		t.Fatal("non-allowlisted email was accepted")
	}
}

func TestSignupAllowlistDeniedRegistrationCreatesNoAccount(t *testing.T) {
	store, _ := newStore(t)
	mailer := testMailer(t, store)
	h, err := NewHTTP(store, HTTPOptions{Origin: mailer.origin, Mail: mailer, Signup: true, SignupAllowed: func(string) bool { return false }})
	if err != nil {
		t.Fatal(err)
	}
	w := portalRequest(h, "POST", "/api/register", `{"email":"denied@example.test","password":"`+testPassword+`","workspace":"Denied"}`, h.origin, "", nil)
	if w.Code != 403 || w.Body.String() != `{"error":"Registration is by invitation. Contact the operator for an invitation."}
` {
		t.Fatalf("unexpected denial: %d %s", w.Code, w.Body.String())
	}
	if _, err := store.Login(t.Context(), "denied@example.test", testPassword); err != ErrCredentials {
		t.Fatalf("denied signup created an account: %v", err)
	}
}

func TestSignupAllowlistInvalidReloadFailsClosed(t *testing.T) {
	path := t.TempDir() + "/signup.json"
	if err := os.WriteFile(path, []byte(`["one@example.com"]`), 0600); err != nil {
		t.Fatal(err)
	}
	allowed := SignupAllowlist(path)
	if !allowed("one@example.com") {
		t.Fatal("initial allowlist rejected email")
	}
	if err := os.WriteFile(path, []byte(`{"not":"an array"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if allowed("one@example.com") {
		t.Fatal("invalid reloaded allowlist was accepted")
	}
}
