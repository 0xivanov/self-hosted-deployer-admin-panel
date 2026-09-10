package portal

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

const testPassword = "a long synthetic password"

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "private")
	path := filepath.Join(dir, "portal.db")
	s, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}
func verifiedAccount(t *testing.T, s *Store, email string) (Account, Session) {
	t.Helper()
	ctx := context.Background()
	a, token, e := s.Register(ctx, email, testPassword, "workspace")
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Verify(ctx, token); e != nil {
		t.Fatal(e)
	}
	session, e := s.Login(ctx, email, testPassword)
	if e != nil {
		t.Fatal(e)
	}
	return a, session
}

func TestVerificationAndPersistentSessions(t *testing.T) {
	t.Parallel()
	s, path := newStore(t)
	ctx := context.Background()
	a, token, e := s.Register(ctx, "Alice@Example.com", testPassword, "Alice workspace")
	if e != nil {
		t.Fatal(e)
	}
	if a.Email != "alice@example.com" {
		t.Fatal(a)
	}
	if _, e = s.Login(ctx, a.Email, testPassword); !errors.Is(e, ErrCredentials) {
		t.Fatalf("unverified login: %v", e)
	}
	if _, _, e = s.Register(ctx, "alice@example.com", testPassword, "duplicate"); !errors.Is(e, ErrExists) {
		t.Fatalf("duplicate email: %v", e)
	}
	if e = s.Verify(ctx, token); e != nil {
		t.Fatal(e)
	}
	if e = s.Verify(ctx, token); !errors.Is(e, ErrDenied) {
		t.Fatalf("verification replay: %v", e)
	}
	session, e := s.Login(ctx, a.Email, testPassword)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	reopened, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	got, e := reopened.Authenticate(ctx, session.Token)
	if e != nil || got.ID != a.ID {
		t.Fatalf("persistent session: %+v %v", got, e)
	}
	if e = reopened.Logout(ctx, session.Token); e != nil {
		t.Fatal(e)
	}
	if _, e = reopened.Authenticate(ctx, session.Token); !errors.Is(e, ErrDenied) {
		t.Fatalf("logged out session: %v", e)
	}
}
func TestWorkspaceIsolationAndRoles(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)
	ctx := context.Background()
	alice, as := verifiedAccount(t, s, "alice@example.com")
	bob, bs := verifiedAccount(t, s, "bob@example.com")
	p, e := s.CreateProject(ctx, as.Token, alice.WorkspaceID, "site", "static")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.GetProject(ctx, bs.Token, p.ID); !errors.Is(e, ErrDenied) {
		t.Fatalf("cross-workspace read: %v", e)
	}
	if _, e = s.CreateProject(ctx, bs.Token, alice.WorkspaceID, "attack", "node"); !errors.Is(e, ErrDenied) {
		t.Fatalf("cross-workspace write: %v", e)
	}
	// Fixture membership represents a completed invitation, which is not exposed yet.
	if _, e = s.db.Exec("INSERT INTO memberships VALUES(?,?,'viewer')", bob.ID, alice.WorkspaceID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.GetProject(ctx, bs.Token, p.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.CreateProject(ctx, bs.Token, alice.WorkspaceID, "blocked", "node"); !errors.Is(e, ErrDenied) {
		t.Fatalf("viewer write: %v", e)
	}
	if _, e = s.db.Exec("DELETE FROM memberships WHERE user_id=? AND workspace_id=?", bob.ID, alice.WorkspaceID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.GetProject(ctx, bs.Token, p.ID); !errors.Is(e, ErrDenied) {
		t.Fatalf("removed member access: %v", e)
	}
}
func TestPasswordResetIsSingleUseAndRevokesSessions(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)
	ctx := context.Background()
	a, session := verifiedAccount(t, s, "reset@example.com")
	token, e := s.RequestReset(ctx, a.Email)
	if e != nil || token == "" {
		t.Fatalf("reset request: %v", e)
	}
	password := "another long synthetic password"
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Go(func() { results <- s.ResetPassword(ctx, token, password) })
	}
	wg.Wait()
	close(results)
	success, denied := 0, 0
	for e := range results {
		if e == nil {
			success++
		} else if errors.Is(e, ErrDenied) {
			denied++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || denied != 1 {
		t.Fatalf("reset outcomes success=%d denied=%d", success, denied)
	}
	if _, e = s.Authenticate(ctx, session.Token); !errors.Is(e, ErrDenied) {
		t.Fatalf("old session remains: %v", e)
	}
	if _, e = s.Login(ctx, a.Email, testPassword); !errors.Is(e, ErrCredentials) {
		t.Fatalf("old password works: %v", e)
	}
	if _, e = s.Login(ctx, a.Email, password); e != nil {
		t.Fatal(e)
	}
}
func TestExpiryAndDisabledAccounts(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)
	ctx := context.Background()
	a, session := verifiedAccount(t, s, "expiry@example.com")
	now := s.now()
	s.now = func() time.Time { return now.Add(25 * time.Hour) }
	if _, e := s.Authenticate(ctx, session.Token); !errors.Is(e, ErrDenied) {
		t.Fatalf("expired session: %v", e)
	}
	s.now = func() time.Time { return now }
	token, e := s.RequestReset(ctx, a.Email)
	if e != nil {
		t.Fatal(e)
	}
	s.now = func() time.Time { return now.Add(time.Hour) }
	if e = s.ResetPassword(ctx, token, "different long password"); !errors.Is(e, ErrDenied) {
		t.Fatalf("expired reset: %v", e)
	}
	s.now = func() time.Time { return now }
	if _, e = s.db.Exec("UPDATE users SET disabled=1 WHERE id=?", a.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Authenticate(ctx, session.Token); !errors.Is(e, ErrDenied) {
		t.Fatalf("disabled session: %v", e)
	}
	if _, e = s.Login(ctx, a.Email, testPassword); !errors.Is(e, ErrCredentials) {
		t.Fatalf("disabled login: %v", e)
	}
}
func TestPrivateDatabaseAndFutureSchema(t *testing.T) {
	t.Parallel()
	t.Run("public directory", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "public")
		if e := os.Mkdir(dir, 0755); e != nil {
			t.Fatal(e)
		}
		if s, e := Open(filepath.Join(dir, "db")); e == nil {
			s.Close()
			t.Fatal("accepted public directory")
		}
	})
	t.Run("future schema", func(t *testing.T) {
		s, path := newStore(t)
		if _, e := s.db.Exec("PRAGMA user_version=3"); e != nil {
			t.Fatal(e)
		}
		s.Close()
		if other, e := Open(path); e == nil {
			other.Close()
			t.Fatal("opened future schema")
		}
	})
}
