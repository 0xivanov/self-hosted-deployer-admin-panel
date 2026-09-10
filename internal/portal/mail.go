package portal

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"time"
)

// Mail contains secrets. Senders must not log bodies or URLs. ID is stable across
// retries for providers supporting idempotency, but SMTP delivery is at-least-once.
type Mail struct {
	ID      string
	To      string
	Subject string
	Text    string
}
type MailSender interface {
	Send(context.Context, Mail) error
}
type AccountMail struct {
	store  *Store
	cipher cipher.AEAD
	origin string
}

// NewAccountMail takes an externally managed 32-byte key. Preserve it separately
// from database backups. Losing the key invalidates queued messages, not accounts.
func NewAccountMail(s *Store, key []byte, origin string, development bool) (*AccountMail, error) {
	if len(key) != 32 {
		return nil, errors.New("account mail encryption requires a 32-byte key")
	}
	// Reuse the portal's strict origin validator; no credentials or paths in links.
	if _, err := NewHTTP(s, HTTPOptions{Origin: origin, Development: development}); err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &AccountMail{store: s, cipher: aead, origin: origin}, nil
}

// Register commits the account and encrypted mail together. No bearer token is
// returned to the public caller. HTTP should mask ErrExists to avoid enumeration.
func (m *AccountMail) Register(ctx context.Context, email, password, workspace string) error {
	_, _, err := m.store.register(ctx, email, password, workspace, m.enqueue)
	return err
}
func (m *AccountMail) RequestReset(ctx context.Context, email string) error {
	_, err := m.store.requestReset(ctx, email, m.enqueue)
	return err
}
func (m *AccountMail) enqueue(ctx context.Context, tx *sql.Tx, user, email, token, purpose string) error {
	id := randomToken()
	subject := "Verify your Deployer account"
	action := "verify"
	if purpose == "reset" {
		subject = "Reset your Deployer password"
		action = "reset"
	}
	// Fragment tokens do not enter HTTP request URLs, proxy logs or referrers.
	link := m.origin + "/#" + action + "=" + url.QueryEscape(token)
	message := Mail{ID: id, To: email, Subject: subject, Text: subject + "\n\nOpen this link to continue:\n" + link + "\n\nIf you did not request this, ignore this email.\n"}
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	nonce := make([]byte, m.cipher.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return err
	}
	encrypted := m.cipher.Seal(nonce, nonce, data, []byte(id))
	_, err = tx.ExecContext(ctx, "INSERT INTO mail_outbox(id,user_id,token_hash,purpose,payload,next_attempt,created_at) VALUES(?,?,?,?,?,?,?)", id, user, digest(token), purpose, encrypted, m.store.now().Unix(), m.store.now().Unix())
	return err
}

// DeliverOne leases a due message, then sends outside the transaction. Sender
// deadlines are shorter than leases. A crash after delivery may cause a duplicate
// email, but verification/reset tokens remain single-use. Errors contain no secrets.
func (m *AccountMail) DeliverOne(ctx context.Context, sender MailSender) (bool, error) {
	if sender == nil {
		return false, errors.New("mail sender required")
	}
	tx, err := m.store.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var id, user, tokenHash, purpose string
	var encrypted []byte
	var attempts int
	now := m.store.now().Unix()
	err = tx.QueryRowContext(ctx, `SELECT id,user_id,token_hash,purpose,payload,attempts FROM mail_outbox WHERE state='pending' AND next_attempt<=? AND lease_until<=? ORDER BY created_at,id LIMIT 1`, now, now).Scan(&id, &user, &tokenHash, &purpose, &encrypted, &attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	lease := randomToken()
	if _, err = tx.ExecContext(ctx, "UPDATE mail_outbox SET lease_id=?,lease_until=?,attempts=attempts+1 WHERE id=?", lease, now+60, id); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	finish := func(state string, next int64) error {
		payload := encrypted
		if state != "pending" {
			payload = []byte{}
		}
		_, err := m.store.db.ExecContext(ctx, "UPDATE mail_outbox SET state=?,payload=?,next_attempt=?,lease_until=0,lease_id='' WHERE id=? AND lease_id=?", state, payload, next, id, lease)
		return err
	}
	var valid int
	err = m.store.db.QueryRowContext(ctx, `SELECT count(*) FROM account_tokens t JOIN users u ON u.id=t.user_id WHERE t.token_hash=? AND t.user_id=? AND t.purpose=? AND t.expires_at>? AND u.disabled=0`, tokenHash, user, purpose, now).Scan(&valid)
	if err != nil {
		return true, err
	}
	if valid == 0 {
		return true, finish("discarded", now)
	}
	if len(encrypted) < m.cipher.NonceSize() {
		finish("failed", now)
		return true, errors.New("invalid encrypted mail payload")
	}
	data, err := m.cipher.Open(nil, encrypted[:m.cipher.NonceSize()], encrypted[m.cipher.NonceSize():], []byte(id))
	if err != nil {
		// Preserve payload to permit recovery with the correct key; bound retries.
		state := "pending"
		if attempts >= 9 {
			state = "failed"
		}
		finish(state, now+300)
		return true, errors.New("cannot decrypt queued mail")
	}
	var message Mail
	if err = json.Unmarshal(data, &message); err != nil || message.ID != id {
		finish("failed", now)
		return true, errors.New("invalid mail envelope")
	}
	sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err = sender.Send(sendCtx, message); err != nil {
		state := "pending"
		if attempts >= 9 {
			state = "failed"
		}
		delay := int64(30) * (1 << min(attempts, 6))
		if e := finish(state, now+delay); e != nil {
			return true, e
		}
		return true, errors.New("account mail delivery failed; retry scheduled or exhausted")
	}
	// A disconnected request must not prevent acknowledgment of successful delivery.
	ackCtx, cancelAck := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancelAck()
	_, err = m.store.db.ExecContext(ackCtx, "UPDATE mail_outbox SET state='sent',payload=X'',lease_id='',lease_until=0 WHERE id=? AND lease_id=?", id, lease)
	return true, err
}
