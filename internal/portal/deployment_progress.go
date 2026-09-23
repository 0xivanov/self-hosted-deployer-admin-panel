package portal

// Only describe persisted control-plane facts. Running does not prove the app
// is healthy, and a failed attempt does not prove an older site is reachable.
func deploymentProgress(state string, dispatched bool) (string, string) {
	switch state {
	case "queued":
		return "queued", "Waiting for a deployment worker to pick up this release."
	case "running":
		if dispatched {
			return "verifying", "Deployment submitted. Waiting for the runtime to confirm the result and website health."
		}
		return "preparing", "A worker is preparing this deployment. Runtime submission is not recorded yet."
	case "succeeded":
		return "succeeded", "This release was published successfully."
	case "failed":
		return "failed", "This deployment did not complete successfully. Retry the saved release, or restore a previously published release from history. Contact support if it fails again."
	case "cancelled":
		return "cancelled", "This deployment was cancelled before publication."
	default:
		return "", ""
	}
}
