package portal

import (
	"context"
	"encoding/json"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/merchantbilling"
)

type MerchantMaintenanceProvider interface {
	MerchantAccountProvider
	MerchantCheckoutProvider
}

type MerchantMaintenanceResult struct {
	AccountsChecked int    `json:"accounts_checked"`
	OrdersChecked   int    `json:"orders_checked"`
	Failures        int    `json:"failures"`
	AccountCursor   string `json:"account_cursor"`
	OrderCursor     string `json:"order_cursor"`
}

type maintenanceAccount struct {
	Account    MerchantAccount
	Generation int64
}

func (s *Store) readMaintenanceAccounts(ctx context.Context, cursor string, limit int) ([]maintenanceAccount, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+merchantAccountColumns+",observation_generation FROM merchant_accounts WHERE state='bound' AND workspace_id>? ORDER BY workspace_id LIMIT ?", cursor, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	accounts := []maintenanceAccount{}
	for rows.Next() {
		var a MerchantAccount
		var submitted int64
		var snapshot []byte
		var generation int64
		if err = rows.Scan(&a.WorkspaceID, &a.RequestID, &a.ActorID, &a.Country, &a.State, &a.AccountID, &a.CreatedAt, &submitted, &snapshot, &generation); err != nil {
			return nil, err
		}
		if len(snapshot) != 0 {
			var value merchantbilling.Account
			if err = json.Unmarshal(snapshot, &value); err != nil {
				return nil, err
			}
			a.Snapshot = &value
		}
		accounts = append(accounts, maintenanceAccount{Account: a, Generation: generation})
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return accounts, rows.Close()
}

func (s *Store) refreshMappedMerchantAccount(ctx context.Context, item maintenanceAccount, provider MerchantMaintenanceProvider) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, "UPDATE merchant_accounts SET observation_generation=observation_generation+1 WHERE workspace_id=? AND state='bound' AND account_id=? AND observation_generation=?", item.Account.WorkspaceID, item.Account.AccountID, item.Generation)
	if err != nil {
		tx.Rollback()
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		tx.Rollback()
		return err
	}
	if changed != 1 {
		tx.Rollback()
		return ErrBillingConflict
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	started := s.now().Unix()
	call, cancel := context.WithTimeout(ctx, 30*time.Second)
	observed, providerErr := provider.RetrieveAccount(call, item.Account.AccountID, item.Account.Country, item.Account.RequestID)
	cancel()
	if providerErr != nil {
		return providerErr
	}
	now := s.now().Unix()
	if observed.ID != item.Account.AccountID || observed.Country != item.Account.Country || observed.RequestID != item.Account.RequestID || observed.ObservedAt < started || observed.ObservedAt > now {
		return ErrBillingConflict
	}
	snapshot, err := json.Marshal(observed)
	if err != nil {
		return err
	}
	tx, err = s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err = tx.ExecContext(ctx, "UPDATE merchant_accounts SET snapshot=? WHERE workspace_id=? AND state='bound' AND account_id=? AND observation_generation=?", snapshot, item.Account.WorkspaceID, item.Account.AccountID, item.Generation+1)
	if err != nil {
		return err
	}
	changed, err = result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrBillingConflict
	}
	previous := item.Account.Snapshot
	if previous == nil || previous.DetailsSubmitted != observed.DetailsSubmitted || previous.ChargesEnabled != observed.ChargesEnabled || previous.PayoutsEnabled != observed.PayoutsEnabled || previous.CardPayments != observed.CardPayments {
		if err = audit(ctx, tx, item.Account.ActorID, item.Account.WorkspaceID, "merchant.capabilities_updated", observed.ObservedAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) MaintainMerchants(ctx context.Context, provider MerchantMaintenanceProvider, accountCursor, orderCursor string, limit int) (MerchantMaintenanceResult, error) {
	result := MerchantMaintenanceResult{}
	if err := validateMerchantProviderMode(provider); err != nil {
		return result, err
	}
	if limit < 1 || limit > 100 {
		return result, ErrInvalid
	}
	accounts, err := s.readMaintenanceAccounts(ctx, accountCursor, limit)
	if err != nil {
		return result, err
	}
	for _, item := range accounts {
		result.AccountsChecked++
		if err = s.refreshMappedMerchantAccount(ctx, item, provider); err != nil {
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
			result.Failures++
		}
		result.AccountCursor = item.Account.WorkspaceID
	}
	if len(accounts) == limit && len(accounts) > 0 {
		result.AccountCursor = accounts[len(accounts)-1].Account.WorkspaceID
	} else {
		result.AccountCursor = ""
	}
	rows, err := s.db.QueryContext(ctx, "SELECT "+merchantOrderColumns+" FROM merchant_orders WHERE session_id IS NOT NULL AND session_id<>'' AND state IN ('open','complete') AND payment_status!='paid' AND id>? ORDER BY id LIMIT ?", orderCursor, limit)
	if err != nil {
		return result, err
	}
	orders := []MerchantOrder{}
	for rows.Next() {
		order, _, _, _, scanErr := scanMerchantOrder(rows)
		if scanErr != nil {
			rows.Close()
			return result, scanErr
		}
		orders = append(orders, order)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return result, err
	}
	if err = rows.Close(); err != nil {
		return result, err
	}
	for _, order := range orders {
		result.OrdersChecked++
		if _, err = s.ReconcileMerchantOrder(ctx, order.ID, order.SessionID, provider); err != nil {
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
			result.Failures++
		}
		result.OrderCursor = order.ID
	}
	if len(orders) == limit && len(orders) > 0 {
		result.OrderCursor = orders[len(orders)-1].ID
	} else {
		result.OrderCursor = ""
	}
	return result, nil
}
