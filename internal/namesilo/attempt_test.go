package namesilo

import (
	"errors"
	"net/http"
	"path/filepath"
	"testing"
)

func TestAttemptSurvivesRestartWithoutRepeatingMutation(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(map[bool]string{false: "receipt", true: "unknown"}[uncertain], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "attempt.jsonl")
			calls := 0
			client := testClient(t, func(*http.Request) (*http.Response, error) {
				calls++
				if uncertain {
					return nil, errors.New("connection lost")
				}
				return httpResponse(200, mutationXML("renewDomain", 300)), nil
			})
			attempt := SandboxAttempt{Operation: "renewDomain", Domain: "example.com"}
			first, err := (&SandboxWriter{client: client}).ExecuteOnce(t.Context(), path, attempt)
			if uncertain && !errors.Is(err, ErrOutcomeUnknown) || !uncertain && err != nil {
				t.Fatal(err)
			}
			// A new writer uses the persisted evidence, not in-memory state.
			second, err := (&SandboxWriter{client: client}).ExecuteOnce(t.Context(), path, attempt)
			if uncertain && !errors.Is(err, ErrOutcomeUnknown) || !uncertain && (err != nil || second != first) {
				t.Fatal("incorrect recovery result", err)
			}
			if calls != 1 {
				t.Fatalf("mutation repeated: %d", calls)
			}
			attempt.Domain = "other.com"
			if _, err = (&SandboxWriter{client: client}).ExecuteOnce(t.Context(), path, attempt); !errors.Is(err, ErrOutcomeUnknown) {
				t.Fatal("attempt identity was reusable")
			}
			if calls != 1 {
				t.Fatal("conflicting request dispatched")
			}
		})
	}
}
