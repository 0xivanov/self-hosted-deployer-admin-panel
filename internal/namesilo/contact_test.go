package namesilo

import (
	"errors"
	"net/http"
	"testing"
)

func TestSandboxContactIDsReadOnly(t *testing.T) {
	c := testClient(t, func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/contactList" || r.URL.Query().Get("key") != "sandbox-key" {
			t.Fatal(r.URL)
		}
		return httpResponse(200, `<namesilo><request><operation>contactList</operation></request><reply><code>300</code><contact><contact_id>fixture-1</contact_id></contact><contact><contact_id>fixture-2</contact_id></contact></reply></namesilo>`), nil
	})
	got, err := c.SandboxContactIDs(t.Context())
	if err != nil || len(got) != 2 || got[0] != "fixture-1" || got[1] != "fixture-2" {
		t.Fatalf("ids=%v err=%v", got, err)
	}
}

func TestAddContactRequiresCompleteFieldsAndNeverRetries(t *testing.T) {
	calls := 0
	w := &SandboxWriter{client: testClient(t, func(r *http.Request) (*http.Response, error) {
		calls++
		q := r.URL.Query()
		if r.URL.Path != "/api/contactAdd" || q.Get("fn") != "Ada" || q.Get("ln") != "Lovelace" || q.Get("ad") != "1 Main" || q.Get("ct") != "US" || q.Get("em") != "ada@example.test" || q.Get("ph") != "2025550100" {
			t.Fatal(r.URL)
		}
		return httpResponse(200, `<namesilo><request><operation>contactAdd</operation></request><reply><code>300</code><contact_id>new-1</contact_id></reply></namesilo>`), nil
	})}
	got, err := w.AddContact(t.Context(), ContactInput{FirstName: "Ada", LastName: "Lovelace", Address: "1 Main", City: "London", State: "LN", Zip: "N1", Country: "US", Email: "ada@example.test", Phone: "2025550100"})
	if err != nil || got.ContactID != "new-1" || calls != 1 {
		t.Fatalf("result=%+v err=%v calls=%d", got, err, calls)
	}
	if _, err = w.AddContact(t.Context(), ContactInput{FirstName: "Ada"}); !errors.Is(err, ErrInvalid) || calls != 1 {
		t.Fatalf("incomplete contact err=%v calls=%d", err, calls)
	}
}
