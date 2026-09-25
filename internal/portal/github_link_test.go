package portal

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestGitHubLinkBoundToSessionProjectAndSingleUse(t *testing.T) {
	s, owner, session, _, other, p := projectClientFixture(t)
	ctx := context.Background()
	state, err := s.BeginGitHubLink(ctx, session.Token, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	var saved, sessionHash string
	if err = s.db.QueryRow("SELECT state_hash,session_hash FROM github_link_states WHERE project_id=?", p.ID).Scan(&saved, &sessionHash); err != nil {
		t.Fatal(err)
	}
	if saved == state.State || sessionHash == session.Token || saved != digest(state.State) {
		t.Fatal("link/session secret stored in clear")
	}
	if _, err = s.BeginGitHubLink(ctx, session.Token, p.ID); !errors.Is(err, ErrGitHubLinkRate) {
		t.Fatal("missing attempt rate limit", err)
	}
	if _, err = s.ConsumeGitHubLink(ctx, other.Token, p.ID, state.State); !errors.Is(err, ErrDenied) {
		t.Fatal("other user accepted", err)
	}
	second, err := s.Login(ctx, owner.Email, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ConsumeGitHubLink(ctx, second.Token, p.ID, state.State); !errors.Is(err, ErrGitHubLink) {
		t.Fatal("different session accepted", err)
	}
	sibling, err := s.CreateProject(ctx, session.Token, owner.WorkspaceID, "Sibling", "static")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ConsumeGitHubLink(ctx, session.Token, sibling.ID, state.State); !errors.Is(err, ErrGitHubLink) {
		t.Fatal("different project accepted", err)
	}
	result, err := s.ConsumeGitHubLink(ctx, session.Token, p.ID, state.State)
	if err != nil || result.ActorID != owner.ID || result.ProjectID != p.ID {
		t.Fatal("valid callback rejected", err)
	}
	if _, err = s.ConsumeGitHubLink(ctx, session.Token, p.ID, state.State); !errors.Is(err, ErrGitHubLink) {
		t.Fatal("callback replay accepted", err)
	}
}
func TestGitHubLinkExpiryAndAuthorityRevocation(t *testing.T) {
	for _, reason := range []string{"expired", "role removed", "project deleting"} {
		t.Run(reason, func(t *testing.T) {
			s, owner, session, _, _, p := projectClientFixture(t)
			ctx := context.Background()
			state, err := s.BeginGitHubLink(ctx, session.Token, p.ID)
			if err != nil {
				t.Fatal(err)
			}
			switch reason {
			case "expired":
				s.now = func() time.Time { return time.Unix(state.ExpiresAt, 0) }
			case "role removed":
				_, err = s.db.Exec("UPDATE memberships SET role='viewer' WHERE user_id=? AND workspace_id=?", owner.ID, owner.WorkspaceID)
			case "project deleting":
				_, err = s.db.Exec("UPDATE projects SET deletion_requested_at=1 WHERE id=?", p.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.ConsumeGitHubLink(ctx, session.Token, p.ID, state.State); err == nil {
				t.Fatal("invalidated callback accepted")
			}
		})
	}
}
func TestGitHubLinkMigrationAndReopen(t *testing.T) {
	s, path := newStore(t)
	owner, session := verifiedAccount(t, s, "github-migrate@example.test")
	p, err := s.CreateProject(t.Context(), session.Token, owner.WorkspaceID, "Site", "static")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("DROP TABLE github_link_states; PRAGMA user_version=46"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	state, err := s.BeginGitHubLink(t.Context(), session.Token, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.ConsumeGitHubLink(t.Context(), session.Token, p.ID, state.State); err != nil {
		t.Fatal("reopen lost valid session/project/state", err)
	}
	if _, err = s.BeginGitHubLink(t.Context(), session.Token, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("DELETE FROM projects WHERE id=?", p.ID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = s.db.QueryRow("SELECT count(*) FROM github_link_states").Scan(&count); err != nil || count != 0 {
		t.Fatal("state cleanup", err)
	}
}
