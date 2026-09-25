package portal

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/githubdeploy"
)

// GitHubWebhookHandler authenticates raw request bytes before interpreting any
// event. Unsigned delivery/event headers are never used for replay identity.
func GitHubWebhookHandler(store *Store, host, secret string) (http.Handler, error) {
	if store == nil || host == "" || len(secret) < 32 || len(secret) > 4096 || strings.ContainsAny(secret, "\r\n") {
		return nil, errors.New("GitHub webhook requires a store, HTTPS host and a 32 to 4096 byte secret")
	}
	slots := make(chan struct{}, 4)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.URL.EscapedPath() != "/webhooks/github" || r.URL.RawQuery != "" || r.URL.ForceQuery {
			httpError(w, 404, "Not found")
			return
		}
		if r.Host != host || r.TLS == nil || r.Header.Get("Origin") != "" {
			httpError(w, 403, "Invalid webhook origin")
			return
		}
		if r.Method != "POST" {
			httpError(w, 405, "Use POST")
			return
		}
		media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || media != "application/json" {
			httpError(w, 415, "Use application/json")
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			w.Header().Set("Retry-After", "5")
			httpError(w, 503, "Webhook intake is busy")
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, githubdeploy.MaxWebhookPayload))
		if err != nil {
			httpError(w, 413, "Webhook payload exceeds limits")
			return
		}
		delivery, err := githubdeploy.VerifyDelivery([]byte(secret), r.Header.Get("X-Hub-Signature-256"), body)
		if errors.Is(err, githubdeploy.ErrInvalidSignature) {
			httpError(w, 403, "Invalid webhook signature")
			return
		}
		if err != nil {
			httpError(w, 400, "Invalid webhook payload")
			return
		}
		if delivery.Kind == "push" && delivery.Push != nil {
			err = store.AcceptGitHubPush(r.Context(), *delivery.Push, githubdeploy.PayloadSHA256(body))
			if errors.Is(err, ErrGitHubPushCapacity) {
				w.Header().Set("Retry-After", "60")
				httpError(w, 503, "Webhook storage is full; retry later")
				return
			}
			if err != nil {
				httpError(w, 500, "Webhook could not be saved")
				return
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}), nil
}
