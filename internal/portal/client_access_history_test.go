package portal

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestClientAccessHistoryOwnerScope(t *testing.T) {
	s, owner, session, client, clientSession, p := projectClientFixture(t)
	defer s.Close()
	sibling, err := s.CreateProject(t.Context(), session.Token, owner.WorkspaceID, "Other site", "static")
	if err != nil {
		t.Fatal(err)
	}
	for _, project := range []string{p.ID, sibling.ID} {
		if err = s.ChangeProjectClient(t.Context(), session.Token, project, client.Email, true); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.ChangeProjectClient(t.Context(), session.Token, p.ID, client.Email, false); err != nil {
		t.Fatal(err)
	}
	history, err := s.ClientAccessHistory(t.Context(), session.Token, p.ID)
	if err != nil || len(history) != 2 {
		t.Fatal(history, err)
	}
	seen := map[string]bool{}
	for _, e := range history {
		seen[e.Action] = true
		if e.Actor != owner.Email || e.Client != client.Email {
			t.Fatal(e)
		}
	}
	if !seen["granted"] || !seen["revoked"] {
		t.Fatal(seen)
	}
	if _, err = s.ClientAccessHistory(t.Context(), clientSession.Token, p.ID); err == nil {
		t.Fatal("client read owner audit")
	}
	developer, developerSession := verifiedAccount(t, s, "audit-developer@example.test")
	if _, err = s.db.Exec("INSERT INTO memberships VALUES(?,?,?)", developer.ID, owner.WorkspaceID, "developer"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ClientAccessHistory(t.Context(), developerSession.Token, p.ID); err == nil {
		t.Fatal("developer read owner audit")
	}
	h, err := NewHTTP(s, HTTPOptions{Origin: "https://portal.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	cookie, _ := httpLogin(t, h, owner.Email)
	response := portalRequest(h, "GET", "/api/project-clients?project="+p.ID, "", "", "", cookie)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"history"`) {
		t.Fatal(response.Code, response.Body.String())
	}
}

func TestClientAccessHistoryInvitationsHideTokens(t *testing.T) {
	s, m, owner, session, p := clientInvitationFixture(t)
	defer s.Close()
	recipient, recipientSession := verifiedAccount(t, s, "audit-recipient@example.test")
	if _, err := m.InviteClient(t.Context(), session.Token, p.ID, recipient.Email, nil); err != nil {
		t.Fatal(err)
	}
	token := clientInvitationToken(t, m)
	if _, err := s.AcceptClientInvitation(t.Context(), recipientSession.Token, token); err != nil {
		t.Fatal(err)
	}
	history, err := s.ClientAccessHistory(t.Context(), session.Token, p.ID)
	if err != nil || len(history) != 2 {
		t.Fatal(history, err)
	}
	for _, e := range history {
		if e.Client != recipient.Email {
			t.Fatal(e)
		}
		if e.Action == "invited" && e.Actor != owner.Email {
			t.Fatal(e)
		}
		if e.Action == "invitation_accepted" && e.Actor != recipient.Email {
			t.Fatal(e)
		}
	}
	encoded, err := json.Marshal(history)
	if err != nil || strings.Contains(string(encoded), token) || strings.Contains(string(encoded), "proof_hash") {
		t.Fatal("history exposed internal invitation data")
	}
}
