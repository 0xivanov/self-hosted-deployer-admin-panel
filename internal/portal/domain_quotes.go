package portal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/domains"
)

// DomainQuoteReader is trusted registrar access. Prices never come from browsers.
type DomainQuoteReader interface {
	QuoteDomain(context.Context, string) (domains.RegistrarQuote, error)
}

type DomainQuote struct {
	ID          string        `json:"id"`
	WorkspaceID string        `json:"workspace_id"`
	Offer       domains.Offer `json:"offer"`
	Expired     bool          `json:"expired"`
}

var ErrDomainQuoteLimit = errors.New("saved domain quote limit reached")

// RequestDomainQuote snapshots a one-year retail offer and its private provider
// evidence. Markup is operator configuration, not a customer request parameter.
// A saved quote does not reserve a domain or authorize a purchase.
func (s *Store) RequestDomainQuote(ctx context.Context, p DomainQuoteReader, token, workspace, name string, markupMinor int64) (DomainQuote, error) {
	if p == nil {
		return DomainQuote{}, ErrDenied
	}
	name, err := domains.PurchaseName(name)
	if err != nil {
		return DomainQuote{}, err
	}
	if markupMinor < 0 {
		return DomainQuote{}, domains.ErrQuote
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DomainQuote{}, err
	}
	defer tx.Rollback()
	if _, err = s.authorizeOwner(ctx, tx, token, workspace); err != nil {
		return DomainQuote{}, err
	}
	if err = domainQuoteCapacity(ctx, tx, workspace); err != nil {
		return DomainQuote{}, err
	}
	if err = tx.Commit(); err != nil {
		return DomainQuote{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	evidence, err := p.QuoteDomain(ctx, name)
	if err != nil {
		return DomainQuote{}, err
	}
	tx, err = s.db.BeginTx(ctx, nil)
	if err != nil {
		return DomainQuote{}, err
	}
	defer tx.Rollback()
	actor, err := s.authorizeOwner(ctx, tx, token, workspace)
	if err != nil {
		return DomainQuote{}, err
	}
	if err = domainQuoteCapacity(ctx, tx, workspace); err != nil {
		return DomainQuote{}, err
	}
	offer, err := domains.OfferFor(evidence, name, markupMinor, s.now())
	if err != nil {
		return DomainQuote{}, err
	}
	raw, err := json.Marshal(evidence)
	if err != nil {
		return DomainQuote{}, err
	}
	public, err := json.Marshal(offer)
	if err != nil {
		return DomainQuote{}, err
	}
	q := DomainQuote{ID: randomToken(), WorkspaceID: workspace, Offer: offer}
	if _, err = tx.ExecContext(ctx, "INSERT INTO domain_quotes(id,workspace_id,actor_id,evidence,offer,created_at) VALUES(?,?,?,?,?,?)", q.ID, workspace, actor, raw, public, s.now().Unix()); err != nil {
		return DomainQuote{}, err
	}
	if err = audit(ctx, tx, actor, workspace, "domain.quoted:"+q.ID, s.now().Unix()); err != nil {
		return DomainQuote{}, err
	}
	return q, tx.Commit()
}

func domainQuoteCapacity(ctx context.Context, tx *sql.Tx, workspace string) error {
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM domain_quotes WHERE workspace_id=?", workspace).Scan(&count); err != nil {
		return err
	}
	if count >= 1000 {
		return ErrDomainQuoteLimit
	}
	return nil
}

// DomainQuote returns the immutable retail offer, never provider costs. Expiry
// is evaluated on every read; eventual ordering must also recheck the registrar.
func (s *Store) DomainQuote(ctx context.Context, token, workspace, id string) (DomainQuote, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DomainQuote{}, err
	}
	defer tx.Rollback()
	if _, err = s.authorizeOwner(ctx, tx, token, workspace); err != nil {
		return DomainQuote{}, err
	}
	var raw []byte
	if err = tx.QueryRowContext(ctx, "SELECT offer FROM domain_quotes WHERE id=? AND workspace_id=?", id, workspace).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return DomainQuote{}, ErrDenied
		}
		return DomainQuote{}, err
	}
	q := DomainQuote{ID: id, WorkspaceID: workspace}
	if err = json.Unmarshal(raw, &q.Offer); err != nil {
		return DomainQuote{}, err
	}
	q.Expired = !q.Offer.ExpiresAt.After(s.now())
	return q, tx.Commit()
}
