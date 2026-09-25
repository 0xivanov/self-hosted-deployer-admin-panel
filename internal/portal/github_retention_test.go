//go:build integration

package portal

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestGitHubRetentionKeepsPendingRecentAndReplayReceipts(t *testing.T) {
	s, _, p := setupPushProcessing(t)
	defer s.Close()
	now := s.now()
	old := now.Add(-31 * 24 * time.Hour).Unix()
	for n := 0; n < 25; n++ {
		hash := githubPushHash(byte(100 + n))
		if err := s.AcceptGitHubPush(t.Context(), githubPushFixture(), hash); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec("UPDATE github_push_events SET created_at=? WHERE payload_hash=?", old+int64(n), hash); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec("INSERT INTO github_push_processing(event_id,state,created_at) SELECT id,'skipped',created_at FROM github_push_events WHERE payload_hash=?", hash); err != nil {
			t.Fatal(err)
		}
	}
	result, err := s.PruneGitHubHistory(t.Context())
	if err != nil || result.Events != 6 || result.Receipts != 0 {
		t.Fatal(result, err)
	}
	// The initial pending event plus 19 recent completed events remain.
	var count int
	if err = s.db.QueryRow("SELECT count(*) FROM github_push_events WHERE project_id=?", p.ID).Scan(&count); err != nil || count != 20 {
		t.Fatal(count, err)
	}
	if err = s.AcceptGitHubPush(t.Context(), githubPushFixture(), githubPushHash(100)); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRow("SELECT count(*) FROM github_push_events WHERE project_id=?", p.ID).Scan(&count); err != nil || count != 20 {
		t.Fatal("old exact delivery recreated deleted event", count, err)
	}
	// Old orphan receipts expire, but an equally old receipt with retained work does not.
	if _, err = s.db.Exec("UPDATE github_push_receipts SET created_at=?", now.Add(-91*24*time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	result, err = s.PruneGitHubHistory(t.Context())
	if err != nil || result.Receipts != 6 {
		t.Fatal(result, err)
	}
	if err = s.db.QueryRow("SELECT count(*) FROM github_push_receipts").Scan(&count); err != nil || count != 20 {
		t.Fatal(count, err)
	}
}

func TestGitHubRetentionPreservesLivePublicationAndQueuedWork(t *testing.T) {
	for _, live := range []bool{true, false} {
		t.Run(fmt.Sprint(live), func(t *testing.T) {
			s, _, p := readyStaticPipeline(t)
			defer s.Close()
			pipeline, err := s.NextGitHubPipeline(t.Context())
			if err != nil || pipeline == nil {
				t.Fatal(pipeline, err)
			}
			if err = s.AdvanceGitHubPipeline(t.Context(), pipeline.ID, pipeline.Commit, nil, true); err != nil {
				t.Fatal(err)
			}
			if live {
				if _, err = s.db.Exec("UPDATE publication_jobs SET state='succeeded' WHERE project_id=?", p.ID); err != nil {
					t.Fatal(err)
				}
				if _, err = s.db.Exec("INSERT INTO publications(project_id,job_id) SELECT project_id,id FROM publication_jobs WHERE project_id=?", p.ID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = s.db.Exec("UPDATE github_pipelines SET state='succeeded',updated_at=1;UPDATE github_push_events SET created_at=1;INSERT INTO github_push_processing(event_id,state,created_at) SELECT id,'imported',1 FROM github_push_events"); err != nil {
				t.Fatal(err)
			}
			for n := 0; n < 21; n++ {
				if err = s.AcceptGitHubPush(t.Context(), githubPushFixture(), githubPushHash(byte(150+n))); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = s.PruneGitHubHistory(t.Context()); err != nil {
				t.Fatal(err)
			}
			var count int
			if err = s.db.QueryRow("SELECT count(*) FROM github_pipelines WHERE id=?", pipeline.ID).Scan(&count); err != nil || count != 1 {
				t.Fatal("lost current/queued publication history", count, err)
			}
			if err = s.db.QueryRow("SELECT count(*) FROM uploads WHERE project_id=?", p.ID).Scan(&count); err != nil || count != 1 {
				t.Fatal("deleted source", count, err)
			}
		})
	}
}

func TestGitHubRetentionPrunesOnlyOldCompletedUnlinkedImports(t *testing.T) {
	s, session, p := setupPushProcessing(t)
	defer s.Close()
	var actor string
	if err := s.db.QueryRow("SELECT actor_id FROM github_connections WHERE project_id=?", p.ID).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	old := s.now().Add(-31 * 24 * time.Hour).Unix()
	for n := 0; n < 25; n++ {
		id := fmt.Sprintf("old-import-%02d", n)
		if _, err := s.db.Exec("INSERT INTO github_imports(id,project_id,request_key,actor_id,revision,commit_sha,state,created_at) VALUES(?,?,?,?,1,?,'failed',?)", id, p.ID, id, actor, strings.Repeat("a", 40), old+int64(n)); err != nil {
			t.Fatal(err)
		}
	}
	pending, err := s.RequestGitHubImport(t.Context(), session.Token, p.ID, "pending-retention-import")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("UPDATE github_imports SET created_at=? WHERE id=?", old-1, pending.ID); err != nil {
		t.Fatal(err)
	}
	result, err := s.PruneGitHubHistory(t.Context())
	if err != nil || result.Imports != 5 {
		t.Fatal(result, err)
	}
	var count int
	if err = s.db.QueryRow("SELECT count(*) FROM github_imports WHERE id=? AND state='queued'", pending.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
}
