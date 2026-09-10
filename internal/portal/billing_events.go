package portal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/hostingbilling"
)

const BillingInboxBytes = 100 << 20
const BillingInboxEvents = 10000

var ErrBillingConflict = errors.New("billing event identity conflicts with retained data")
var ErrBillingCapacity = errors.New("billing inbox capacity reached")

type BillingReceipt struct {
	ID        string `json:"id"`
	Duplicate bool   `json:"duplicate"`
}

// AcceptBillingWebhook verifies the raw signature before any durable mutation.
// Success means the event is durably queued, not that hosting was paid for.
func (s *Store) AcceptBillingWebhook(ctx context.Context, body []byte, signature, secret string) (BillingReceipt, error) {
	event, err := hostingbilling.VerifyTestEvent(body, signature, secret)
	if err != nil {
		return BillingReceipt{}, err
	}
	if len(event.ID) > 255 || len(event.Type) > 255 {
		return BillingReceipt{}, hostingbilling.ErrWebhook
	}
	// Canonicalize object data without converting integer amounts to floating
	// point. Delivery-envelope counters are not part of an event's identity.
	decoder := json.NewDecoder(bytes.NewReader(event.Object))
	decoder.UseNumber()
	var object any
	if err = decoder.Decode(&object); err != nil {
		return BillingReceipt{}, hostingbilling.ErrWebhook
	}
	payload, err := json.Marshal(object)
	if err != nil {
		return BillingReceipt{}, err
	}
	identity, err := json.Marshal(struct {
		Type    string
		Created int64
		Object  json.RawMessage
	}{event.Type, event.Created, payload})
	if err != nil {
		return BillingReceipt{}, err
	}
	sum := sha256.Sum256(identity)
	fingerprint := hex.EncodeToString(sum[:])
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BillingReceipt{}, err
	}
	defer tx.Rollback()
	var existing string
	err = tx.QueryRowContext(ctx, "SELECT fingerprint FROM billing_events WHERE id=?", event.ID).Scan(&existing)
	if err == nil {
		if existing != fingerprint {
			return BillingReceipt{}, ErrBillingConflict
		}
		return BillingReceipt{ID: event.ID, Duplicate: true}, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return BillingReceipt{}, err
	}
	var count, size int64
	if err = tx.QueryRowContext(ctx, "SELECT count(*),COALESCE(sum(length(payload)),0) FROM billing_events").Scan(&count, &size); err != nil {
		return BillingReceipt{}, err
	}
	if count >= BillingInboxEvents || size+int64(len(payload)) > BillingInboxBytes {
		return BillingReceipt{}, ErrBillingCapacity
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO billing_events(id,event_type,provider_created,fingerprint,payload,received_at) VALUES(?,?,?,?,?,?)", event.ID, event.Type, event.Created, fingerprint, payload, s.now().Unix()); err != nil {
		return BillingReceipt{}, err
	}
	return BillingReceipt{ID: event.ID}, tx.Commit()
}

// BillingWebhookHandler is a dedicated endpoint, not a browser-session route.
// Caller-owned routing must mount it separately from the portal's CSRF routes.
// Neither billing secrets nor receipt payloads are exposed to customers.
func BillingWebhookHandler(store *Store, host, secret string) (http.Handler, error) {
	if store == nil || host == "" || strings.ContainsAny(host, "/\\@ \r\n\t") || !strings.HasPrefix(secret, "whsec_") || len(secret) < 16 {
		return nil, errors.New("invalid billing webhook configuration")
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
			w.Header().Set("Retry-After", "5")
			http.Error(w, "Webhook busy", 503)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, hostingbilling.MaxWebhookBytes))
		if err != nil {
			http.Error(w, "Webhook body unavailable or too large", 413)
			return
		}
		_, err = store.AcceptBillingWebhook(r.Context(), body, r.Header.Get("Stripe-Signature"), secret)
		if err != nil {
			if errors.Is(err, hostingbilling.ErrWebhook) {
				http.Error(w, "Invalid webhook", 400)
			} else {
				http.Error(w, "Webhook could not be retained; retry later", 503)
			}
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}), nil
}
