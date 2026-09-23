package portal

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestProjectCapacityReservesUntilCleanup(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	a, session := verifiedAccount(t, s, "capacity@example.test")
	if err := s.ConfigureProjectCapacity(2, 1); err != nil {
		t.Fatal(err)
	}
	first, err := s.CreateProject(ctx, session.Token, a.WorkspaceID, "Node", "node")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateProject(ctx, session.Token, a.WorkspaceID, "Second Node", "node"); !errors.Is(err, ErrHostingCapacity) {
		t.Fatalf("node capacity: %v", err)
	}
	if _, err = s.CreateProject(ctx, session.Token, a.WorkspaceID, "Static", "static"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DeleteProject(ctx, session.Token, first.ID, "Node"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateProject(ctx, session.Token, a.WorkspaceID, "Too early", "static"); !errors.Is(err, ErrHostingCapacity) {
		t.Fatalf("deleting slot released early: %v", err)
	}
	availability, err := s.ProjectAvailability(ctx, session.Token, a.WorkspaceID)
	if err != nil || availability.Used != 2 || availability.Static || availability.Node {
		t.Fatalf("availability: %+v %v", availability, err)
	}
	// Model the final database purge after the external cleanup has completed.
	if _, err = s.db.Exec("DELETE FROM projects WHERE id=?", first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateProject(ctx, session.Token, a.WorkspaceID, "Replacement", "node"); err != nil {
		t.Fatal(err)
	}
}

func TestProjectCapacityConcurrentAdmissionAndTenantPrivacy(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	a, session := verifiedAccount(t, s, "capacity-race@example.test")
	_, foreign := verifiedAccount(t, s, "capacity-foreign@example.test")
	if err := s.ConfigureProjectCapacity(1, 1); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	out := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.CreateProject(ctx, session.Token, a.WorkspaceID, fmt.Sprintf("Site %d", i), "static")
			out <- err
		}(i)
	}
	wg.Wait()
	close(out)
	accepted := 0
	for err := range out {
		if err == nil {
			accepted++
		} else if !errors.Is(err, ErrHostingCapacity) {
			t.Fatal(err)
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted %d into one slot", accepted)
	}
	if _, err := s.ProjectAvailability(ctx, foreign.Token, a.WorkspaceID); !errors.Is(err, ErrDenied) {
		t.Fatalf("foreign capacity: %v", err)
	}
	if err := s.ConfigureProjectCapacity(1, 2); !errors.Is(err, ErrInvalid) {
		t.Fatal("accepted invalid limits")
	}
}

func TestProjectCapacityHTTPRejectsWithoutCreating(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	a, session := verifiedAccount(t, s, "capacity-http@example.test")
	if err := s.ConfigureProjectCapacity(1, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateProject(ctx, session.Token, a.WorkspaceID, "Existing", "static"); err != nil {
		t.Fatal(err)
	}
	h, err := NewHTTP(s, HTTPOptions{Origin: "https://portal.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf := httpLogin(t, h, a.Email)
	r := portalRequest(h, "GET", "/api/projects?workspace="+a.WorkspaceID, "", "", "", cookie)
	var body struct {
		Availability ProjectAvailability `json:"availability"`
	}
	if r.Code != 200 {
		t.Fatal(r.Code)
	}
	if err = json.Unmarshal(r.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Availability.Static || body.Availability.Node || body.Availability.Used != 1 {
		t.Fatal(body.Availability)
	}
	r = portalRequest(h, "POST", "/api/projects", `{"workspace":"`+a.WorkspaceID+`","name":"Rejected","kind":"static"}`, h.origin, csrf, cookie)
	if r.Code != 409 || !strings.Contains(r.Body.String(), "No website was created") {
		t.Fatal(r.Code, r.Body.String())
	}
	projects, err := s.Projects(ctx, session.Token, a.WorkspaceID)
	if err != nil || len(projects) != 1 {
		t.Fatal("failed admission created a record", err)
	}
}
