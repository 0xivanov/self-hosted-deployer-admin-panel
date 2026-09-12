//go:build integration

package portal

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestVerificationResendFlow(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)
	m := testMailer(t, s)
	if err := m.Register(t.Context(), "resend@example.test", testPassword, "Workspace"); err != nil {
		t.Fatal(err)
	}
	var first string
	if _, err := m.DeliverOne(t.Context(), senderFunc(func(_ context.Context, msg Mail) error {
		first = strings.Split(strings.Split(msg.Text, "/#verify=")[1], "\n")[0]
		return nil
	})); err != nil {
		t.Fatal(err)
	}
	h, err := NewHTTP(s, HTTPOptions{Origin: m.origin, Mail: m, Signup: true})
	if err != nil {
		t.Fatal(err)
	}
	send := func(email string) string {
		t.Helper()
		w := portalRequest(h, "POST", "/api/verification/resend", `{"email":"`+email+`"}`, h.origin, "", nil)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	message := send("resend@example.test")
	if send("missing@example.test") != message {
		t.Fatal("account existence disclosed")
	}
	var count int
	if err = s.db.QueryRow("SELECT count(*) FROM mail_outbox").Scan(&count); err != nil || count != 1 {
		t.Fatal("cooldown failed", count, err)
	}
	now := s.now().Add(2 * time.Minute)
	s.now = func() time.Time { return now }
	if send("resend@example.test") != message {
		t.Fatal("response changed")
	}
	if err = s.db.QueryRow("SELECT count(*) FROM mail_outbox").Scan(&count); err != nil || count != 2 {
		t.Fatal("replacement not queued", count, err)
	}
	var replacement string
	if _, err = m.DeliverOne(t.Context(), senderFunc(func(_ context.Context, msg Mail) error {
		replacement = strings.Split(strings.Split(msg.Text, "/#verify=")[1], "\n")[0]
		return nil
	})); err != nil {
		t.Fatal(err)
	}
	if replacement == "" || replacement == first || strings.Contains(message, replacement) {
		t.Fatal("invalid or exposed link")
	}
	if err = s.Verify(t.Context(), first); err != nil {
		t.Fatal("resend invalidated earlier valid link", err)
	}
	if _, err = s.Login(t.Context(), "resend@example.test", testPassword); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	if send("resend@example.test") != message {
		t.Fatal("verified account disclosed")
	}
	if err = s.db.QueryRow("SELECT count(*) FROM mail_outbox").Scan(&count); err != nil || count != 2 {
		t.Fatal("verified user mailed", count, err)
	}
	w := portalRequest(h, "POST", "/api/verification/resend", `{"email":"resend@example.test"}`, "https://foreign.example.test", "", nil)
	if w.Code != 403 {
		t.Fatal("foreign origin accepted", w.Code)
	}
}
