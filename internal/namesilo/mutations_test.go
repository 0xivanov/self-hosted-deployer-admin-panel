package namesilo

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func mutationXML(op string, code int) string {
	return fmt.Sprintf(`<namesilo><request><operation>%s</operation></request><reply><code>%d</code><domain>example.com</domain><order_amount>8.99</order_amount></reply></namesilo>`, op, code)
}
func TestSandboxRegistrationAndRenewal(t *testing.T) {
	for _, op := range []string{"registerDomain", "renewDomain"} {
		t.Run(op, func(t *testing.T) {
			calls := 0
			w := &SandboxWriter{client: testClient(t, func(r *http.Request) (*http.Response, error) {
				calls++
				q := r.URL.Query()
				if r.Method != "GET" || r.URL.Scheme != "https" || r.URL.Host != "ote.namesilo.com" || r.URL.Path != "/api/"+op || !r.Close {
					t.Fatal("unsafe request target or connection policy")
				}
				if q.Get("years") != "1" || q.Get("domain") != "example.com" || q.Get("key") != "sandbox-key" || q.Has("payment_id") {
					t.Fatal("unexpected mutation parameters")
				}
				if op == "registerDomain" && (q.Get("private") != "1" || q.Get("auto_renew") != "0" || q.Get("contact_id") != "profile-1" || q.Get("ns1") != "ns1.namesilo.com" || q.Get("ns2") != "ns2.namesilo.com") {
					t.Fatal("registration defaults missing")
				}
				return httpResponse(200, mutationXML(op, 300)), nil
			})}
			var got MutationResult
			var err error
			if op == "registerDomain" {
				got, err = w.Register(t.Context(), "example.com", "profile-1")
			} else {
				got, err = w.Renew(t.Context(), "example.com")
			}
			if err != nil || got.Environment != "sandbox" || got.AmountMinor != 899 || got.ReviewRequired || calls != 1 {
				t.Fatalf("result=%+v err=%v calls=%d", got, err, calls)
			}
		})
	}
}
func TestRegistrationFallbackIsSuccessNeedingReview(t *testing.T) {
	for code, warning := range map[int]string{301: "nameserver_fallback", 302: "contact_fallback"} {
		t.Run(warning, func(t *testing.T) {
			w := &SandboxWriter{client: testClient(t, func(*http.Request) (*http.Response, error) {
				return httpResponse(200, mutationXML("registerDomain", code)), nil
			})}
			got, err := w.Register(t.Context(), "example.com", "profile-1")
			if err != nil || !got.ReviewRequired || got.Warning != warning {
				t.Fatalf("result=%+v err=%v", got, err)
			}
		})
	}
}
func TestMutationUncertainResponsesNeverRetry(t *testing.T) {
	base := mutationXML("renewDomain", 300)
	cases := map[string]string{
		"wrong operation": strings.Replace(base, "renewDomain", "registerDomain", 1),
		"wrong domain":    strings.Replace(base, "example.com", "other.com", 1),
		"duplicate code":  strings.Replace(base, "</code>", "</code><code>300</code>", 1),
		"duplicate reply": strings.Replace(base, "</namesilo>", "<reply><code>300</code></reply></namesilo>", 1),
		"truncated":       base[:len(base)-10],
		"missing amount":  strings.Replace(base, "<order_amount>8.99</order_amount>", "", 1),
		"renew fallback":  mutationXML("renewDomain", 302),
		"provider error":  mutationXML("renewDomain", 400),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			calls := 0
			w := &SandboxWriter{client: testClient(t, func(*http.Request) (*http.Response, error) { calls++; return httpResponse(200, body), nil })}
			_, err := w.Renew(t.Context(), "example.com")
			if !errors.Is(err, ErrOutcomeUnknown) || calls != 1 {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
		})
	}
	for _, status := range []int{302, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			w := &SandboxWriter{client: testClient(t, func(*http.Request) (*http.Response, error) { calls++; return httpResponse(status, "sandbox-key"), nil })}
			_, err := w.Renew(t.Context(), "example.com")
			if !errors.Is(err, ErrOutcomeUnknown) || calls != 1 || strings.Contains(err.Error(), "sandbox-key") {
				t.Fatal("unsafe error handling")
			}
		})
	}
	t.Run("network error", func(t *testing.T) {
		calls := 0
		w := &SandboxWriter{client: testClient(t, func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("sandbox-key") })}
		_, err := w.Renew(t.Context(), "example.com")
		if !errors.Is(err, ErrOutcomeUnknown) || calls != 1 || strings.Contains(err.Error(), "sandbox-key") {
			t.Fatal("unsafe transport error handling")
		}
	})
}
func TestWriterRejectsInvalidBeforeDispatch(t *testing.T) {
	w := &SandboxWriter{client: testClient(t, func(*http.Request) (*http.Response, error) { t.Fatal("invalid request dispatched"); return nil, nil })}
	for _, name := range []string{"example.org", "example.com/path", "foo.example.com"} {
		if _, err := w.Renew(t.Context(), name); !errors.Is(err, ErrInvalid) {
			t.Fatal(err)
		}
	}
	if _, err := w.Register(t.Context(), "example.com", ""); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := w.Renew(ctx, "example.com"); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	real, err := NewSandboxWriter("fixture-key")
	if err != nil {
		t.Fatal(err)
	}
	defer real.Close()
	transport := real.client.http.Transport.(*http.Transport)
	if !transport.DisableKeepAlives || transport.Proxy != nil {
		t.Fatal("mutation transport may reuse connections or proxy credentials")
	}
}
