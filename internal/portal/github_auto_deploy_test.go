package portal

import (
	"errors"
	"testing"
)

func TestGitHubAutoDeployToggleFencesOldPush(t *testing.T) {
	s, session, p := setupPushProcessing(t)
	defer s.Close()
	claim, err := s.ClaimGitHubPush(t.Context())
	if err != nil || claim == nil {
		t.Fatal(claim, err)
	}
	if err = s.SetGitHubAutoDeploy(t.Context(), session.Token, p.ID, false); err != nil {
		t.Fatal(err)
	}
	connection, err := s.ProjectGitHubConnection(t.Context(), session.Token, p.ID)
	if err != nil || connection.DeployOnPush || connection.Revision != claim.Connection.Revision+1 {
		t.Fatal(connection, err)
	}
	if err = s.SetGitHubAutoDeploy(t.Context(), session.Token, p.ID, false); err != nil {
		t.Fatal(err)
	}
	unchanged, err := s.ProjectGitHubConnection(t.Context(), session.Token, p.ID)
	if err != nil || unchanged.Revision != connection.Revision {
		t.Fatal(unchanged, err)
	}
	if err = s.SetGitHubAutoDeploy(t.Context(), session.Token, p.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CompleteGitHubPush(t.Context(), claim.Event.ID, claim.Lease, claim.Event.After); !errors.Is(err, ErrGitHubPushLease) {
		t.Fatal(err)
	}
}

func TestGitHubAutoDeployRejectsOtherWorkspace(t *testing.T) {
	s, _, p := setupPushProcessing(t)
	defer s.Close()
	_, other := verifiedAccount(t, s, "auto-other@example.test")
	if err := s.SetGitHubAutoDeploy(t.Context(), other.Token, p.ID, false); err == nil {
		t.Fatal("other workspace changed automatic deployment")
	}
}
