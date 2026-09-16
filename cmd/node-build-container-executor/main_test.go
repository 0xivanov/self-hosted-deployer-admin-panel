//go:build linux

package main

import (
	"crypto/tls"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/containerbuild"
)

func TestProjectRouterDeleteIsAuthenticatedIdempotentAndTombstoned(t *testing.T) {
	router, stateRoot, depsRoot := testProjectRouter(t)
	project := projectID(1)
	for _, root := range []string{stateRoot, depsRoot} {
		if err := os.MkdirAll(filepath.Join(root, project), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, project, "artifact"), []byte("test"), 0600); err != nil {
			t.Fatal(err)
		}
	}

	unauthorized := projectRequest(http.MethodDelete, project, "wrong")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, unauthorized)
	if response.Code != http.StatusForbidden {
		t.Fatalf("unauthorized delete status = %d, want 403", response.Code)
	}

	response = httptest.NewRecorder()
	router.ServeHTTP(response, projectRequest(http.MethodDelete, project, "secret"))
	if response.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", response.Code)
	}
	if _, err := os.Stat(filepath.Join(stateRoot, project+".deleted")); err != nil {
		t.Fatalf("tombstone missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stateRoot, project)); !os.IsNotExist(err) {
		t.Fatalf("state directory still exists, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(depsRoot, project)); !os.IsNotExist(err) {
		t.Fatalf("dependencies directory still exists, err=%v", err)
	}

	response = httptest.NewRecorder()
	router.ServeHTTP(response, projectRequest(http.MethodDelete, project, "secret"))
	if response.Code != http.StatusNoContent {
		t.Fatalf("repeated delete status = %d, want 204", response.Code)
	}

	response = httptest.NewRecorder()
	router.ServeHTTP(response, projectRequest(http.MethodGet, project, "secret"))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("tombstoned reopen status = %d, want 503", response.Code)
	}
}

func TestProjectRouterDeletePreservesUnrelatedProject(t *testing.T) {
	router, stateRoot, depsRoot := testProjectRouter(t)
	deletedProject := projectID(2)
	otherProject := projectID(3)
	if err := os.MkdirAll(filepath.Join(stateRoot, otherProject), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(depsRoot, otherProject), 0700); err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	router.ServeHTTP(response, projectRequest(http.MethodDelete, deletedProject, "secret"))
	if response.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", response.Code)
	}
	for _, path := range []string{filepath.Join(stateRoot, otherProject), filepath.Join(depsRoot, otherProject)} {
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			t.Fatalf("unrelated project storage %q missing or invalid: %v", path, err)
		}
	}
}

func testProjectRouter(t *testing.T) (*projectRouter, string, string) {
	t.Helper()
	stateRoot := t.TempDir()
	depsRoot := t.TempDir()
	router, err := newProjectRouter(containerbuild.Config{
		Project:               "*",
		ToolchainSHA256:       strings.Repeat("a", 64),
		Architecture:          "amd64",
		Host:                  "executor.example",
		StateDirectory:        stateRoot,
		DependenciesDirectory: depsRoot,
		Image:                 "builder-image",
	}, "secret")
	if err != nil {
		t.Fatalf("new project router: %v", err)
	}
	t.Cleanup(func() { _ = router.shutdown(t.Context()) })
	return router, stateRoot, depsRoot
}

func projectRequest(method, project, token string) *http.Request {
	req := httptest.NewRequest(method, "https://executor.example/v1/project", nil)
	req.Host = "executor.example"
	req.TLS = &tls.ConnectionState{}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Project-ID", project)
	return req
}

func projectID(seed byte) string {
	b := make([]byte, 32)
	b[0] = seed
	return hex.EncodeToString(b)
}
