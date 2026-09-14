package portal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/merchantbilling"
)

// MerchantAccount records a workspace's persisted Connect account intent and
// the provider acknowledgement. Snapshot is retained for trusted workers and
// is never exposed by the customer-facing JSON representation.
type MerchantAccount struct {
	WorkspaceID string                   `json:"workspace_id"`
	RequestID   string                   `json:"-"`
	ActorID     string                   `json:"-"`
	Country     string                   `json:"country"`
	AccountID   string                   `json:"-"`
	State       string                   `json:"state"`
	CreatedAt   int64                    `json:"created_at"`
	Snapshot    *merchantbilling.Account `json:"-"`
}

// MerchantAccountProvider is the only authority allowed to create or read a
// provider account. The request identity is sent to the provider on every call.
type MerchantAccountProvider interface {
	CreateAccount(context.Context, string, string) (merchantbilling.Account, error)
	RetrieveAccount(context.Context, string, string, string) (merchantbilling.Account, error)
}

const merchantAccountColumns = "workspace_id,request_id,actor_id,country,state,COALESCE(account_id,''),created_at,submitted_at,COALESCE(snapshot,x'')"

func scanMerchantAccount(row interface{ Scan(...any) error }) (MerchantAccount, error) {
	var account MerchantAccount
	var snapshot []byte
	var submittedAt int64
	if err := row.Scan(&account.WorkspaceID, &account.RequestID, &account.ActorID, &account.Country, &account.State, &account.AccountID, &account.CreatedAt, &submittedAt, &snapshot); err != nil {
		return account, err
	}
	if len(snapshot) != 0 {
		var value merchantbilling.Account
		if err := json.Unmarshal(snapshot, &value); err != nil {
			return account, err
		}
		account.Snapshot = &value
	}
	return account, nil
}

func validMerchantCountry(country string) bool {
	return len(country) == 2 && country == strings.ToUpper(country) && country[0] >= 'A' && country[0] <= 'Z' && country[1] >= 'A' && country[1] <= 'Z'
}

func validMerchantAccountID(id string) bool {
	if len(id) <= 5 || len(id) > 255 || !strings.HasPrefix(id, "acct_") {
		return false
	}
	for _, r := range id {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_') {
			return false
		}
	}
	return true
}

func (s *Store) RequestMerchantAccount(ctx context.Context, token, workspace, country string) (MerchantAccount, error) {
	if !validMerchantCountry(country) {
		return MerchantAccount{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MerchantAccount{}, err
	}
	defer tx.Rollback()
	actor, err := s.authorizeOwner(ctx, tx, token, workspace)
	if err != nil {
		return MerchantAccount{}, err
	}
	existing, err := scanMerchantAccount(tx.QueryRowContext(ctx, "SELECT "+merchantAccountColumns+" FROM merchant_accounts WHERE workspace_id=?", workspace))
	if err == nil {
		if existing.Country != country {
			return MerchantAccount{}, ErrBillingConflict
		}
		return existing, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return MerchantAccount{}, err
	}
	account := MerchantAccount{WorkspaceID: workspace, RequestID: randomToken(), ActorID: actor, Country: country, State: "requested", CreatedAt: s.now().Unix()}
	if _, err = tx.ExecContext(ctx, "INSERT INTO merchant_accounts(workspace_id,request_id,actor_id,country,state,created_at) VALUES(?,?,?,?,?,?)", account.WorkspaceID, account.RequestID, account.ActorID, account.Country, account.State, account.CreatedAt); err != nil {
		return MerchantAccount{}, err
	}
	if err = audit(ctx, tx, actor, workspace, "merchant_account.requested:"+account.RequestID, account.CreatedAt); err != nil {
		return MerchantAccount{}, err
	}
	return account, tx.Commit()
}

func (s *Store) MerchantAccount(ctx context.Context, token, workspace string) (MerchantAccount, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MerchantAccount{}, err
	}
	defer tx.Rollback()
	if _, err = s.authorizeOwner(ctx, tx, token, workspace); err != nil {
		return MerchantAccount{}, err
	}
	account, err := scanMerchantAccount(tx.QueryRowContext(ctx, "SELECT "+merchantAccountColumns+" FROM merchant_accounts WHERE workspace_id=?", workspace))
	if errors.Is(err, sql.ErrNoRows) {
		return MerchantAccount{}, ErrDenied
	}
	if err != nil {
		return MerchantAccount{}, err
	}
	return account, tx.Commit()
}

// DispatchMerchantAccount marks the request submitted before calling the
// provider. A failed or uncertain create remains submitted and must be
// reconciled by request identity rather than creating a replacement.
func (s *Store) DispatchMerchantAccount(ctx context.Context, request string, provider MerchantAccountProvider) (MerchantAccount, error) {
	if provider == nil {
		return MerchantAccount{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MerchantAccount{}, err
	}
	defer tx.Rollback()
	account, err := scanMerchantAccount(tx.QueryRowContext(ctx, "SELECT "+merchantAccountColumns+" FROM merchant_accounts WHERE request_id=?", request))
	if errors.Is(err, sql.ErrNoRows) {
		return MerchantAccount{}, ErrDenied
	}
	if err != nil {
		return MerchantAccount{}, err
	}
	if account.State == "bound" {
		return account, tx.Commit()
	}
	if account.State != "requested" {
		return MerchantAccount{}, ErrBillingConflict
	}
	var allowed int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM users u JOIN memberships m ON m.user_id=u.id AND m.workspace_id=? WHERE u.id=? AND u.verified=1 AND u.disabled=0 AND m.role='owner'", account.WorkspaceID, account.ActorID).Scan(&allowed); err != nil {
		return MerchantAccount{}, err
	}
	if allowed != 1 {
		return MerchantAccount{}, ErrDenied
	}
	submittedAt := s.now().Unix()
	if _, err = tx.ExecContext(ctx, "UPDATE merchant_accounts SET state='submitted',submitted_at=? WHERE request_id=? AND state='requested'", submittedAt, request); err != nil {
		return MerchantAccount{}, err
	}
	if err = audit(ctx, tx, account.ActorID, account.WorkspaceID, "merchant_account.submitted:"+request, submittedAt); err != nil {
		return MerchantAccount{}, err
	}
	if err = tx.Commit(); err != nil {
		return MerchantAccount{}, err
	}

	call, cancel := context.WithTimeout(ctx, 30*time.Second)
	providerAccount, providerErr := provider.CreateAccount(call, account.Country, account.RequestID)
	cancel()
	if providerErr != nil {
		account.State = "submitted"
		return account, providerErr
	}
	return s.bindMerchantAccount(ctx, account.RequestID, providerAccount)
}

// ReconcileMerchantAccount retrieves a submitted candidate using trusted provider
// evidence. Bound mappings are immutable; this method does not refresh readiness.
func (s *Store) ReconcileMerchantAccount(ctx context.Context, request, accountID string, provider MerchantAccountProvider) (MerchantAccount, error) {
	if provider == nil || !validMerchantAccountID(accountID) {
		return MerchantAccount{}, ErrInvalid
	}
	account, err := scanMerchantAccount(s.db.QueryRowContext(ctx, "SELECT "+merchantAccountColumns+" FROM merchant_accounts WHERE request_id=?", request))
	if errors.Is(err, sql.ErrNoRows) {
		return MerchantAccount{}, ErrDenied
	}
	if err != nil {
		return MerchantAccount{}, err
	}
	if account.State == "bound" {
		if account.AccountID != accountID {
			return MerchantAccount{}, ErrBillingConflict
		}
		return account, nil
	}
	if account.State != "submitted" {
		return MerchantAccount{}, ErrBillingConflict
	}
	started := s.now().Unix()
	call, cancel := context.WithTimeout(ctx, 30*time.Second)
	providerAccount, providerErr := provider.RetrieveAccount(call, accountID, account.Country, account.RequestID)
	cancel()
	if providerErr != nil {
		return account, providerErr
	}
	if providerAccount.ID != accountID || providerAccount.ObservedAt < started {
		return MerchantAccount{}, ErrBillingConflict
	}
	return s.bindMerchantAccount(ctx, request, providerAccount)
}

func (s *Store) bindMerchantAccount(ctx context.Context, request string, providerAccount merchantbilling.Account) (MerchantAccount, error) {
	if !validMerchantAccountID(providerAccount.ID) || providerAccount.Country == "" || providerAccount.RequestID != request || providerAccount.ObservedAt <= 0 {
		return MerchantAccount{}, ErrBillingConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MerchantAccount{}, err
	}
	defer tx.Rollback()
	account, err := scanMerchantAccount(tx.QueryRowContext(ctx, "SELECT "+merchantAccountColumns+" FROM merchant_accounts WHERE request_id=?", request))
	if errors.Is(err, sql.ErrNoRows) {
		return MerchantAccount{}, ErrDenied
	}
	if err != nil {
		return MerchantAccount{}, err
	}
	if account.State == "bound" {
		if account.AccountID != providerAccount.ID {
			return MerchantAccount{}, ErrBillingConflict
		}
		return account, tx.Commit()
	}
	var submittedAt int64
	if err = tx.QueryRowContext(ctx, "SELECT submitted_at FROM merchant_accounts WHERE request_id=?", account.RequestID).Scan(&submittedAt); err != nil {
		return MerchantAccount{}, err
	}
	if account.State != "submitted" || providerAccount.Country != account.Country || providerAccount.ObservedAt < submittedAt || providerAccount.ObservedAt > s.now().Unix() {
		return MerchantAccount{}, ErrBillingConflict
	}
	var used int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM merchant_accounts WHERE account_id=? AND request_id!=?", providerAccount.ID, request).Scan(&used); err != nil {
		return MerchantAccount{}, err
	}
	if used != 0 {
		return MerchantAccount{}, ErrBillingConflict
	}
	snapshot, err := json.Marshal(providerAccount)
	if err != nil {
		return MerchantAccount{}, err
	}
	result, err := tx.ExecContext(ctx, "UPDATE merchant_accounts SET account_id=?,state='bound',snapshot=? WHERE request_id=? AND state='submitted' AND account_id IS NULL", providerAccount.ID, snapshot, request)
	if err != nil {
		return MerchantAccount{}, err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return MerchantAccount{}, ErrBillingConflict
	}
	account.AccountID, account.State, account.Snapshot = providerAccount.ID, "bound", &providerAccount
	if err = audit(ctx, tx, account.ActorID, account.WorkspaceID, "merchant_account.bound:"+request, providerAccount.ObservedAt); err != nil {
		return MerchantAccount{}, err
	}
	return account, tx.Commit()
}
