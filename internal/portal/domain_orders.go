package portal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/domains"
)

var ErrDomainOrderConflict = errors.New("domain order quote changed or is no longer available")

type DomainOrder struct {
	ID          string        `json:"id"`
	QuoteID     string        `json:"quote_id"`
	WorkspaceID string        `json:"workspace_id"`
	Offer       domains.Offer `json:"offer"`
	State       string        `json:"state"`
	CreatedAt   int64         `json:"created_at"`
	ActorID     string        `json:"-"`
}

func scanDomainOrder(row interface{ Scan(...any) error }) (DomainOrder, error) {
	var order DomainOrder
	var raw []byte
	err := row.Scan(&order.ID, &order.QuoteID, &order.WorkspaceID, &order.ActorID, &raw, &order.State, &order.CreatedAt)
	if err != nil {
		return order, err
	}
	if err = json.Unmarshal(raw, &order.Offer); err != nil {
		return order, err
	}
	return order, nil
}

func domainOrderQuote(rawEvidence, rawOffer []byte, now time.Time) (domains.Offer, domains.RegistrarQuote, int64, error) {
	var evidence domains.RegistrarQuote
	var offer domains.Offer
	if json.Unmarshal(rawEvidence, &evidence) != nil || json.Unmarshal(rawOffer, &offer) != nil {
		return domains.Offer{}, domains.RegistrarQuote{}, 0, ErrDomainOrderConflict
	}
	name, err := domains.PurchaseName(offer.Domain)
	if err != nil || offer.Environment != evidence.Environment || (offer.Environment != "" && offer.Environment != "sandbox") || name != offer.Domain || evidence.Domain != offer.Domain || offer.Currency != evidence.Currency || offer.Years != 1 || offer.RegistrationMinor <= 0 || offer.RenewalMinor <= 0 || evidence.RegistrationMinor <= 0 || evidence.RenewalMinor <= 0 || !evidence.Available || evidence.Premium || !evidence.PremiumChecked || !offer.ExpiresAt.After(now) || !evidence.CheckedAt.After(now.Add(-5*time.Minute)) || evidence.CheckedAt.After(now) {
		return domains.Offer{}, domains.RegistrarQuote{}, 0, ErrDomainOrderConflict
	}
	markup := offer.RegistrationMinor - evidence.RegistrationMinor
	if markup < 0 || offer.RenewalMinor-evidence.RenewalMinor != markup || !offer.ExpiresAt.Equal(evidence.CheckedAt.Add(5*time.Minute)) {
		return domains.Offer{}, domains.RegistrarQuote{}, 0, ErrDomainOrderConflict
	}
	return offer, evidence, markup, nil
}

func (s *Store) RequestDomainOrder(ctx context.Context, p DomainQuoteReader, token, workspace, quoteID string) (DomainOrder, error) {
	if p == nil {
		return DomainOrder{}, ErrDenied
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DomainOrder{}, err
	}
	defer tx.Rollback()
	actor, err := s.authorizeOwner(ctx, tx, token, workspace)
	if err != nil {
		return DomainOrder{}, err
	}
	var existing DomainOrder
	existing, err = scanDomainOrder(tx.QueryRowContext(ctx, "SELECT id,quote_id,workspace_id,actor_id,offer,state,created_at FROM domain_orders WHERE quote_id=? AND workspace_id=?", quoteID, workspace))
	if err == nil {
		return existing, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return DomainOrder{}, err
	}
	var rawEvidence, rawOffer []byte
	if err = tx.QueryRowContext(ctx, "SELECT evidence,offer FROM domain_quotes WHERE id=? AND workspace_id=?", quoteID, workspace).Scan(&rawEvidence, &rawOffer); errors.Is(err, sql.ErrNoRows) {
		return DomainOrder{}, ErrDenied
	} else if err != nil {
		return DomainOrder{}, err
	}
	offer, _, markup, err := domainOrderQuote(rawEvidence, rawOffer, s.now())
	if err != nil {
		return DomainOrder{}, err
	}
	if err = tx.Commit(); err != nil {
		return DomainOrder{}, err
	}
	started := s.now()
	call, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	refreshed, err := p.QuoteDomain(call, offer.Domain)
	if err != nil {
		return DomainOrder{}, ErrDomainOrderConflict
	}
	newOffer, err := domains.OfferFor(refreshed, offer.Domain, markup, s.now())
	if err != nil || newOffer.Environment != offer.Environment || refreshed.CheckedAt.Before(started) || newOffer.Currency != offer.Currency || newOffer.RegistrationMinor != offer.RegistrationMinor || newOffer.RenewalMinor != offer.RenewalMinor {
		return DomainOrder{}, ErrDomainOrderConflict
	}
	tx, err = s.db.BeginTx(ctx, nil)
	if err != nil {
		return DomainOrder{}, err
	}
	defer tx.Rollback()
	actor, err = s.authorizeOwner(ctx, tx, token, workspace)
	if err != nil {
		return DomainOrder{}, err
	}
	if err = tx.QueryRowContext(ctx, "SELECT offer FROM domain_quotes WHERE id=? AND workspace_id=?", quoteID, workspace).Scan(&rawOffer); err != nil {
		return DomainOrder{}, ErrDomainOrderConflict
	}
	var original domains.Offer
	if json.Unmarshal(rawOffer, &original) != nil || !original.ExpiresAt.After(s.now()) || original.Environment != offer.Environment || original.Domain != offer.Domain || original.Currency != offer.Currency || original.RegistrationMinor != offer.RegistrationMinor || original.RenewalMinor != offer.RenewalMinor || original.Years != offer.Years || !original.ExpiresAt.Equal(offer.ExpiresAt) {
		return DomainOrder{}, ErrDomainOrderConflict
	}
	if err = tx.QueryRowContext(ctx, "SELECT id,quote_id,workspace_id,actor_id,offer,state,created_at FROM domain_orders WHERE quote_id=? AND workspace_id=?", quoteID, workspace).Scan(&existing.ID, &existing.QuoteID, &existing.WorkspaceID, &existing.ActorID, &rawOffer, &existing.State, &existing.CreatedAt); err == nil {
		if json.Unmarshal(rawOffer, &existing.Offer) != nil {
			return DomainOrder{}, ErrDomainOrderConflict
		}
		return existing, tx.Commit()
	} else if !errors.Is(err, sql.ErrNoRows) {
		return DomainOrder{}, err
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM domain_orders WHERE workspace_id=?", workspace).Scan(&count); err != nil {
		return DomainOrder{}, err
	}
	if count >= 100 {
		return DomainOrder{}, ErrDomainOrderConflict
	}
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM domain_orders WHERE workspace_id=? AND state='awaiting_payment' AND name=?", workspace, original.Domain).Scan(&count); err != nil {
		return DomainOrder{}, err
	}
	if count != 0 {
		return DomainOrder{}, ErrDomainOrderConflict
	}
	public, err := json.Marshal(original)
	if err != nil {
		return DomainOrder{}, err
	}
	order := DomainOrder{ID: randomToken(), QuoteID: quoteID, WorkspaceID: workspace, Offer: original, State: "awaiting_payment", CreatedAt: s.now().Unix(), ActorID: actor}
	if _, err = tx.ExecContext(ctx, "INSERT INTO domain_orders(id,quote_id,workspace_id,actor_id,name,offer,state,created_at) VALUES(?,?,?,?,?,?,?,?)", order.ID, order.QuoteID, order.WorkspaceID, order.ActorID, order.Offer.Domain, public, order.State, order.CreatedAt); err != nil {
		return DomainOrder{}, err
	}
	if err = audit(ctx, tx, actor, workspace, "domain.order_requested:"+order.ID, order.CreatedAt); err != nil {
		return DomainOrder{}, err
	}
	return order, tx.Commit()
}

func (s *Store) DomainOrders(ctx context.Context, token, workspace string) ([]DomainOrder, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = s.authorizeOwner(ctx, tx, token, workspace); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT id,quote_id,workspace_id,actor_id,offer,state,created_at FROM domain_orders WHERE workspace_id=? ORDER BY created_at DESC,id DESC LIMIT 100", workspace)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	orders := []DomainOrder{}
	for rows.Next() {
		order, scanErr := scanDomainOrder(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		orders = append(orders, order)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	return orders, tx.Commit()
}

func (s *Store) CancelDomainOrder(ctx context.Context, token, workspace, id string) (DomainOrder, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DomainOrder{}, err
	}
	defer tx.Rollback()
	actor, err := s.authorizeOwner(ctx, tx, token, workspace)
	if err != nil {
		return DomainOrder{}, err
	}
	order, err := scanDomainOrder(tx.QueryRowContext(ctx, "SELECT id,quote_id,workspace_id,actor_id,offer,state,created_at FROM domain_orders WHERE id=? AND workspace_id=?", id, workspace))
	if errors.Is(err, sql.ErrNoRows) {
		return DomainOrder{}, ErrDenied
	}
	if err != nil {
		return DomainOrder{}, err
	}
	if order.State == "canceled" {
		return order, tx.Commit()
	}
	if order.State != "awaiting_payment" {
		return order, ErrDomainOrderConflict
	}
	if _, err = tx.ExecContext(ctx, "UPDATE domain_orders SET state='canceled' WHERE id=? AND workspace_id=? AND state='awaiting_payment'", id, workspace); err != nil {
		return DomainOrder{}, err
	}
	order.State = "canceled"
	if err = audit(ctx, tx, actor, workspace, "domain.order_canceled:"+order.ID, s.now().Unix()); err != nil {
		return DomainOrder{}, err
	}
	return order, tx.Commit()
}
