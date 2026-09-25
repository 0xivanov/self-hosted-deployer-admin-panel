package portal

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

var ErrMerchantProductConflict = errors.New("merchant product changed or request conflicts")

type MerchantProduct struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	Name        string `json:"name"`
	Currency    string `json:"currency"`
	AmountMinor int64  `json:"amount_minor"`
	Active      bool   `json:"active"`
	Revision    int64  `json:"revision"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

type MerchantProductInput struct {
	Workspace   string `json:"workspace"`
	ID          string `json:"id"`
	Key         string `json:"key"`
	Name        string `json:"name"`
	Currency    string `json:"currency"`
	AmountMinor int64  `json:"amount_minor"`
	Active      *bool  `json:"active"`
	Revision    int64  `json:"revision"`
}

const merchantProductColumns = "id,workspace_id,name,currency,amount_minor,active,revision,created_at,updated_at"

func scanMerchantProduct(row interface{ Scan(...any) error }) (MerchantProduct, error) {
	var p MerchantProduct
	err := row.Scan(&p.ID, &p.WorkspaceID, &p.Name, &p.Currency, &p.AmountMinor, &p.Active, &p.Revision, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

func (s *Store) MerchantProducts(ctx context.Context, token, workspace string) ([]MerchantProduct, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	mode := s.merchantModeValue()
	if _, err = s.authorizeOwner(ctx, tx, token, workspace); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT "+merchantProductColumns+" FROM merchant_products WHERE mode=? AND workspace_id=? ORDER BY created_at,id LIMIT 100", mode, workspace)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	products := []MerchantProduct{}
	for rows.Next() {
		p, err := scanMerchantProduct(rows)
		if err != nil {
			return nil, err
		}
		products = append(products, p)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	return products, tx.Commit()
}

// SaveMerchantProduct persists owner prices in integer minor units. Edits require
// the last read revision and never overwrite another editor's saved changes.
// Disabling preserves references needed by future order history.
func (s *Store) SaveMerchantProduct(ctx context.Context, token string, input MerchantProductInput) (MerchantProduct, error) {
	if input.Active == nil || input.Name != strings.TrimSpace(input.Name) || !utf8.ValidString(input.Name) || len(input.Name) < 1 || len(input.Name) > 120 || strings.IndexFunc(input.Name, unicode.IsControl) >= 0 || (input.Currency != "eur" && input.Currency != "usd" && input.Currency != "gbp") || input.AmountMinor < 50 || input.AmountMinor > 99999999 {
		return MerchantProduct{}, ErrInvalid
	}
	if input.ID == "" {
		if input.Revision != 0 || len(input.Key) < 16 || len(input.Key) > 128 || strings.IndexFunc(input.Key, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-')
		}) >= 0 {
			return MerchantProduct{}, ErrInvalid
		}
	} else if input.Key != "" || input.Revision < 1 {
		return MerchantProduct{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MerchantProduct{}, err
	}
	defer tx.Rollback()
	mode := s.merchantModeValue()
	actor, err := s.authorizeOwner(ctx, tx, token, input.Workspace)
	if err != nil {
		return MerchantProduct{}, err
	}
	if input.ID == "" {
		existing, err := scanMerchantProduct(tx.QueryRowContext(ctx, "SELECT "+merchantProductColumns+" FROM merchant_products WHERE mode=? AND workspace_id=? AND request_key=?", mode, input.Workspace, input.Key))
		if err == nil {
			if existing.Name != input.Name || existing.Currency != input.Currency || existing.AmountMinor != input.AmountMinor || existing.Active != *input.Active {
				return MerchantProduct{}, ErrMerchantProductConflict
			}
			return existing, tx.Commit()
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return MerchantProduct{}, err
		}
		var count int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM merchant_products WHERE mode=? AND workspace_id=?", mode, input.Workspace).Scan(&count); err != nil {
			return MerchantProduct{}, err
		}
		if count >= 100 {
			return MerchantProduct{}, ErrMerchantProductConflict
		}
		now := s.now().Unix()
		product := MerchantProduct{ID: randomToken(), WorkspaceID: input.Workspace, Name: input.Name, Currency: input.Currency, AmountMinor: input.AmountMinor, Active: *input.Active, Revision: 1, CreatedAt: now, UpdatedAt: now}
		if _, err = tx.ExecContext(ctx, "INSERT INTO merchant_products(mode,id,workspace_id,request_key,name,currency,amount_minor,active,revision,created_at,updated_at) VALUES(?,?,?,?,?,?,?, ?,1,?,?)", mode, product.ID, product.WorkspaceID, input.Key, product.Name, product.Currency, product.AmountMinor, product.Active, now, now); err != nil {
			return MerchantProduct{}, err
		}
		if err = audit(ctx, tx, actor, input.Workspace, "merchant.product_created:"+product.ID, now); err != nil {
			return MerchantProduct{}, err
		}
		return product, tx.Commit()
	}
	product, err := scanMerchantProduct(tx.QueryRowContext(ctx, "SELECT "+merchantProductColumns+" FROM merchant_products WHERE mode=? AND id=? AND workspace_id=?", mode, input.ID, input.Workspace))
	if errors.Is(err, sql.ErrNoRows) {
		return MerchantProduct{}, ErrDenied
	}
	if err != nil {
		return MerchantProduct{}, err
	}
	if product.Revision != input.Revision {
		return MerchantProduct{}, ErrMerchantProductConflict
	}
	now := s.now().Unix()
	result, err := tx.ExecContext(ctx, "UPDATE merchant_products SET name=?,currency=?,amount_minor=?,active=?,revision=revision+1,updated_at=? WHERE mode=? AND id=? AND workspace_id=? AND revision=?", input.Name, input.Currency, input.AmountMinor, *input.Active, now, mode, input.ID, input.Workspace, input.Revision)
	if err != nil {
		return MerchantProduct{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return MerchantProduct{}, err
	}
	if count != 1 {
		return MerchantProduct{}, ErrMerchantProductConflict
	}
	product.Name = input.Name
	product.Currency = input.Currency
	product.AmountMinor = input.AmountMinor
	product.Active = *input.Active
	product.Revision++
	product.UpdatedAt = now
	if err = audit(ctx, tx, actor, input.Workspace, "merchant.product_updated:"+product.ID, product.UpdatedAt); err != nil {
		return MerchantProduct{}, err
	}
	return product, tx.Commit()
}
