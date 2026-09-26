package portal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/domainbilling"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/domains"
)

func (s *Store) migrateDomainPurchases() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`CREATE TABLE IF NOT EXISTS sandbox_domain_purchases(
 order_id TEXT PRIMARY KEY REFERENCES domain_orders(id), workspace_id TEXT NOT NULL REFERENCES workspaces(id),
 project_id TEXT REFERENCES projects(id) ON DELETE SET NULL, domain TEXT NOT NULL UNIQUE,
 currency TEXT NOT NULL, amount_minor INTEGER NOT NULL CHECK(amount_minor>0),
 state TEXT NOT NULL CHECK(state IN ('payment_pending','registering','connecting','connected','needs_review')),
 checkout_id TEXT NOT NULL DEFAULT '', checkout_url TEXT NOT NULL DEFAULT '', message TEXT NOT NULL DEFAULT '',
 created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
 CREATE UNIQUE INDEX IF NOT EXISTS sandbox_domain_checkout ON sandbox_domain_purchases(checkout_id) WHERE checkout_id<>'';
 PRAGMA user_version=57;`)
	if err != nil {
		return err
	}
	return tx.Commit()
}

type DomainCheckoutProvider interface {
	CreateCheckout(context.Context, domainbilling.Order) (domainbilling.Checkout, error)
	ReadCheckout(context.Context, string, domainbilling.Order) (domainbilling.Checkout, error)
}
type SandboxDomainRegistrar interface {
	Register(context.Context, string, string) error // durable order ID, domain
	Connect(context.Context, string, string) error
}
type DomainPurchases struct {
	store     *Store
	quotes    DomainQuoteReader
	payment   DomainCheckoutProvider
	registrar SandboxDomainRegistrar
	mu        sync.Mutex
	wake      chan string
}

func NewDomainPurchases(s *Store, q DomainQuoteReader, p DomainCheckoutProvider, r SandboxDomainRegistrar) *DomainPurchases {
	return &DomainPurchases{store: s, quotes: q, payment: p, registrar: r, wake: make(chan string, 100)}
}

type DomainPurchase struct {
	OrderID                         string `json:"order_id"`
	ProjectID                       string `json:"project_id"`
	Domain                          string `json:"domain"`
	State                           string `json:"state"`
	CheckoutURL                     string `json:"checkout_url,omitempty"`
	Message                         string `json:"message"`
	checkoutID, workspace, currency string
	amount, created                 int64
}

const purchaseColumns = "order_id,COALESCE(project_id,''),domain,state,checkout_url,message,checkout_id,workspace_id,currency,amount_minor,created_at"

func scanPurchase(row interface{ Scan(...any) error }) (DomainPurchase, error) {
	var p DomainPurchase
	err := row.Scan(&p.OrderID, &p.ProjectID, &p.Domain, &p.State, &p.CheckoutURL, &p.Message, &p.checkoutID, &p.workspace, &p.currency, &p.amount, &p.created)
	return p, err
}
func (d *DomainPurchases) authorized(ctx context.Context, token, workspace string) error {
	tx, err := d.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = d.store.authorizeOwner(ctx, tx, token, workspace)
	return err
}
func (d *DomainPurchases) List(ctx context.Context, token, workspace string) ([]DomainPurchase, error) {
	if err := d.authorized(ctx, token, workspace); err != nil {
		return nil, err
	}
	rows, err := d.store.db.QueryContext(ctx, "SELECT "+purchaseColumns+" FROM sandbox_domain_purchases WHERE workspace_id=? ORDER BY created_at DESC", workspace)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []DomainPurchase{}
	for rows.Next() {
		p, e := scanPurchase(rows)
		if e != nil {
			return nil, e
		}
		result = append(result, p)
	}
	return result, rows.Err()
}
func (d *DomainPurchases) Start(ctx context.Context, token, workspace, id, project string) (DomainPurchase, error) {
	if err := d.authorized(ctx, token, workspace); err != nil {
		return DomainPurchase{}, err
	}
	if !validMerchantOrderToken(id) || !validMerchantOrderToken(project) {
		return DomainPurchase{}, ErrInvalid
	}
	old, err := scanPurchase(d.store.db.QueryRowContext(ctx, "SELECT "+purchaseColumns+" FROM sandbox_domain_purchases WHERE order_id=? AND workspace_id=?", id, workspace))
	if err == nil {
		if old.ProjectID != project {
			return DomainPurchase{}, ErrDomainOrderConflict
		}
		return d.Sync(ctx, token, workspace, id)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return DomainPurchase{}, err
	}
	order, err := scanDomainOrder(d.store.db.QueryRowContext(ctx, "SELECT id,quote_id,workspace_id,actor_id,offer,state,created_at FROM domain_orders WHERE id=? AND workspace_id=?", id, workspace))
	if err != nil || order.State != "awaiting_payment" || order.Offer.Environment != "sandbox" {
		return DomainPurchase{}, ErrDomainOrderConflict
	}
	fresh, err := d.quotes.QuoteDomain(ctx, order.Offer.Domain)
	if err != nil {
		return DomainPurchase{}, err
	}
	offer, err := domains.OfferFor(fresh, order.Offer.Domain, 0, d.store.now())
	// Initial sandbox is deliberately sold at registrar cost, with no markup.
	if err != nil || offer.Environment != "sandbox" || offer.Currency != order.Offer.Currency || offer.RegistrationMinor != order.Offer.RegistrationMinor {
		return DomainPurchase{}, ErrDomainOrderConflict
	}
	tx, err := d.store.db.BeginTx(ctx, nil)
	if err != nil {
		return DomainPurchase{}, err
	}
	defer tx.Rollback()
	if _, err = d.store.authorizeOwner(ctx, tx, token, workspace); err != nil {
		return DomainPurchase{}, err
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM projects WHERE id=? AND workspace_id=? AND deletion_requested_at=0", project, workspace).Scan(&count); err != nil || count != 1 {
		return DomainPurchase{}, ErrDenied
	}
	var state string
	var raw []byte
	if err = tx.QueryRowContext(ctx, "SELECT state,offer FROM domain_orders WHERE id=? AND workspace_id=?", id, workspace).Scan(&state, &raw); err != nil || state != "awaiting_payment" {
		return DomainPurchase{}, ErrDomainOrderConflict
	}
	var saved domains.Offer
	if json.Unmarshal(raw, &saved) != nil || saved != order.Offer {
		return DomainPurchase{}, ErrDomainOrderConflict
	}
	now := d.store.now().Unix()
	_, err = tx.ExecContext(ctx, `INSERT INTO sandbox_domain_purchases(order_id,workspace_id,project_id,domain,currency,amount_minor,state,message,created_at,updated_at) VALUES(?,?,?,?,?,?,'payment_pending','Waiting for Stripe test payment',?,?)`, id, workspace, project, offer.Domain, offer.Currency, offer.RegistrationMinor, now, now)
	if err != nil {
		return DomainPurchase{}, ErrDomainOrderConflict
	}
	if err = tx.Commit(); err != nil {
		return DomainPurchase{}, err
	}
	d.process(ctx, id)
	return scanPurchase(d.store.db.QueryRowContext(ctx, "SELECT "+purchaseColumns+" FROM sandbox_domain_purchases WHERE order_id=? AND workspace_id=?", id, workspace))
}
func (d *DomainPurchases) Sync(ctx context.Context, token, workspace, id string) (DomainPurchase, error) {
	if err := d.authorized(ctx, token, workspace); err != nil {
		return DomainPurchase{}, err
	}
	var count int
	if err := d.store.db.QueryRowContext(ctx, "SELECT count(*) FROM sandbox_domain_purchases WHERE order_id=? AND workspace_id=?", id, workspace).Scan(&count); err != nil || count != 1 {
		return DomainPurchase{}, ErrDenied
	}
	select {
	case d.wake <- id:
	default:
	}
	return scanPurchase(d.store.db.QueryRowContext(ctx, "SELECT "+purchaseColumns+" FROM sandbox_domain_purchases WHERE order_id=? AND workspace_id=?", id, workspace))
}
func (d *DomainPurchases) update(ctx context.Context, id, state, message string) error {
	_, err := d.store.db.ExecContext(ctx, "UPDATE sandbox_domain_purchases SET state=?,message=?,updated_at=? WHERE order_id=?", state, message, d.store.now().Unix(), id)
	return err
}
func (d *DomainPurchases) process(ctx context.Context, id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	p, err := scanPurchase(d.store.db.QueryRowContext(ctx, "SELECT "+purchaseColumns+" FROM sandbox_domain_purchases WHERE order_id=?", id))
	if err != nil || p.State == "connected" || p.State == "needs_review" {
		return
	}
	var active int
	if err = d.store.db.QueryRowContext(ctx, "SELECT count(*) FROM projects WHERE id=? AND workspace_id=? AND deletion_requested_at=0", p.ProjectID, p.workspace).Scan(&active); err != nil {
		return
	}
	if active != 1 {
		d.update(ctx, id, "needs_review", "Website was removed. Contact support to resolve the sandbox order.")
		return
	}
	order := domainbilling.Order{ID: p.OrderID, Domain: p.Domain, Currency: p.currency, AmountMinor: p.amount}
	if p.State == "payment_pending" {
		if p.checkoutID == "" {
			if d.store.now().Unix()-p.created > 23*3600 {
				d.update(ctx, id, "needs_review", "Checkout creation needs review before retrying.")
				return
			}
			checkout, e := d.payment.CreateCheckout(ctx, order)
			if e != nil {
				return
			}
			if _, err = d.store.db.ExecContext(ctx, "UPDATE sandbox_domain_purchases SET checkout_id=?,checkout_url=?,updated_at=? WHERE order_id=? AND checkout_id=''", checkout.ID, checkout.URL, d.store.now().Unix(), id); err != nil {
				return
			}
			p.checkoutID = checkout.ID
		}
		checkout, e := d.payment.ReadCheckout(ctx, p.checkoutID, order)
		if e != nil || !checkout.Paid {
			return
		}
		if err = d.update(ctx, id, "registering", "Test payment confirmed. Registering with NameSilo sandbox."); err != nil {
			return
		}
		p.State = "registering"
	}
	if p.State == "registering" {
		if err = d.registrar.Register(ctx, id, p.Domain); err != nil {
			d.update(ctx, id, "needs_review", "Payment confirmed; registrar outcome needs review. Do not pay again.")
			return
		}
		if err = d.update(ctx, id, "connecting", "Registered in sandbox. Configuring domain connection."); err != nil {
			return
		}
		p.State = "connecting"
	}
	if p.State == "connecting" {
		if err = d.registrar.Connect(ctx, id, p.Domain); err != nil {
			d.update(ctx, id, "needs_review", "Sandbox registration completed; domain connection needs review.")
			return
		}
		d.update(ctx, id, "connected", "Sandbox domain linked to this website. OTE domains do not resolve publicly and cannot receive public HTTPS certificates.")
	}
}
func (d *DomainPurchases) Run(ctx context.Context) {
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-d.wake:
			call, cancel := context.WithTimeout(ctx, 60*time.Second)
			d.process(call, id)
			cancel()
		case <-tick.C:
			rows, err := d.store.db.QueryContext(ctx, "SELECT order_id FROM sandbox_domain_purchases WHERE state IN ('payment_pending','registering','connecting') ORDER BY created_at LIMIT 100")
			if err != nil {
				continue
			}
			var ids []string
			for rows.Next() {
				var id string
				if rows.Scan(&id) == nil {
					ids = append(ids, id)
				}
			}
			rows.Close()
			for _, id := range ids {
				if ctx.Err() != nil {
					return
				}
				call, cancel := context.WithTimeout(ctx, 60*time.Second)
				d.process(call, id)
				cancel()
			}
		}
	}
}
