package portal

import (
	"context"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/buildlog"
	"net/http"
	"time"
)

func (s *Store) authorizeRuntimeLog(ctx context.Context, token, project string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, _, err = s.uploadProject(ctx, tx, token, project, true)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (h *HTTP) runtimeLogsHTTP(w http.ResponseWriter, r *http.Request, token string) {
	if r.Method != "GET" {
		httpError(w, 405, "Use GET")
		return
	}
	project := r.URL.Query().Get("project")
	if err := h.store.authorizeRuntimeLog(r.Context(), token, project); err != nil {
		h.storeError(w, err)
		return
	}
	if h.runtimeLogs == nil {
		httpError(w, 503, "Runtime logs are not configured for this hosting environment")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	output, err := h.runtimeLogs(ctx, project)
	// Membership may be revoked while the operator bridge is reading output.
	if authErr := h.store.authorizeRuntimeLog(r.Context(), token, project); authErr != nil {
		h.storeError(w, authErr)
		return
	}
	if err != nil {
		httpError(w, 503, "Runtime output is unavailable. The website may not be deployed yet. Retry or contact support.")
		return
	}
	httpJSON(w, map[string]any{"log": buildlog.Sanitize(output), "fetched_at": time.Now().Unix()})
}
