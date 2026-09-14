package portal

import (
	"context"
	"net/http"
	"testing"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/projectarchive"
)

func TestNodeStarterDownloadAcceptedByUploadValidator(t *testing.T) {
	t.Parallel()
	h := &HTTP{host: "portal.example.test", origin: "https://portal.example.test"}
	w := portalRequest(h, http.MethodGet, "/examples/node-website.zip", "", "", "", nil)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/zip" || w.Header().Get("Content-Disposition") != `attachment; filename="node-website.zip"` {
		t.Fatalf("unexpected download response: %d, %v", w.Code, w.Header())
	}
	manifest, err := projectarchive.Validate(context.Background(), w.Body.Bytes(), "node")
	if err != nil || manifest.Files != 5 {
		t.Fatalf("starter cannot be uploaded: %+v, %v", manifest, err)
	}
	w = portalRequest(h, http.MethodGet, "/examples/node-website.zip", "", "https://foreign.example.test", "", nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("foreign origin download: %d", w.Code)
	}
	w = portalRequest(h, http.MethodPost, "/examples/node-website.zip", "", h.origin, "", nil)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST download: %d", w.Code)
	}
}
