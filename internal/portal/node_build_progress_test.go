package portal

import (
	"strings"
	"testing"
)

func TestNodeBuildProgressUsesSafePersistedEvidence(t *testing.T) {
	for _, tc := range []struct {
		state               string
		dispatched          bool
		result, phase, part string
	}{
		{"queued", false, "", "queued", "Waiting for"},
		{"running", false, "", "preparing", "dependencies"},
		{"running", true, "", "submitted", "Waiting for builder"},
		{"succeeded", true, "", "succeeded", "Deploy a saved release"},
		{"cancelled", false, "", "cancelled", "unchanged"},
		{"failed", false, `{"reason":"preparation_expired_before_dispatch"}`, "failed", "not submitted"},
		{"failed", true, `{"reason":"SECRET-provider-credentials"}`, "failed", "did not produce"},
		{"failed", true, `invalid SECRET`, "failed", "did not produce"},
	} {
		phase, message := nodeBuildProgress(tc.state, tc.dispatched, []byte(tc.result))
		if phase != tc.phase || !strings.Contains(message, tc.part) || strings.Contains(message, "SECRET") {
			t.Fatalf("%s: %s %s", tc.state, phase, message)
		}
	}
}
