package namesilo

import (
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/domains"
)

const (
	requestTimeout   = 20 * time.Second
	maxResponseBytes = 1 << 20
)

type DomainInfo struct {
	Domain              string   `json:"domain"`
	Created             string   `json:"created,omitempty"`
	Expires             string   `json:"expires,omitempty"`
	Status              string   `json:"status,omitempty"`
	Locked              string   `json:"locked,omitempty"`
	Private             string   `json:"private,omitempty"`
	AutoRenew           string   `json:"auto_renew,omitempty"`
	Nameservers         []string `json:"nameservers,omitempty"`
	RegistrantContactID string   `json:"registrant_contact_id,omitempty"`
}

type domainInfoReply struct {
	XMLName xml.Name `xml:"namesilo"`
	Request struct {
		Operation string `xml:"operation"`
	} `xml:"request"`
	Reply struct {
		Code        int    `xml:"code"`
		Created     string `xml:"created"`
		Expires     string `xml:"expires"`
		Status      string `xml:"status"`
		Locked      string `xml:"locked"`
		Private     string `xml:"private"`
		AutoRenew   string `xml:"auto_renew"`
		Nameservers struct {
			Values []string `xml:"nameserver"`
		} `xml:"nameservers"`
		Contacts struct {
			Registrant string `xml:"registrant"`
		} `xml:"contact_ids"`
	} `xml:"reply"`
}

// GetDomainInfo reads registrar state used to reconcile a sandbox registration.
// It never retries and does not claim that a domain is publicly resolvable.
func (c *Client) GetDomainInfo(ctx context.Context, name string) (DomainInfo, error) {
	normalized, err := normalizeDomain(name)
	if err != nil {
		return DomainInfo{}, ErrInvalid
	}
	var parsed domainInfoReply
	q := url.Values{"domain": {normalized}}
	if err := c.readOperation(ctx, "getDomainInfo", q, &parsed); err != nil || parsed.Request.Operation != "getDomainInfo" || parsed.Reply.Code != 300 {
		return DomainInfo{}, ErrInvalid
	}
	if len(parsed.Reply.Nameservers.Values) == 0 {
		return DomainInfo{}, ErrInvalid
	}
	return DomainInfo{Domain: normalized, Created: parsed.Reply.Created, Expires: parsed.Reply.Expires, Status: parsed.Reply.Status, Locked: parsed.Reply.Locked, Private: parsed.Reply.Private, AutoRenew: parsed.Reply.AutoRenew, Nameservers: parsed.Reply.Nameservers.Values, RegistrantContactID: parsed.Reply.Contacts.Registrant}, nil
}

type DNSRecord struct {
	ID       string `json:"id,omitempty"`
	Type     string `json:"type"`
	Host     string `json:"host"`
	Value    string `json:"value"`
	TTL      int    `json:"ttl,omitempty"`
	Distance int    `json:"distance,omitempty"`
}
type DNSRecordResult struct {
	Environment string `json:"environment"`
	RecordID    string `json:"record_id"`
}

func (c *Client) DNSRecords(ctx context.Context, name string) ([]DNSRecord, error) {
	normalized, err := normalizeDomain(name)
	if err != nil {
		return nil, ErrInvalid
	}
	q := url.Values{"domain": {normalized}}
	var parsed struct {
		XMLName xml.Name `xml:"namesilo"`
		Request struct {
			Operation string `xml:"operation"`
		} `xml:"request"`
		Reply struct {
			Code    int `xml:"code"`
			Records []struct {
				ID       string `xml:"record_id"`
				Type     string `xml:"type"`
				Host     string `xml:"host"`
				Value    string `xml:"value"`
				TTL      int    `xml:"ttl"`
				Distance int    `xml:"distance"`
			} `xml:"resource_record"`
		} `xml:"reply"`
	}
	if err := c.readOperation(ctx, "dnsListRecords", q, &parsed); err != nil || parsed.Request.Operation != "dnsListRecords" || parsed.Reply.Code != 300 {
		return nil, ErrInvalid
	}
	result := make([]DNSRecord, 0, len(parsed.Reply.Records))
	for _, r := range parsed.Reply.Records {
		result = append(result, DNSRecord{ID: r.ID, Type: r.Type, Host: r.Host, Value: r.Value, TTL: r.TTL, Distance: r.Distance})
	}
	return result, nil
}

// AddDNSRecord configures one record after a successful sandbox registration.
// Like all writes, callers must persist intent and reconcile an uncertain
// result before attempting it again.
func (w *SandboxWriter) AddDNSRecord(ctx context.Context, name string, record DNSRecord) (DNSRecordResult, error) {
	normalized, err := normalizeDomain(name)
	if err != nil || ctx.Err() != nil || !safeValue(record.Type, 8) || (record.Host != "" && !safeValue(record.Host, 253)) || !safeValue(record.Value, 2048) || record.TTL < 0 || record.Distance < 0 {
		return DNSRecordResult{}, ErrInvalid
	}
	q := url.Values{"domain": {normalized}, "rrtype": {record.Type}, "rrhost": {record.Host}, "rrvalue": {record.Value}, "rrttl": {strconv.Itoa(record.TTL)}}
	if record.Distance > 0 {
		q.Set("rrdistance", strconv.Itoa(record.Distance))
	}
	q.Set("version", "1")
	q.Set("type", "xml")
	q.Set("key", w.client.key)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://ote.namesilo.com/api/dnsAddRecord?"+q.Encode(), nil)
	if err != nil {
		return DNSRecordResult{}, ErrInvalid
	}
	req.Close = true
	call, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	resp, err := w.client.http.Do(req.WithContext(call))
	if err != nil {
		return DNSRecordResult{}, ErrOutcomeUnknown
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil || len(raw) > maxResponseBytes || resp.StatusCode != http.StatusOK {
		return DNSRecordResult{}, ErrOutcomeUnknown
	}
	var parsed struct {
		XMLName xml.Name `xml:"namesilo"`
		Request struct {
			Operation string `xml:"operation"`
		} `xml:"request"`
		Reply struct {
			Code int    `xml:"code"`
			ID   string `xml:"record_id"`
		} `xml:"reply"`
	}
	dec := xml.NewDecoder(strings.NewReader(string(raw)))
	if dec.Decode(&parsed) != nil || dec.Decode(new(any)) != io.EOF || parsed.Request.Operation != "dnsAddRecord" || parsed.Reply.Code != 300 || !safeValue(parsed.Reply.ID, 128) {
		return DNSRecordResult{}, ErrOutcomeUnknown
	}
	return DNSRecordResult{Environment: "sandbox", RecordID: parsed.Reply.ID}, nil
}

func (c *Client) readOperation(ctx context.Context, operation string, q url.Values, out any) error {
	if ctx.Err() != nil {
		return ErrInvalid
	}
	if q == nil {
		q = url.Values{}
	}
	q.Set("version", "1")
	q.Set("type", "xml")
	q.Set("key", c.key)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://ote.namesilo.com/api/"+operation+"?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	call, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	resp, err := c.http.Do(req.WithContext(call))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ErrInvalid
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil || len(raw) > maxResponseBytes {
		return ErrInvalid
	}
	dec := xml.NewDecoder(strings.NewReader(string(raw)))
	if dec.Decode(out) != nil || dec.Decode(new(any)) != io.EOF {
		return ErrInvalid
	}
	return nil
}

func normalizeDomain(name string) (string, error) {
	n, err := domains.PurchaseName(name)
	if err != nil || strings.HasSuffix(n, ".org") {
		return "", ErrInvalid
	}
	return n, nil
}
