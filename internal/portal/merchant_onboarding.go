package portal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/merchantbilling"
)

var ErrMerchantRateLimited = errors.New("merchant request limit reached")

type MerchantProvider interface {
	MerchantAccountProvider
	CreateOnboardingLink(context.Context, string, string) (merchantbilling.OnboardingLink, error)
}

type MerchantView struct {
	State            string `json:"state"`
	Country          string `json:"country"`
	DetailsSubmitted bool   `json:"details_submitted"`
	ChargesEnabled   bool   `json:"charges_enabled"`
	PayoutsEnabled   bool   `json:"payouts_enabled"`
	CardPayments     string `json:"card_payments"`
	ObservedAt       int64  `json:"observed_at"`
	Stale            bool   `json:"stale"`
}

func (s *Store) MerchantStatus(ctx context.Context, token, workspace string) (MerchantView, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MerchantView{}, err
	}
	defer tx.Rollback()
	if _, err = s.authorizeOwner(ctx, tx, token, workspace); err != nil {
		return MerchantView{}, err
	}
	account, err := scanMerchantAccount(tx.QueryRowContext(ctx, "SELECT "+merchantAccountColumns+" FROM merchant_accounts WHERE workspace_id=?", workspace))
	if errors.Is(err, sql.ErrNoRows) {
		return MerchantView{State: "not_started", Stale: true}, tx.Commit()
	}
	if err != nil {
		return MerchantView{}, err
	}
	view := MerchantView{State: account.State, Country: account.Country, Stale: true}
	if a := account.Snapshot; a != nil {
		if a.ID != account.AccountID || a.Country != account.Country || a.RequestID != account.RequestID {
			return MerchantView{}, ErrBillingConflict
		}
		view.DetailsSubmitted = a.DetailsSubmitted
		view.ChargesEnabled = a.ChargesEnabled
		view.PayoutsEnabled = a.PayoutsEnabled
		view.CardPayments = a.CardPayments
		view.ObservedAt = a.ObservedAt
		now := s.now().Unix()
		view.Stale = a.ObservedAt <= 0 || a.ObservedAt > now || now-a.ObservedAt >= 300
	}
	return view, tx.Commit()
}

// merchantActionIntent limits provider requests per owner across workspaces and
// persists intent before external work. Link credentials never enter the audit.
func (s *Store) merchantActionIntent(ctx context.Context, tx *sql.Tx, actor, workspace, action string) (string, error) {
	var count int
	now := s.now().Unix()
	err := tx.QueryRowContext(ctx, "SELECT count(*) FROM audit_events WHERE actor_id=? AND action LIKE 'merchant.provider.%' AND created_at>=?", actor, now-60).Scan(&count)
	if err != nil {
		return "", err
	}
	if count >= 5 {
		return "", ErrMerchantRateLimited
	}
	request := randomToken()
	if err = audit(ctx, tx, actor, workspace, "merchant.provider."+action+":"+request, now); err != nil {
		return "", err
	}
	return request, nil
}

func (s *Store) MerchantOnboardingLink(ctx context.Context, token, workspace string, p MerchantProvider) (merchantbilling.OnboardingLink, error) {
	if p == nil {
		return merchantbilling.OnboardingLink{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return merchantbilling.OnboardingLink{}, err
	}
	defer tx.Rollback()
	actor, err := s.authorizeOwner(ctx, tx, token, workspace)
	if err != nil {
		return merchantbilling.OnboardingLink{}, err
	}
	a, err := scanMerchantAccount(tx.QueryRowContext(ctx, "SELECT "+merchantAccountColumns+" FROM merchant_accounts WHERE workspace_id=?", workspace))
	if err != nil {
		return merchantbilling.OnboardingLink{}, ErrDenied
	}
	if a.State != "bound" {
		return merchantbilling.OnboardingLink{}, ErrBillingConflict
	}
	request, err := s.merchantActionIntent(ctx, tx, actor, workspace, "onboarding")
	if err != nil {
		return merchantbilling.OnboardingLink{}, err
	}
	if err = tx.Commit(); err != nil {
		return merchantbilling.OnboardingLink{}, err
	}
	call, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	link, err := p.CreateOnboardingLink(call, a.AccountID, request)
	if err != nil {
		return merchantbilling.OnboardingLink{}, errors.New("merchant onboarding unavailable")
	}
	u, e := url.Parse(link.URL)
	now := s.now().Unix()
	if e != nil || u.Scheme != "https" || u.Host != "connect.stripe.com" || u.User != nil || u.Fragment != "" || link.ExpiresAt <= now || link.ExpiresAt > now+3600 {
		return merchantbilling.OnboardingLink{}, ErrBillingConflict
	}
	tx, err = s.db.BeginTx(ctx, nil)
	if err != nil {
		return merchantbilling.OnboardingLink{}, err
	}
	defer tx.Rollback()
	if _, err = s.authorizeOwner(ctx, tx, token, workspace); err != nil {
		return merchantbilling.OnboardingLink{}, err
	}
	var bound string
	if err = tx.QueryRowContext(ctx, "SELECT account_id FROM merchant_accounts WHERE workspace_id=? AND state='bound'", workspace).Scan(&bound); err != nil || bound != a.AccountID {
		return merchantbilling.OnboardingLink{}, ErrBillingConflict
	}
	if err = audit(ctx, tx, actor, workspace, "merchant.onboarding_issued:"+request, now); err != nil {
		return merchantbilling.OnboardingLink{}, err
	}
	return link, tx.Commit()
}

// RefreshMerchantAccount fences older provider reads before the request starts.
// It records provider evidence, never activates website checkout or fulfillment.
func (s *Store) RefreshMerchantAccount(ctx context.Context, token, workspace string, p MerchantAccountProvider) error {
	if p == nil {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	actor, err := s.authorizeOwner(ctx, tx, token, workspace)
	if err != nil {
		return err
	}
	a, err := scanMerchantAccount(tx.QueryRowContext(ctx, "SELECT "+merchantAccountColumns+" FROM merchant_accounts WHERE workspace_id=?", workspace))
	if err != nil {
		return ErrDenied
	}
	if a.State != "bound" {
		return ErrBillingConflict
	}
	if _, err = s.merchantActionIntent(ctx, tx, actor, workspace, "refresh"); err != nil {
		return err
	}
	var generation int64
	if err = tx.QueryRowContext(ctx, "UPDATE merchant_accounts SET observation_generation=observation_generation+1 WHERE workspace_id=? RETURNING observation_generation", workspace).Scan(&generation); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	started := s.now().Unix()
	call, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	snapshot, err := p.RetrieveAccount(call, a.AccountID, a.Country, a.RequestID)
	if err != nil {
		return errors.New("merchant account status unavailable")
	}
	if snapshot.ID != a.AccountID || snapshot.Country != a.Country || snapshot.RequestID != a.RequestID || snapshot.ObservedAt < started || snapshot.ObservedAt > s.now().Unix() {
		return ErrBillingConflict
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	tx, err = s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = s.authorizeOwner(ctx, tx, token, workspace); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, "UPDATE merchant_accounts SET snapshot=? WHERE workspace_id=? AND account_id=? AND observation_generation=? AND state='bound'", raw, workspace, a.AccountID, generation)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrBillingConflict
	}
	return tx.Commit()
}
