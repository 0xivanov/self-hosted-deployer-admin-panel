//go:build integration

package portal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublicationSitesReloadFailsClosed(t *testing.T) {
	t.Parallel()
	store, _, account, _, project, _ := publicationFixture(t)
	path := filepath.Join(t.TempDir(), "publication-sites.json")
	writeSites := func(sites map[string]string) {
		raw, err := json.Marshal(sites)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeSites(map[string]string{project.ID: "https://site-one.example.net"})
	provider, err := NewPublicationSitesProvider(path, "https://portal.example.test")
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewHTTP(store, HTTPOptions{Origin: "https://portal.example.test", PublicationSitesLookup: provider.Snapshot})
	if err != nil {
		t.Fatal(err)
	}
	cookie, _ := httpLogin(t, h, account.Email)
	request := func() string {
		w := portalRequest(h, "GET", "/api/publications?project="+project.ID, "", "", "", cookie)
		if w.Code != 200 {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	if body := request(); !strings.Contains(body, "site-one.example.net") {
		t.Fatal("initial assignment missing", body)
	}
	writeSites(map[string]string{project.ID: "https://site-two.example.net"})
	if body := request(); !strings.Contains(body, "site-two.example.net") || strings.Contains(body, "site-one.example.net") {
		t.Fatal("valid reload not applied", body)
	}
	if err = os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if body := request(); !strings.Contains(body, `"available":false`) || strings.Contains(body, "site-two.example.net") {
		t.Fatal("invalid reload did not fail closed", body)
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if body := request(); !strings.Contains(body, `"available":false`) {
		t.Fatal("missing reload did not fail closed", body)
	}
}
