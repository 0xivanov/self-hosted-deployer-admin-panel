//go:build integration

package portal

import (
	"archive/zip"
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodebuild"
)

func nodeUploadFixture(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for _, f := range []struct{ name, body string }{{"package.json", `{"scripts":{"start":"node server.js","build":"exit 99"}}`}, {"package-lock.json", `{"lockfileVersion":3,"packages":{"":{}}}`}, {"server.js", "throw new Error('not executed')"}} {
		w, err := z.Create(f.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write([]byte(f.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func TestNodeBuildPersistenceAuthorizationAndCancellation(t *testing.T) {
	t.Parallel()
	s, path := newStore(t)
	ctx := t.Context()
	a, session := verifiedAccount(t, s, "node-build@example.test")
	b, other := verifiedAccount(t, s, "node-other@example.test")
	project, err := s.CreateProject(ctx, session.Token, a.WorkspaceID, "app", "node")
	if err != nil {
		t.Fatal(err)
	}
	upload, err := s.SaveUpload(ctx, session.Token, project.ID, nodeUploadFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	assignment := NodeBuildAssignment{Settings: nodebuild.Settings{Architecture: "arm64"}, ToolchainSHA256: strings.Repeat("a", 64)}
	key := strings.Repeat("k", 16)
	j, err := s.RequestNodeBuild(ctx, session.Token, project.ID, upload.ID, key, assignment)
	if err != nil || j.State != "queued" || j.Plan.SourceSHA256 != upload.SHA256 {
		t.Fatal(j, err)
	}
	again, err := s.RequestNodeBuild(ctx, session.Token, project.ID, upload.ID, key, assignment)
	if err != nil || again.ID != j.ID {
		t.Fatal(again, err)
	}
	altered := assignment
	altered.Settings.SkipBuild = true
	if _, err = s.RequestNodeBuild(ctx, session.Token, project.ID, upload.ID, key, altered); !errors.Is(err, ErrBuildConflict) {
		t.Fatal(err)
	}
	if _, err = s.RequestNodeBuild(ctx, session.Token, project.ID, upload.ID, strings.Repeat("n", 16), assignment); !errors.Is(err, ErrBuildConflict) {
		t.Fatal(err)
	}
	if err = s.DeleteUpload(ctx, session.Token, project.ID, upload.ID); !errors.Is(err, ErrRetained) {
		t.Fatal(err)
	}
	for _, role := range []string{"outsider", "viewer"} {
		if role == "viewer" {
			if _, err = s.db.Exec("INSERT INTO memberships VALUES(?,?,'viewer')", b.ID, a.WorkspaceID); err != nil {
				t.Fatal(err)
			}
		}
		if _, err = s.NodeBuilds(ctx, other.Token, project.ID); !errors.Is(err, ErrDenied) {
			t.Fatal(role, err)
		}
		if _, err = s.RequestNodeBuild(ctx, other.Token, project.ID, upload.ID, key, assignment); !errors.Is(err, ErrDenied) {
			t.Fatal(role, err)
		}
		if err = s.CancelNodeBuild(ctx, other.Token, project.ID, j.ID); !errors.Is(err, ErrDenied) {
			t.Fatal(role, err)
		}
	}
	if _, err = s.db.Exec("UPDATE memberships SET role='developer' WHERE user_id=? AND workspace_id=?", b.ID, a.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if err = s.CancelNodeBuild(ctx, other.Token, project.ID, j.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.CancelNodeBuild(ctx, other.Token, project.ID, j.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteUpload(ctx, session.Token, project.ID, upload.ID); !errors.Is(err, ErrRetained) {
		t.Fatal(err)
	}
	next, err := s.RequestNodeBuild(ctx, other.Token, project.ID, upload.ID, strings.Repeat("n", 16), assignment)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	jobs, err := reopened.NodeBuilds(ctx, session.Token, project.ID)
	if err != nil || len(jobs) != 2 {
		t.Fatal(jobs, err)
	}
	var found bool
	for _, job := range jobs {
		if job.ID == next.ID {
			found = job.State == "queued" && job.Plan.SourceSHA256 == upload.SHA256 && job.ToolchainSHA256 == assignment.ToolchainSHA256
		}
	}
	if !found {
		t.Fatal("pending specification was not preserved")
	}
}
func TestNodeBuildConcurrentRequestAndForeignUpload(t *testing.T) {
	t.Parallel()
	s, _ := newStore(t)
	ctx := t.Context()
	a, session := verifiedAccount(t, s, "node-race@example.test")
	p, err := s.CreateProject(ctx, session.Token, a.WorkspaceID, "one", "node")
	if err != nil {
		t.Fatal(err)
	}
	q, err := s.CreateProject(ctx, session.Token, a.WorkspaceID, "two", "node")
	if err != nil {
		t.Fatal(err)
	}
	upload, err := s.SaveUpload(ctx, session.Token, p.ID, nodeUploadFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	assignment := NodeBuildAssignment{Settings: nodebuild.Settings{Architecture: "amd64"}, ToolchainSHA256: strings.Repeat("b", 64)}
	if _, err = s.RequestNodeBuild(ctx, session.Token, q.ID, upload.ID, strings.Repeat("k", 16), assignment); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	type result struct {
		job NodeBuild
		err error
	}
	results := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() {
			j, e := s.RequestNodeBuild(ctx, session.Token, p.ID, upload.ID, strings.Repeat("k", 16), assignment)
			results <- result{j, e}
		}()
	}
	first, second := <-results, <-results
	if first.err != nil || second.err != nil || first.job.ID != second.job.ID {
		t.Fatal(first, second)
	}
	if _, err = s.db.Exec("UPDATE node_builds SET state='running' WHERE id=?", first.job.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.CancelNodeBuild(ctx, session.Token, p.ID, first.job.ID); !errors.Is(err, ErrBuildConflict) {
		t.Fatal("active executor relabelled", err)
	}
}
