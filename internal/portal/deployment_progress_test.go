package portal

import "testing"

func TestDeploymentProgressStates(t *testing.T) {
	for _, tc := range []struct {
		state      string
		dispatched bool
		phase      string
	}{{"queued", false, "queued"}, {"running", false, "preparing"}, {"running", true, "verifying"}, {"failed", true, "failed"}, {"succeeded", true, "succeeded"}, {"cancelled", false, "cancelled"}} {
		phase, message := deploymentProgress(tc.state, tc.dispatched)
		if phase != tc.phase || message == "" {
			t.Fatalf("%+v: %s %s", tc, phase, message)
		}
	}
	if phase, message := deploymentProgress("unknown", false); phase != "" || message != "" {
		t.Fatal("invented state")
	}
}
