package portal

import (
	"context"
	"strings"
	"testing"
)

func TestRuntimeLogsIsolationAndRevocation(t *testing.T) {
	s, _ := newStore(t)
	a, session := verifiedAccount(t, s, "runtime-owner@example.test")
	other, _ := verifiedAccount(t, s, "runtime-other@example.test")
	p, err := s.CreateProject(t.Context(), session.Token, a.WorkspaceID, "Logs", "static")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	h, err := NewHTTP(s, HTTPOptions{Origin: "https://portal.example.test", RuntimeLogs: func(_ context.Context, id string) (string, error) {
		calls++
		if id != p.ID {
			t.Fatal("wrong project")
		}
		return "Listening on port 8080\nSECRET=never-return", nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	owner, _ := httpLogin(t, h, a.Email)
	foreign, _ := httpLogin(t, h, other.Email)
	path := "/api/runtime-logs?project=" + p.ID
	if r := portalRequest(h, "GET", path, "", "", "", foreign); r.Code != 403 || calls != 0 {
		t.Fatal("foreign log access")
	}
	if _, err = s.db.Exec("INSERT INTO memberships VALUES(?,?,'viewer')", other.ID, a.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if r := portalRequest(h, "GET", path, "", "", "", foreign); r.Code != 403 || calls != 0 {
		t.Fatal("viewer log access")
	}
	r := portalRequest(h, "GET", path, "", "", "", owner)
	if r.Code != 200 || !strings.Contains(r.Body.String(), "Listening on port") || strings.Contains(r.Body.String(), "never-return") {
		t.Fatal(r.Code, r.Body.String())
	}
	h.runtimeLogs = func(context.Context, string) (string, error) {
		_, err := s.db.Exec("DELETE FROM memberships WHERE user_id=?", a.ID)
		return "must-not-return", err
	}
	r = portalRequest(h, "GET", path, "", "", "", owner)
	if r.Code != 403 || strings.Contains(r.Body.String(), "must-not-return") {
		t.Fatal("revoked during fetch", r.Code)
	}
}
