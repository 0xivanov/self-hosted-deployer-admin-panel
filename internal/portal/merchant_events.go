package portal

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/merchantbilling"
)

// AcceptMerchantEvent retains only verified references, never customer payloads.
func (s *Store) AcceptMerchantEvent(ctx context.Context, event merchantbilling.CheckoutEvent) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var hash string
	err = tx.QueryRowContext(ctx, "SELECT body_hash FROM merchant_events WHERE id=?", event.ID).Scan(&hash)
	if err == nil {
		if hash != event.SHA256 {
			return ErrBillingConflict
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var count int
	err = tx.QueryRowContext(ctx, "SELECT count(*) FROM merchant_orders WHERE id=? AND account_id=? AND state!='requested' AND (session_id IS NULL OR session_id=?)", event.OrderID, event.AccountID, event.SessionID).Scan(&count)
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrDenied
	}
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM merchant_events").Scan(&count); err != nil {
		return err
	}
	if count >= 100000 {
		return ErrBillingConflict
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO merchant_events(id,account_id,order_id,session_id,event_type,body_hash,created_at,received_at) VALUES(?,?,?,?,?,?,?,?)", event.ID, event.AccountID, event.OrderID, event.SessionID, event.Type, event.SHA256, event.Created, s.now().Unix())
	if err != nil {
		return err
	}
	return tx.Commit()
}

// ProcessMerchantEvents retrieves the canonical checkout before updating orders.
// Failed items stay durable and become eligible again after one minute.
func (s *Store) ProcessMerchantEvents(ctx context.Context, p MerchantCheckoutProvider, limit int) (int, int, error) {
	if p == nil || limit < 1 || limit > 100 {
		return 0, 0, ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, "SELECT id,order_id,session_id FROM merchant_events WHERE state='pending' AND next_attempt<=? ORDER BY next_attempt,id LIMIT ?", s.now().Unix(), limit)
	if err != nil {
		return 0, 0, err
	}
	type item struct{ id, order, session string }
	items := []item{}
	for rows.Next() {
		var v item
		if err = rows.Scan(&v.id, &v.order, &v.session); err != nil {
			rows.Close()
			return 0, 0, err
		}
		items = append(items, v)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return 0, 0, err
	}
	if err = rows.Close(); err != nil {
		return 0, 0, err
	}
	done, failed := 0, 0
	for _, v := range items {
		result, err := s.db.ExecContext(ctx, "UPDATE merchant_events SET next_attempt=? WHERE id=? AND state='pending' AND next_attempt<=?", s.now().Unix()+60, v.id, s.now().Unix())
		if err != nil {
			return done, failed, err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return done, failed, err
		}
		if n != 1 {
			continue
		}
		if _, err = s.ReconcileMerchantOrder(ctx, v.order, v.session, p); err != nil {
			if ctx.Err() != nil {
				return done, failed, ctx.Err()
			}
			failed++
			continue
		}
		if _, err = s.db.ExecContext(ctx, "UPDATE merchant_events SET state='done' WHERE id=?", v.id); err != nil {
			return done, failed, err
		}
		done++
	}
	return done, failed, nil
}

func MerchantWebhookHandler(store *Store, host, secret string) (http.Handler, error) {
	if store == nil || host == "" || strings.ContainsAny(host, "/\\@ \r\n\t") || !strings.HasPrefix(secret, "whsec_") || len(secret) < 16 {
		return nil, errors.New("invalid merchant webhook configuration")
	}
	slots := make(chan struct{}, 4)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.TLS == nil || r.Host != host || r.Header.Get("Origin") != "" {
			http.Error(w, "Webhook access denied", 403)
			return
		}
		if r.Method != "POST" {
			w.Header().Set("Allow", "POST")
			http.Error(w, "Method not allowed", 405)
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			http.Error(w, "Webhook busy", 503)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			http.Error(w, "Webhook too large", 413)
			return
		}
		event, err := merchantbilling.VerifyTestCheckoutEvent(body, r.Header.Get("Stripe-Signature"), secret)
		if errors.Is(err, merchantbilling.ErrUnsupportedEvent) {
			w.WriteHeader(204)
			return
		}
		if err != nil {
			http.Error(w, "Invalid webhook", 400)
			return
		}
		err = store.AcceptMerchantEvent(r.Context(), event)
		if errors.Is(err, ErrDenied) {
			w.WriteHeader(204)
			return
		}
		if err != nil {
			http.Error(w, "Webhook could not be retained; retry later", 503)
			return
		}
		w.WriteHeader(204)
	}), nil
}
