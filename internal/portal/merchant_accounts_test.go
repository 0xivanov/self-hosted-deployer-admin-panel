//go:build integration

package portal

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/merchantbilling"
)

type merchantProviderFixture struct {
	create func(context.Context, string, string) (merchantbilling.Account, error)
	read   func(context.Context, string, string, string) (merchantbilling.Account, error)
}

func (p merchantProviderFixture) CreateAccount(ctx context.Context, country, request string) (merchantbilling.Account, error) {
	return p.create(ctx, country, request)
}
func (p merchantProviderFixture) RetrieveAccount(ctx context.Context, id, country, request string) (merchantbilling.Account, error) {
	return p.read(ctx, id, country, request)
}
func merchantSnapshot(id, country, request string) merchantbilling.Account {
	return merchantbilling.Account{ID: id, Country: country, RequestID: request, CardPayments: "pending", ObservedAt: time.Now().Unix()}
}

func TestMerchantDurableDispatchAndReconciliation(t *testing.T) {
	t.Parallel()
	s, path := newStore(t)
	owner, session := verifiedAccount(t, s, "merchant-owner@example.test")
	ctx := t.Context()
	intent, err := s.RequestMerchantAccount(ctx, session.Token, owner.WorkspaceID, "BG")
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := s.RequestMerchantAccount(ctx, session.Token, owner.WorkspaceID, "BG")
	if err != nil || repeated.RequestID != intent.RequestID {
		t.Fatal(repeated, err)
	}
	if _, err = s.RequestMerchantAccount(ctx, session.Token, owner.WorkspaceID, "US"); !errors.Is(err, ErrBillingConflict) {
		t.Fatal("changed country", err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	provider := merchantProviderFixture{create: func(ctx context.Context, country, request string) (merchantbilling.Account, error) {
		calls.Add(1)
		close(entered)
		<-release
		return merchantbilling.Account{}, errors.New("lost acknowledgement")
	}}
	result := make(chan error, 1)
	go func() { _, err := s.DispatchMerchantAccount(ctx, intent.RequestID, provider); result <- err }()
	<-entered
	if _, err = s.DispatchMerchantAccount(ctx, intent.RequestID, provider); !errors.Is(err, ErrBillingConflict) {
		t.Fatal("duplicate dispatch", err)
	}
	close(release)
	if err = <-result; err == nil {
		t.Fatal("lost reply was accepted")
	}
	if calls.Load() != 1 {
		t.Fatal("duplicate provider account", calls.Load())
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.DispatchMerchantAccount(ctx, intent.RequestID, provider); !errors.Is(err, ErrBillingConflict) {
		t.Fatal("restart resubmitted", err)
	}
	provider.read = func(_ context.Context, id, country, request string) (merchantbilling.Account, error) {
		return merchantSnapshot(id, country, request), nil
	}
	bound, err := s.ReconcileMerchantAccount(ctx, intent.RequestID, "acct_saved", provider)
	if err != nil || bound.State != "bound" || bound.AccountID != "acct_saved" {
		t.Fatal(bound, err)
	}
	if _, err = s.ReconcileMerchantAccount(ctx, intent.RequestID, "acct_replacement", provider); !errors.Is(err, ErrBillingConflict) {
		t.Fatal("replaced account", err)
	}
	view, err := s.MerchantAccount(ctx, session.Token, owner.WorkspaceID)
	if err != nil || view.State != "bound" {
		t.Fatal(view, err)
	}
	raw, _ := json.Marshal(view)
	for _, private := range []string{intent.RequestID, "acct_saved", owner.ID} {
		if strings.Contains(string(raw), private) {
			t.Fatal("private mapping exposed", private)
		}
	}
}

func TestMerchantOwnerAndBindingIsolation(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)
	ctx := t.Context()
	owner, session := verifiedAccount(t, s, "merchant-alice@example.test")
	other, foreign := verifiedAccount(t, s, "merchant-bob@example.test")
	a, err := s.RequestMerchantAccount(ctx, session.Token, owner.WorkspaceID, "BG")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.MerchantAccount(ctx, foreign.Token, owner.WorkspaceID); !errors.Is(err, ErrDenied) {
		t.Fatal("foreign read", err)
	}
	if _, err = s.RequestMerchantAccount(ctx, foreign.Token, owner.WorkspaceID, "BG"); !errors.Is(err, ErrDenied) {
		t.Fatal("foreign request", err)
	}
	if _, err = s.db.Exec("INSERT INTO memberships VALUES(?,?,'developer')", other.ID, owner.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.MerchantAccount(ctx, foreign.Token, owner.WorkspaceID); !errors.Is(err, ErrDenied) {
		t.Fatal("developer read", err)
	}
	provider := merchantProviderFixture{create: func(_ context.Context, country, request string) (merchantbilling.Account, error) {
		return merchantSnapshot("acct_shared", country, request), nil
	}}
	if _, err = s.DispatchMerchantAccount(ctx, a.RequestID, provider); err != nil {
		t.Fatal(err)
	}
	b, err := s.RequestMerchantAccount(ctx, foreign.Token, other.WorkspaceID, "BG")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DispatchMerchantAccount(ctx, b.RequestID, provider); err == nil {
		t.Fatal("account shared between workspaces")
	}
}

func TestMerchantRevokedOwnerCannotDispatch(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)
	ctx := t.Context()
	owner, session := verifiedAccount(t, s, "merchant-revoked@example.test")
	intent, err := s.RequestMerchantAccount(ctx, session.Token, owner.WorkspaceID, "BG")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("UPDATE memberships SET role='viewer' WHERE user_id=?", owner.ID); err != nil {
		t.Fatal(err)
	}
	p := merchantProviderFixture{create: func(context.Context, string, string) (merchantbilling.Account, error) {
		t.Fatal("revoked owner reached provider")
		return merchantbilling.Account{}, nil
	}}
	if _, err = s.DispatchMerchantAccount(ctx, intent.RequestID, p); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
}

func TestMerchantReconciliationRejectsWrongCandidate(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)
	ctx := t.Context()
	owner, session := verifiedAccount(t, s, "merchant-candidate@example.test")
	intent, err := s.RequestMerchantAccount(ctx, session.Token, owner.WorkspaceID, "BG")
	if err != nil {
		t.Fatal(err)
	}
	provider := merchantProviderFixture{create: func(context.Context, string, string) (merchantbilling.Account, error) {
		return merchantbilling.Account{}, errors.New("unknown outcome")
	}}
	if _, err = s.DispatchMerchantAccount(ctx, intent.RequestID, provider); err == nil {
		t.Fatal("unknown outcome")
	}
	for _, kind := range []string{"account", "country", "request", "old"} {
		t.Run(kind, func(t *testing.T) {
			provider.read = func(_ context.Context, id, country, request string) (merchantbilling.Account, error) {
				value := merchantSnapshot(id, country, request)
				switch kind {
				case "account":
					value.ID = "acct_foreign"
				case "country":
					value.Country = "US"
				case "request":
					value.RequestID = strings.Repeat("f", 64)
				case "old":
					value.ObservedAt -= 3600
				}
				return value, nil
			}
			if _, err = s.ReconcileMerchantAccount(ctx, intent.RequestID, "acct_candidate", provider); !errors.Is(err, ErrBillingConflict) {
				t.Fatal("invalid observation accepted", err)
			}
		})
	}
	account, err := s.MerchantAccount(ctx, session.Token, owner.WorkspaceID)
	if err != nil || account.State != "submitted" || account.AccountID != "" {
		t.Fatal(account, err)
	}
}
