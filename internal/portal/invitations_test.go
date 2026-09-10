package portal

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func invitationToken(t *testing.T, m *AccountMail) string {
	t.Helper()
	var token string
	worked, err := m.DeliverOne(context.Background(), senderFunc(func(_ context.Context, msg Mail) error {
		if !strings.Contains(msg.Text, "/#invite=") {
			t.Fatal("wrong mail type")
		}
		token = strings.Split(strings.Split(msg.Text, "/#invite=")[1], "\n")[0]
		return nil
	}))
	if err != nil || !worked || token == "" {
		t.Fatal("invitation delivery", worked, err)
	}
	return token
}
func TestInvitationRecipientAndReplay(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)
	m := testMailer(t, s)
	ctx := context.Background()
	a, as := verifiedAccount(t, s, "owner@example.test")
	b, bs := verifiedAccount(t, s, "guest@example.test")
	_, stranger := verifiedAccount(t, s, "stranger@example.test")
	if _, err := m.Invite(ctx, stranger.Token, a.WorkspaceID, b.Email, "owner"); !errors.Is(err, ErrDenied) {
		t.Fatal("outsider invited", err)
	}
	if _, err := m.Invite(ctx, as.Token, a.WorkspaceID, b.Email, "developer"); err != nil {
		t.Fatal(err)
	}
	token := invitationToken(t, m)
	if _, err := s.AcceptInvitation(ctx, stranger.Token, token); !errors.Is(err, ErrDenied) {
		t.Fatal("wrong email accepted", err)
	}
	workspace, err := s.AcceptInvitation(ctx, bs.Token, token)
	if err != nil || workspace != a.WorkspaceID {
		t.Fatal(workspace, err)
	}
	if _, err := s.CreateProject(ctx, bs.Token, workspace, "joined", "static"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptInvitation(ctx, bs.Token, token); !errors.Is(err, ErrDenied) {
		t.Fatal("replayed invite", err)
	}
	if _, err := m.Invite(ctx, as.Token, a.WorkspaceID, b.Email, "owner"); !errors.Is(err, ErrExists) {
		t.Fatal("invite escalated existing member", err)
	}
}
func TestRevokedExpiredAndReplacedInvites(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)
	m := testMailer(t, s)
	ctx := context.Background()
	a, as := verifiedAccount(t, s, "owner@example.test")
	b, bs := verifiedAccount(t, s, "guest@example.test")
	first, err := m.Invite(ctx, as.Token, a.WorkspaceID, b.Email, "viewer")
	if err != nil {
		t.Fatal(err)
	}
	old := invitationToken(t, m)
	if err := s.RevokeInvitation(ctx, as.Token, a.WorkspaceID, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptInvitation(ctx, bs.Token, old); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	if _, err := m.Invite(ctx, as.Token, a.WorkspaceID, b.Email, "owner"); err != nil {
		t.Fatal(err)
	}
	old = invitationToken(t, m)
	if _, err := m.Invite(ctx, as.Token, a.WorkspaceID, b.Email, "viewer"); err != nil {
		t.Fatal(err)
	}
	current := invitationToken(t, m)
	if _, err := s.AcceptInvitation(ctx, bs.Token, old); !errors.Is(err, ErrDenied) {
		t.Fatal("replaced link active", err)
	}
	later := time.Now().Add(8 * 24 * time.Hour)
	s.now = func() time.Time { return later }
	// Renew only the fixture session to test invitation expiry independently.
	if _, err := s.db.Exec("UPDATE sessions SET expires_at=?", later.Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptInvitation(ctx, bs.Token, current); !errors.Is(err, ErrDenied) {
		t.Fatal("expired link accepted", err)
	}
}
func TestOwnerDemotionPermanentlyRevokesInvitations(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)
	m := testMailer(t, s)
	ctx := context.Background()
	a, as := verifiedAccount(t, s, "owner@example.test")
	b, bs := verifiedAccount(t, s, "second@example.test")
	guest, gs := verifiedAccount(t, s, "guest@example.test")
	if _, err := s.db.Exec("INSERT INTO memberships VALUES(?,?,'owner')", b.ID, a.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Invite(ctx, as.Token, a.WorkspaceID, guest.Email, "owner"); err != nil {
		t.Fatal(err)
	}
	token := invitationToken(t, m)
	if err := s.ChangeMember(ctx, bs.Token, a.WorkspaceID, a.ID, "developer"); err != nil {
		t.Fatal(err)
	}
	if err := s.ChangeMember(ctx, bs.Token, a.WorkspaceID, a.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptInvitation(ctx, gs.Token, token); !errors.Is(err, ErrDenied) {
		t.Fatal("old owner invitation restored", err)
	}
}
