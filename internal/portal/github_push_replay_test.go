//go:build integration

package portal

import (
	"strings"
	"testing"
)

func TestGitHubPushReplayAllowsBranchReturnAndRejectsExactRedelivery(t *testing.T) {
	s, _, p := setupPushProcessing(t)
	defer s.Close()
	first := githubPushFixture()
	reverse := first
	reverse.Before, reverse.After = first.After, first.Before
	if err := s.AcceptGitHubPush(t.Context(), reverse, githubPushHash(81)); err != nil {
		t.Fatal(err)
	}
	if err := s.AcceptGitHubPush(t.Context(), first, githubPushHash(82)); err != nil {
		t.Fatal(err)
	}
	// Returning to the same commit is new work, but retrying either signed payload
	// must not create additional imports or pipelines.
	for _, hash := range []string{githubPushHash(31), githubPushHash(81), githubPushHash(82)} {
		if err := s.AcceptGitHubPush(t.Context(), first, hash); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := s.db.QueryRow("SELECT count(*) FROM github_push_events WHERE project_id=?", p.ID).Scan(&count); err != nil || count != 3 {
		t.Fatal(count, err)
	}
}

func TestGitHubPushReplayMigrationPreservesPipelineReferences(t *testing.T) {
	s, _, _ := readyStaticPipeline(t)
	pipeline, err := s.NextGitHubPipeline(t.Context())
	if err != nil || pipeline == nil {
		t.Fatal(pipeline, err)
	}
	if err = s.AdvanceGitHubPipeline(t.Context(), pipeline.ID, pipeline.Commit, nil, true); err != nil {
		t.Fatal(err)
	}
	var path, ddl string
	if err = s.db.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&path); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name='github_push_events'").Scan(&ddl); err != nil {
		t.Fatal(err)
	}
	// Reconstruct the actual schema 52 parent constraint before exercising upgrade.
	ddl = strings.Replace(ddl, `"github_push_events"`, "github_push_events_old", 1)
	ddl = strings.Replace(ddl, "UNIQUE(project_id,payload_hash)", "UNIQUE(project_id,connection_revision,ref,before_sha,after_sha,deleted)", 1)
	if !strings.Contains(ddl, "github_push_events_old") || !strings.Contains(ddl, "UNIQUE(project_id,connection_revision") {
		t.Fatal("fixture failed to reconstruct schema52")
	}
	conn, err := s.db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = conn.ExecContext(t.Context(), "PRAGMA foreign_keys=OFF"); err != nil {
		t.Fatal(err)
	}
	tx, err := conn.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ddl + `;INSERT INTO github_push_events_old SELECT * FROM github_push_events;DROP TABLE github_push_events;ALTER TABLE github_push_events_old RENAME TO github_push_events;CREATE INDEX github_push_events_payload ON github_push_events(payload_hash);CREATE INDEX github_push_events_project ON github_push_events(project_id,created_at,id);PRAGMA user_version=52;`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.ExecContext(t.Context(), "PRAGMA foreign_keys=ON"); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var count int
	if err = s.db.QueryRow("SELECT count(*) FROM github_pipelines p JOIN github_push_events e ON e.id=p.event_id JOIN publication_jobs j ON j.id=p.publication_id WHERE p.id=?", pipeline.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	if err = s.db.QueryRow("SELECT count(*) FROM pragma_foreign_key_check").Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	if err = s.db.QueryRow("PRAGMA foreign_keys").Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	if err = s.AcceptGitHubPush(t.Context(), githubPushFixture(), githubPushHash(83)); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRow("SELECT count(*) FROM github_push_events").Scan(&count); err != nil || count != 2 {
		t.Fatal(count, err)
	}
}
