package portal

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type senderFunc func(context.Context, Mail) error

func (f senderFunc) Send(ctx context.Context, m Mail) error { return f(ctx, m) }
func testMailer(t *testing.T, s *Store) *AccountMail {
	t.Helper()
	m, e := NewAccountMail(s, bytes.Repeat([]byte{7}, 32), "https://portal.example.test", false)
	if e != nil {
		t.Fatal(e)
	}
	return m
}
func TestRegistrationAndMailAreAtomic(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)
	ctx := context.Background()
	_, _, err := s.register(ctx, "atomic@example.test", testPassword, "Workspace", func(context.Context, *sql.Tx, string, string, string, string) error {
		return errors.New("mail storage unavailable")
	})
	if err == nil {
		t.Fatal("queue error ignored")
	}
	var count int
	if err = s.db.QueryRow("SELECT count(*) FROM users").Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial account: %d %v", count, err)
	}
	m := testMailer(t, s)
	if err = m.Register(ctx, "atomic@example.test", testPassword, "Workspace"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Login(ctx, "atomic@example.test", testPassword); !errors.Is(err, ErrCredentials) {
		t.Fatalf("unverified user: %v", err)
	}
}
func TestEncryptedMailSurvivesRestartAndIsConsumed(t *testing.T) {
	t.Parallel()
	s, path := newStore(t)
	m := testMailer(t, s)
	ctx := context.Background()
	if err := m.Register(ctx, "restart@example.test", testPassword, "Workspace"); err != nil {
		t.Fatal(err)
	}
	var payload []byte
	if err := s.db.QueryRow("SELECT payload FROM mail_outbox").Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(payload, []byte("restart@example.test")) || bytes.Contains(payload, []byte("https://")) {
		t.Fatal("plaintext mail stored")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	m = testMailer(t, reopened)
	var delivered Mail
	worked, err := m.DeliverOne(ctx, senderFunc(func(_ context.Context, msg Mail) error { delivered = msg; return nil }))
	if !worked || err != nil {
		t.Fatal(worked, err)
	}
	if delivered.To != "restart@example.test" || !strings.Contains(delivered.Text, "/#verify=") {
		t.Fatal("wrong message")
	}
	token := strings.Split(strings.Split(delivered.Text, "/#verify=")[1], "\n")[0]
	if err = reopened.Verify(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err = reopened.Login(ctx, delivered.To, testPassword); err != nil {
		t.Fatal(err)
	}
	worked, err = m.DeliverOne(ctx, senderFunc(func(context.Context, Mail) error { t.Fatal("sent twice"); return nil }))
	if worked || err != nil {
		t.Fatal(worked, err)
	}
	if err = reopened.db.QueryRow("SELECT payload FROM mail_outbox").Scan(&payload); err != nil || len(payload) != 0 {
		t.Fatal("delivered payload retained", err)
	}
}
func TestMailRetryAndStaleReset(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)
	ctx := context.Background()
	a, _ := verifiedAccount(t, s, "resetmail@example.test")
	m := testMailer(t, s)
	now := time.Now()
	s.now = func() time.Time { return now }
	if err := m.RequestReset(ctx, a.Email); err != nil {
		t.Fatal(err)
	}
	worked, err := m.DeliverOne(ctx, senderFunc(func(context.Context, Mail) error { return errors.New("sensitive provider detail") }))
	if !worked || err == nil || strings.Contains(err.Error(), "sensitive") {
		t.Fatal(worked, err)
	}
	worked, err = m.DeliverOne(ctx, senderFunc(func(context.Context, Mail) error { t.Fatal("retried without delay"); return nil }))
	if worked || err != nil {
		t.Fatal(worked, err)
	}
	// Superseding the reset invalidates the earlier mail and token.
	now = now.Add(time.Minute)
	if err = m.RequestReset(ctx, a.Email); err != nil {
		t.Fatal(err)
	}
	calls := 0
	for range 2 {
		_, err = m.DeliverOne(ctx, senderFunc(func(_ context.Context, msg Mail) error { calls++; return nil }))
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("delivered %d messages", calls)
	}
}
func TestMailLeaseExcludesConcurrentDelivery(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)
	m := testMailer(t, s)
	ctx := context.Background()
	if err := m.Register(ctx, "lease@example.test", testPassword, "workspace"); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		_, err := m.DeliverOne(ctx, senderFunc(func(context.Context, Mail) error { close(entered); <-release; return nil }))
		result <- err
	}()
	<-entered
	worked, err := m.DeliverOne(ctx, senderFunc(func(context.Context, Mail) error { return errors.New("duplicate delivery") }))
	close(release)
	if worked || err != nil {
		t.Fatal(worked, err)
	}
	if err = <-result; err != nil {
		t.Fatal(err)
	}
}
func TestConcurrentResetMailRequestsKeepOnlyLatestToken(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)
	m := testMailer(t, s)
	ctx := context.Background()
	a, _ := verifiedAccount(t, s, "requests@example.test")
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() { errs <- m.RequestReset(ctx, a.Email) })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	for range 2 {
		if _, err := m.DeliverOne(ctx, senderFunc(func(context.Context, Mail) error { calls++; return nil })); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatal("stale reset message sent", calls)
	}
}

func TestMailKeyMismatchCanRecoverWithoutSending(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)
	correct := testMailer(t, s)
	ctx := context.Background()
	now := time.Now()
	s.now = func() time.Time { return now }
	if err := correct.Register(ctx, "key@example.test", testPassword, "workspace"); err != nil {
		t.Fatal(err)
	}
	wrong, err := NewAccountMail(s, bytes.Repeat([]byte{9}, 32), "https://portal.example.test", false)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	sender := senderFunc(func(context.Context, Mail) error { calls++; return nil })
	if _, err = wrong.DeliverOne(ctx, sender); err == nil || calls != 0 {
		t.Fatal("wrong key sent mail", err, calls)
	}
	now = now.Add(6 * time.Minute)
	if _, err = correct.DeliverOne(ctx, sender); err != nil || calls != 1 {
		t.Fatal("key recovery failed", err, calls)
	}
}
