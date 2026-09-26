package namesilo

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/domains"
)

// ErrOutcomeUnknown means the caller must reconcile with the registrar, never
// automatically retry. Even malformed or rejected HTTP replies may follow a write.
var ErrOutcomeUnknown = errors.New("registrar outcome unknown; reconcile before retrying")

// SandboxWriter is deliberately separate from the portal's quote reader. There
// is no production endpoint switch. Callers must persist intent before dispatch
// and must not retry a mutation without reconciling the previous attempt.
type SandboxWriter struct{ client *Client }

func NewSandboxWriter(key string) (*SandboxWriter, error) {
	c, err := NewSandboxClient(key)
	if err != nil {
		return nil, err
	}
	// Go may replay GET on a stale reused connection. NameSilo uses GET for writes,
	// so every mutation must use a fresh connection, with no transport retry.
	c.http.Transport.(*http.Transport).DisableKeepAlives = true
	return &SandboxWriter{client: c}, nil
}
func (w *SandboxWriter) Close() error { return w.client.Close() }

type MutationResult struct {
	Environment string `json:"environment"`
	Domain      string `json:"domain"`
	Operation   string `json:"operation"`
	AmountMinor int64  `json:"amount_minor"`
	// ReviewRequired is a successful mutation with fallback settings. It must not
	// be interpreted as a failed purchase and resubmitted.
	ReviewRequired bool   `json:"review_required"`
	Warning        string `json:"warning,omitempty"`
}

// Register registers one year, enables privacy and disables registrar auto-renew.
// A prevalidated account contact profile is mandatory to avoid implicit defaults.
// This low-level transport does not authorize payments or establish ownership.
func (w *SandboxWriter) Register(ctx context.Context, name, contactID string) (MutationResult, error) {
	if contactID == "" || len(contactID) > 128 || strings.IndexFunc(contactID, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return MutationResult{}, ErrInvalid
	}
	return w.mutate(ctx, "registerDomain", name, url.Values{"contact_id": {contactID}, "private": {"1"}, "auto_renew": {"0"}, "ns1": {"ns1.namesilo.com"}, "ns2": {"ns2.namesilo.com"}})
}

// Renew performs exactly one year. Before using this in live fulfillment, the
// caller must check expiration/status and disable restoration in NameSilo's API
// manager: renewDomain can otherwise restore an expired domain at a higher cost.
func (w *SandboxWriter) Renew(ctx context.Context, name string) (MutationResult, error) {
	return w.mutate(ctx, "renewDomain", name, url.Values{})
}

type mutationReply struct {
	XMLName  xml.Name `xml:"namesilo"`
	Requests []struct {
		Operations []string `xml:"operation"`
	} `xml:"request"`
	Replies []struct {
		Codes   []int    `xml:"code"`
		Domains []string `xml:"domain"`
		Amounts []string `xml:"order_amount"`
	} `xml:"reply"`
}

func (w *SandboxWriter) mutate(ctx context.Context, operation, name string, q url.Values) (MutationResult, error) {
	normalized, err := domains.PurchaseName(name)
	if err != nil || strings.HasSuffix(normalized, ".org") || (operation != "registerDomain" && operation != "renewDomain") {
		return MutationResult{}, ErrInvalid
	}
	if ctx.Err() != nil {
		return MutationResult{}, ErrInvalid
	}
	q.Set("domain", normalized)
	q.Set("years", "1")
	q.Set("version", "1")
	q.Set("type", "xml")
	q.Set("key", w.client.key)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://ote.namesilo.com/api/"+operation+"?"+q.Encode(), nil)
	if err != nil {
		return MutationResult{}, ErrInvalid
	}
	req.Close = true
	// No loop, redirects, idempotency headers, or HTTP error bodies in diagnostics.
	call, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	resp, err := w.client.http.Do(req.WithContext(call))
	if err != nil {
		return MutationResult{}, ErrOutcomeUnknown
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 || resp.StatusCode != http.StatusOK {
		return MutationResult{}, ErrOutcomeUnknown
	}
	var parsed mutationReply
	dec := xml.NewDecoder(strings.NewReader(string(raw)))
	if dec.Decode(&parsed) != nil || dec.Decode(new(any)) != io.EOF || len(parsed.Requests) != 1 || len(parsed.Requests[0].Operations) != 1 || parsed.Requests[0].Operations[0] != operation || len(parsed.Replies) != 1 {
		return MutationResult{}, ErrOutcomeUnknown
	}
	reply := parsed.Replies[0]
	if len(reply.Codes) != 1 || len(reply.Domains) != 1 || reply.Domains[0] != normalized || len(reply.Amounts) != 1 {
		return MutationResult{}, ErrOutcomeUnknown
	}
	code := reply.Codes[0]
	if code != 300 && !(operation == "registerDomain" && (code == 301 || code == 302)) {
		return MutationResult{}, ErrOutcomeUnknown
	}
	amount, ok := parseMoney(reply.Amounts[0])
	if !ok {
		return MutationResult{}, ErrOutcomeUnknown
	}
	result := MutationResult{Environment: "sandbox", Domain: normalized, Operation: operation, AmountMinor: amount}
	if code == 301 {
		result.ReviewRequired = true
		result.Warning = "nameserver_fallback"
	}
	if code == 302 {
		result.ReviewRequired = true
		result.Warning = "contact_fallback"
	}
	return result, nil
}
