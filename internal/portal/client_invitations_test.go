package portal

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func clientInvitationToken(t *testing.T, m *AccountMail) string {
	t.Helper()
	var token string
	worked, err := m.DeliverOne(context.Background(), senderFunc(func(_ context.Context, msg Mail) error {
		if msg.Subject != "Review your website on Launchstead" || !strings.Contains(msg.Text, "/#client-invite=") {
			t.Fatalf("wrong client invitation mail: %#v", msg)
		}
		token = strings.Split(strings.Split(msg.Text, "/#client-invite=")[1], "\n")[0]
		return nil
	}))
	if err != nil || !worked || token == "" {
		t.Fatalf("client invitation delivery: worked=%v err=%v", worked, err)
	}
	return token
}

func clientInvitationFixture(t *testing.T) (*Store, *AccountMail, Account, Session, Project) {
	t.Helper()
	s, _ := newStore(t)
	m := testMailer(t, s)
	owner, ownerSession := verifiedAccount(t, s, "client-invite-owner@example.test")
	p, err := s.CreateProject(t.Context(), ownerSession.Token, owner.WorkspaceID, "Invited site", "static")
	if err != nil {
		t.Fatal(err)
	}
	return s, m, owner, ownerSession, p
}

func TestClientInvitationRecipientReplayAndSiblingIsolation(t *testing.T) {
	s, m, owner, ownerSession, p := clientInvitationFixture(t)
	ctx := context.Background()
	recipient, recipientSession := verifiedAccount(t, s, "client-invite-recipient@example.test")
	_, outsiderSession := verifiedAccount(t, s, "client-invite-outsider@example.test")
	otherProject, err := s.CreateProject(ctx, ownerSession.Token, owner.WorkspaceID, "Sibling", "static")
	if err != nil {
		t.Fatal(err)
	}
	otherOwner, otherOwnerSession := verifiedAccount(t, s, "client-invite-other-owner@example.test")
	other, err := s.CreateProject(ctx, otherOwnerSession.Token, otherOwner.WorkspaceID, "Other tenant", "static")
	if err != nil {
		t.Fatal(err)
	}

	if _, err = m.InviteClient(ctx, ownerSession.Token, p.ID, recipient.Email, nil); err != nil {
		t.Fatal(err)
	}
	token := clientInvitationToken(t, m)
	if _, err = s.AcceptClientInvitation(ctx, outsiderSession.Token, token); !errors.Is(err, ErrDenied) {
		t.Fatalf("outsider accepted invitation: %v", err)
	}
	project, err := s.AcceptClientInvitation(ctx, recipientSession.Token, token)
	if err != nil || project != p.ID {
		t.Fatalf("recipient acceptance: project=%q err=%v", project, err)
	}
	if _, err = s.AcceptClientInvitation(ctx, recipientSession.Token, token); !errors.Is(err, ErrDenied) {
		t.Fatalf("replayed invitation: %v", err)
	}
	var memberships int
	if err = s.db.QueryRow("SELECT count(*) FROM memberships WHERE user_id=? AND workspace_id=?", recipient.ID, owner.WorkspaceID).Scan(&memberships); err != nil || memberships != 0 {
		t.Fatalf("invitation created workspace membership: %d %v", memberships, err)
	}
	websites, err := s.SharedWebsites(ctx, recipientSession.Token)
	if err != nil || len(websites) != 1 || websites[0].ID != p.ID {
		t.Fatalf("recipient website access: %#v %v", websites, err)
	}
	if _, err = m.InviteClient(ctx, ownerSession.Token, otherProject.ID, recipient.Email, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ClientInvitations(ctx, otherOwnerSession.Token, p.ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("cross-tenant invitation listing: %v", err)
	}
	_ = other
}

func TestClientInvitationApprovedSignupThenVerifyAndAccept(t *testing.T) {
	s, m, _, ownerSession, p := clientInvitationFixture(t)
	ctx := context.Background()
	email := "approved-client@example.test"
	if _, err := m.InviteClient(ctx, ownerSession.Token, p.ID, email, func(got string) bool { return got == email }); err != nil {
		t.Fatal(err)
	}
	clientToken := clientInvitationToken(t, m)
	if err := m.Register(ctx, email, testPassword, "Approved workspace"); err != nil {
		t.Fatal(err)
	}
	verifyToken := ""
	worked, err := m.DeliverOne(ctx, senderFunc(func(_ context.Context, msg Mail) error {
		if msg.To != email || !strings.Contains(msg.Text, "/#verify=") {
			t.Fatalf("wrong verification mail: %#v", msg)
		}
		verifyToken = strings.Split(strings.Split(msg.Text, "/#verify=")[1], "\n")[0]
		return nil
	}))
	if err != nil || !worked || verifyToken == "" {
		t.Fatalf("verification delivery: worked=%v err=%v", worked, err)
	}
	if err = s.Verify(ctx, verifyToken); err != nil {
		t.Fatal(err)
	}
	session, err := s.Login(ctx, email, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.AcceptClientInvitation(ctx, session.Token, clientToken); err != nil || got != p.ID {
		t.Fatalf("approved recipient acceptance: project=%q err=%v", got, err)
	}
}

func TestClientInvitationAllowlistAndClosedSignup(t *testing.T) {
	s, m, _, ownerSession, p := clientInvitationFixture(t)
	ctx := context.Background()
	denied := "denied-client@example.test"
	if _, err := m.InviteClient(ctx, ownerSession.Token, p.ID, denied, func(string) bool { return false }); !errors.Is(err, ErrClientSignupApproval) {
		t.Fatalf("allowlist denial: %v", err)
	}
	var count int
	if err := s.db.QueryRow("SELECT count(*) FROM client_invitations WHERE project_id=?", p.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("denied invitation persisted: %d %v", count, err)
	}
	if err := s.db.QueryRow("SELECT count(*) FROM mail_outbox WHERE purpose='client-invite'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("denied invitation queued mail: %d %v", count, err)
	}
	verified, verifiedSession := verifiedAccount(t, s, "closed-signup-client@example.test")
	if _, err := m.InviteClient(ctx, ownerSession.Token, p.ID, verified.Email, nil); err != nil {
		t.Fatalf("verified account invite with signup closed: %v", err)
	}
	if _, err := s.AcceptClientInvitation(ctx, verifiedSession.Token, clientInvitationToken(t, m)); err != nil {
		t.Fatal(err)
	}
}

func TestClientInvitationRevocationExpiryReplacementAndDeliveryDiscard(t *testing.T) {
	s, m, _, ownerSession, p := clientInvitationFixture(t)
	ctx := context.Background()
	recipient, recipientSession := verifiedAccount(t, s, "replace-client@example.test")
	secondRecipient, secondRecipientSession := verifiedAccount(t, s, "replace-client-2@example.test")
	first, err := m.InviteClient(ctx, ownerSession.Token, p.ID, recipient.Email, nil)
	if err != nil {
		t.Fatal(err)
	}
	oldToken := clientInvitationToken(t, m)
	if err = s.RevokeClientInvitation(ctx, ownerSession.Token, p.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcceptClientInvitation(ctx, recipientSession.Token, oldToken); !errors.Is(err, ErrDenied) {
		t.Fatalf("revoked invitation accepted: %v", err)
	}
	if _, err = m.InviteClient(ctx, ownerSession.Token, p.ID, secondRecipient.Email, nil); err != nil {
		t.Fatal(err)
	}
	secondToken := clientInvitationToken(t, m)
	if _, err = m.InviteClient(ctx, ownerSession.Token, p.ID, secondRecipient.Email, nil); !errors.Is(err, ErrClientInviteRate) {
		t.Fatalf("same-email rate limit: %v", err)
	}
	// Move past the one-minute per-recipient window to exercise replacement.
	now := s.now()
	s.now = func() time.Time { return now.Add(61 * time.Second) }
	if _, err = m.InviteClient(ctx, ownerSession.Token, p.ID, secondRecipient.Email, nil); err != nil {
		t.Fatal(err)
	}
	thirdToken := clientInvitationToken(t, m)
	if _, err = s.AcceptClientInvitation(ctx, secondRecipientSession.Token, secondToken); !errors.Is(err, ErrDenied) {
		t.Fatalf("replaced invitation accepted: %v", err)
	}
	if _, err = s.AcceptClientInvitation(ctx, secondRecipientSession.Token, thirdToken); err != nil {
		t.Fatal(err)
	}
	expiring, expiringSession := verifiedAccount(t, s, "expiring-client@example.test")
	if _, err = m.InviteClient(ctx, ownerSession.Token, p.ID, expiring.Email, nil); err != nil {
		t.Fatal(err)
	}
	expiringToken := clientInvitationToken(t, m)
	base := s.now()
	s.now = func() time.Time { return base.Add(8 * 24 * time.Hour) }
	if _, err = s.db.Exec("UPDATE sessions SET expires_at=? WHERE token_hash=?", s.now().Add(time.Hour).Unix(), digest(expiringSession.Token)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcceptClientInvitation(ctx, expiringSession.Token, expiringToken); !errors.Is(err, ErrDenied) {
		t.Fatalf("expired invitation accepted: %v", err)
	}
	s.now = func() time.Time { return base }

	// A revoked queued message is discarded without invoking the sender.
	other, _ := verifiedAccount(t, s, "discard-client@example.test")
	inv, err := m.InviteClient(ctx, ownerSession.Token, p.ID, other.Email, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RevokeClientInvitation(ctx, ownerSession.Token, p.ID, inv.ID); err != nil {
		t.Fatal(err)
	}
	called := false
	worked, err := m.DeliverOne(ctx, senderFunc(func(context.Context, Mail) error { called = true; return nil }))
	if err != nil || !worked || called {
		t.Fatalf("revoked mail delivered: worked=%v err=%v called=%v", worked, err, called)
	}
}

func TestClientInvitationOwnerDemotionDeletionAndPendingCap(t *testing.T) {
	s, m, owner, ownerSession, p := clientInvitationFixture(t)
	ctx := context.Background()
	secondOwner, secondOwnerSession := verifiedAccount(t, s, "second-client-owner@example.test")
	if _, err := s.db.Exec("INSERT INTO memberships VALUES(?,?,?)", secondOwner.ID, owner.WorkspaceID, "owner"); err != nil {
		t.Fatal(err)
	}
	recipient, recipientSession := verifiedAccount(t, s, "demotion-client@example.test")
	if _, err := m.InviteClient(ctx, ownerSession.Token, p.ID, recipient.Email, nil); err != nil {
		t.Fatal(err)
	}
	token := clientInvitationToken(t, m)
	if err := s.ChangeMember(ctx, secondOwnerSession.Token, owner.WorkspaceID, owner.ID, "developer"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptClientInvitation(ctx, recipientSession.Token, token); !errors.Is(err, ErrDenied) {
		t.Fatalf("demoted owner invitation accepted: %v", err)
	}
	if err := s.ChangeProjectClient(ctx, secondOwnerSession.Token, p.ID, recipient.Email, true); err != nil {
		t.Fatal("direct grant should remain available after owner demotion: ", err)
	}

	deleteRecipient, deleteSession := verifiedAccount(t, s, "delete-client@example.test")
	if _, err := m.InviteClient(ctx, secondOwnerSession.Token, p.ID, deleteRecipient.Email, nil); err != nil {
		t.Fatal(err)
	}
	deleteToken := clientInvitationToken(t, m)
	for i := 0; i < 19; i++ {
		email := "pending-" + string(rune('a'+i)) + "@example.test"
		if _, err := m.InviteClient(ctx, secondOwnerSession.Token, p.ID, email, func(string) bool { return true }); err != nil {
			t.Fatalf("pending invitation %d: %v", i, err)
		}
	}
	if _, err := m.InviteClient(ctx, secondOwnerSession.Token, p.ID, "pending-over-cap@example.test", func(string) bool { return true }); !errors.Is(err, ErrClientInviteRate) {
		t.Fatalf("pending invitation cap: %v", err)
	}
	if _, err := s.db.Exec("UPDATE projects SET deletion_requested_at=1 WHERE id=?", p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptClientInvitation(ctx, deleteSession.Token, deleteToken); !errors.Is(err, ErrDenied) {
		t.Fatalf("deleted project invitation accepted: %v", err)
	}
	called := false
	if worked, err := m.DeliverOne(ctx, senderFunc(func(context.Context, Mail) error { called = true; return nil })); err != nil || !worked || called {
		t.Fatalf("deleted project mail: worked=%v called=%v err=%v", worked, called, err)
	}
}

func TestClientInvitationDirectGrantAndRemovalInvalidatePendingTokens(t *testing.T) {
	s, m, _, ownerSession, p := clientInvitationFixture(t)
	ctx := context.Background()
	grantTarget, grantSession := verifiedAccount(t, s, "direct-grant-client@example.test")
	if _, err := m.InviteClient(ctx, ownerSession.Token, p.ID, grantTarget.Email, nil); err != nil {
		t.Fatal(err)
	}
	grantToken := clientInvitationToken(t, m)
	if err := s.ChangeProjectClient(ctx, ownerSession.Token, p.ID, grantTarget.Email, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptClientInvitation(ctx, grantSession.Token, grantToken); !errors.Is(err, ErrDenied) {
		t.Fatalf("direct grant left invitation usable: %v", err)
	}

	removeTarget, removeSession := verifiedAccount(t, s, "direct-remove-client@example.test")
	if _, err := m.InviteClient(ctx, ownerSession.Token, p.ID, removeTarget.Email, nil); err != nil {
		t.Fatal(err)
	}
	removeToken := clientInvitationToken(t, m)
	if _, err := s.db.Exec("INSERT INTO project_clients(project_id,user_id,created_at) VALUES(?,?,?)", p.ID, removeTarget.ID, s.now().Unix()); err != nil {
		t.Fatal(err)
	}
	if err := s.ChangeProjectClient(ctx, ownerSession.Token, p.ID, removeTarget.Email, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptClientInvitation(ctx, removeSession.Token, removeToken); !errors.Is(err, ErrDenied) {
		t.Fatalf("direct removal left invitation usable: %v", err)
	}
}

func TestClientInvitationsMigration41To42PreservesState(t *testing.T) {
	s, path := newStore(t)
	m := testMailer(t, s)
	owner, ownerSession := verifiedAccount(t, s, "migration-client-owner@example.test")
	client, clientSession := verifiedAccount(t, s, "migration-client@example.test")
	p, err := s.CreateProject(t.Context(), ownerSession.Token, owner.WorkspaceID, "Migrated invite site", "static")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ChangeProjectClient(t.Context(), ownerSession.Token, p.ID, client.Email, true); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("DROP TABLE client_invitations; PRAGMA user_version=41"); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.Authenticate(t.Context(), ownerSession.Token); err != nil {
		t.Fatalf("owner session lost during migration: %v", err)
	}
	if _, err = s.Authenticate(t.Context(), clientSession.Token); err != nil {
		t.Fatalf("client session lost during migration: %v", err)
	}
	clients, err := s.ProjectClients(t.Context(), ownerSession.Token, p.ID)
	if err != nil || len(clients) != 1 || clients[0].Email != client.Email {
		t.Fatalf("project client grant lost during migration: %#v %v", clients, err)
	}
	m = testMailer(t, s)
	if _, err = m.InviteClient(t.Context(), ownerSession.Token, p.ID, "after-migration@example.test", func(string) bool { return true }); err != nil {
		t.Fatalf("client invitation after migration: %v", err)
	}
}
