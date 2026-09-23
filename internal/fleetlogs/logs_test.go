package fleetlogs

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrivateBridgeRoundTripAndValidation(t *testing.T) {
	dir, err := os.MkdirTemp("", "logs-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	// macOS has a short Unix socket path limit.
	if len(dir) > 60 {
		os.RemoveAll(dir)
		dir, err = os.MkdirTemp("/tmp", "logs-")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(dir)
	}
	socket := filepath.Join(dir, "bridge.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("a", 64)
	calls := 0
	handler := Handler(func(_ context.Context, project string) (string, error) {
		calls++
		if project != id {
			t.Fatal("wrong project")
		}
		return "app started\nAPI_KEY=secret-value", nil
	})
	server := &http.Server{Handler: handler}
	go server.Serve(listener)
	defer server.Close()
	output, err := Reader(socket)(t.Context(), id)
	if err != nil || !strings.Contains(output, "app started") || strings.Contains(output, "secret-value") {
		t.Fatal(output, err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/logs?project=../../other", nil))
	if response.Code != 400 || calls != 1 {
		t.Fatal("unsafe project accepted")
	}
}
