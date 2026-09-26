package namesilo

import (
	"errors"
	"net/http"
	"testing"
)

func TestGetDomainInfoAndDNSRecords(t *testing.T) {
	c := testClient(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Query().Get("key") != "sandbox-key" {
			t.Fatal("key missing")
		}
		switch r.URL.Path {
		case "/api/getDomainInfo":
			return httpResponse(200, `<namesilo><request><operation>getDomainInfo</operation></request><reply><code>300</code><created>2026-09-26</created><expires>2027-09-26</expires><status>Active</status><locked>Yes</locked><private>Yes</private><auto_renew>No</auto_renew><nameservers><nameserver>ns1.namesilo.com</nameserver><nameserver>ns2.namesilo.com</nameserver></nameservers><contact_ids><registrant>fixture-1</registrant></contact_ids></reply></namesilo>`), nil
		case "/api/dnsListRecords":
			return httpResponse(200, `<namesilo><request><operation>dnsListRecords</operation></request><reply><code>300</code><resource_record><record_id>r1</record_id><type>A</type><host>@</host><value>192.0.2.10</value><ttl>300</ttl><distance>0</distance></resource_record></reply></namesilo>`), nil
		default:
			t.Fatal(r.URL)
			return nil, nil
		}
	})
	info, err := c.GetDomainInfo(t.Context(), "Example.com")
	if err != nil || info.Domain != "example.com" || info.Status != "Active" || len(info.Nameservers) != 2 || info.RegistrantContactID != "fixture-1" {
		t.Fatalf("info=%+v err=%v", info, err)
	}
	records, err := c.DNSRecords(t.Context(), "example.com")
	if err != nil || len(records) != 1 || records[0].Value != "192.0.2.10" {
		t.Fatalf("records=%+v err=%v", records, err)
	}
}

func TestAddDNSRecordUncertainDoesNotRetry(t *testing.T) {
	calls := 0
	w := &SandboxWriter{client: testClient(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Path != "/api/dnsAddRecord" || r.URL.Query().Get("rrtype") != "A" {
			t.Fatal(r.URL)
		}
		return httpResponse(500, "sandbox-key"), nil
	})}
	_, err := w.AddDNSRecord(t.Context(), "example.com", DNSRecord{Type: "A", Host: "@", Value: "192.0.2.10", TTL: 300})
	if !errors.Is(err, ErrOutcomeUnknown) || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}
