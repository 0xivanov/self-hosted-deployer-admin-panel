package namesilo

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func httpResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

const good = `<namesilo><request><operation>checkRegisterAvailability</operation></request><reply><code>300</code><detail>success</detail><available><domain price="6.99" renew="8.99" premium="0" duration="1">example.com</domain></available></reply></namesilo>`

func testClient(t *testing.T, fn transport) *Client {
	t.Helper()
	return newClient(&http.Client{Transport: fn}, "sandbox-key")
}

func TestQuoteDomainExactSandboxOffer(t *testing.T) {
	c := testClient(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Query().Get("domains") != "example.com" || r.URL.Query().Get("type") != "xml" {
			t.Fatal(r.URL)
		}
		return httpResponse(200, good), nil
	})
	q, err := c.QuoteDomain(context.Background(), "Example.com")
	if err != nil || q.Domain != "example.com" || q.RegistrationMinor != 699 || q.RenewalMinor != 899 || q.Currency != "usd" || q.Environment != "sandbox" || !q.PremiumChecked {
		t.Fatalf("quote=%+v err=%v", q, err)
	}
}

func TestQuoteDomainRejectsUnsafeResponses(t *testing.T) {
	for name, body := range map[string]string{
		"invalid category":     strings.Replace(good, "</reply>", "<invalid><domain>example.com</domain></invalid></reply>", 1),
		"unavailable category": strings.Replace(good, "</reply>", "<unavailable><domain>example.com</domain></unavailable></reply>", 1),
		"wrong operation":      strings.Replace(good, "checkRegisterAvailability", "getPrices", 1),
		"premium":              strings.Replace(good, `premium="0"`, `premium="1"`, 1),
		"wrong name":           strings.Replace(good, "example.com", "other.com", 1),
		"missing price":        strings.Replace(good, ` price="6.99"`, "", 1),
		"bad duration":         strings.Replace(good, `duration="1"`, `duration="2"`, 1),
		"duplicate":            strings.Replace(good, "</available>", "<domain price=\"6.99\" renew=\"8.99\" premium=\"0\" duration=\"1\">example.com</domain></available>", 1),
	} {
		t.Run(name, func(t *testing.T) {
			c := testClient(t, func(*http.Request) (*http.Response, error) { return httpResponse(200, body), nil })
			if _, err := c.QuoteDomain(context.Background(), "example.com"); !errors.Is(err, ErrQuote) {
				t.Fatalf("err=%v", err)
			}
		})
	}
	if _, err := testClient(t, func(*http.Request) (*http.Response, error) { return httpResponse(500, "secret"), nil }).QuoteDomain(context.Background(), "example.com"); !errors.Is(err, ErrQuote) {
		t.Fatal(err)
	}
	if _, err := testClient(t, func(*http.Request) (*http.Response, error) { return httpResponse(302, ""), nil }).QuoteDomain(context.Background(), "example.com"); !errors.Is(err, ErrQuote) {
		t.Fatal(err)
	}
}

func TestQuoteDomainValidationAndMoney(t *testing.T) {
	if _, err := NewSandboxClient(""); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	c := testClient(t, func(*http.Request) (*http.Response, error) { return httpResponse(200, good), nil })
	for _, name := range []string{"example.org", "example.com/path", "-bad.com"} {
		if _, err := c.QuoteDomain(context.Background(), name); !errors.Is(err, ErrQuote) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	for _, money := range []string{"6", "6.9", "6.999", "6.9x", "92233720368547758.08"} {
		if _, ok := parseMoney(money); ok {
			t.Fatalf("accepted %q", money)
		}
	}
}

func TestRejectWhitespaceKeys(t *testing.T) {
	for _, key := range []string{"key with space", "key\u00a0value", "key\nvalue"} {
		if _, err := NewSandboxClient(key); !errors.Is(err, ErrInvalid) {
			t.Fatal("accepted whitespace in key")
		}
	}
}
