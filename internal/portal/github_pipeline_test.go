//go:build integration

package portal

import (
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodebuild"
	"strings"
	"testing"
)

func readyStaticPipeline(t *testing.T) (*Store, Session, Project) {
	t.Helper()
	s, session, p := setupPushProcessing(t)
	now := s.now().Unix()
	if _, err := s.db.Exec("INSERT INTO uploads VALUES(?,?,?,?,?,?,?)", "pipeline-upload", p.ID, "pipeline-sha", 1, 1, []byte("zip"), now); err != nil {
		t.Fatal(err)
	}
	var eventID, actor string
	if err := s.db.QueryRow("SELECT id,actor_id FROM github_push_events WHERE project_id=?", p.ID).Scan(&eventID, &actor); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("INSERT INTO github_imports(id,project_id,request_key,actor_id,revision,commit_sha,upload_id,state,created_at) VALUES(?,?,?,?,?,?,?,?,?)", "pipeline-import", p.ID, "github-push:"+eventID, actor, 1, strings.Repeat("b", 40), "pipeline-upload", "succeeded", now); err != nil {
		t.Fatal(err)
	}
	return s, session, p
}

func TestGitHubPipelineNodeBuildAndDeploymentQueue(t *testing.T) {
	s, owner, session, _, _, _ := projectClientFixture(t)
	defer s.Close()
	p, err := s.CreateProject(t.Context(), session.Token, owner.WorkspaceID, "Node Website", "node")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveGitHubConnection(t.Context(), session.Token, githubConnectionFixture(t, s, session.Token, p.ID), githubAccessFixture(), "main", "", true); err != nil {
		t.Fatal(err)
	}
	if err = s.AcceptGitHubPush(t.Context(), githubPushFixture(), githubPushHash(33)); err != nil {
		t.Fatal(err)
	}
	now := s.now().Unix()
	upload, err := s.SaveUpload(t.Context(), session.Token, p.ID, nodeUploadFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	var eventID, actor string
	if err = s.db.QueryRow("SELECT id,actor_id FROM github_push_events WHERE project_id=?", p.ID).Scan(&eventID, &actor); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("INSERT INTO github_imports(id,project_id,request_key,actor_id,revision,commit_sha,upload_id,state,created_at) VALUES(?,?,?,?,?,?,?,?,?)", "node-import", p.ID, "github-push:"+eventID, actor, 1, strings.Repeat("b", 40), upload.ID, "succeeded", now); err != nil {
		t.Fatal(err)
	}
	pipeline, err := s.NextGitHubPipeline(t.Context())
	if err != nil || pipeline == nil {
		t.Fatal(pipeline, err)
	}
	assignment := &NodeProjectConfig{RuntimeID: strings.Repeat("d", 64), Build: NodeBuildAssignment{Settings: nodebuild.Settings{Architecture: "arm64"}, ToolchainSHA256: strings.Repeat("a", 64)}}
	if err = s.AdvanceGitHubPipeline(t.Context(), pipeline.ID, pipeline.Commit, assignment, false); err != nil {
		t.Fatal(err)
	}
	var buildID string
	if err = s.db.QueryRow("SELECT build_id FROM github_pipelines WHERE id=?", pipeline.ID).Scan(&buildID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("UPDATE node_builds SET state='succeeded' WHERE id=?", buildID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("INSERT INTO node_releases(build_id,metadata,archive,compressed_bytes) VALUES(?,?,?,?)", buildID, []byte("{}"), []byte("x"), 1); err != nil {
		t.Fatal(err)
	}
	if err = s.AdvanceGitHubPipeline(t.Context(), pipeline.ID, pipeline.Commit, assignment, false); err != nil {
		t.Fatal(err)
	}
	var deploymentID string
	if err = s.db.QueryRow("SELECT deployment_id FROM github_pipelines WHERE id=?", pipeline.ID).Scan(&deploymentID); err != nil || deploymentID == "" {
		t.Fatal(deploymentID, err)
	}
	if _, err = s.db.Exec("UPDATE node_deployments SET state='succeeded' WHERE id=?", deploymentID); err != nil {
		t.Fatal(err)
	}
	if err = s.AdvanceGitHubPipeline(t.Context(), pipeline.ID, "", nil, false); err != nil {
		t.Fatal(err)
	}
	var state string
	if err = s.db.QueryRow("SELECT state FROM github_pipelines WHERE id=?", pipeline.ID).Scan(&state); err != nil || state != "succeeded" {
		t.Fatal(state, err)
	}
}

func TestGitHubPipelineStaticQueueAndDuplicateAdvance(t *testing.T) {
	s, session, p := readyStaticPipeline(t)
	defer s.Close()
	pipeline, err := s.NextGitHubPipeline(t.Context())
	if err != nil || pipeline == nil {
		t.Fatal(pipeline, err)
	}
	if err = s.AdvanceGitHubPipeline(t.Context(), pipeline.ID, pipeline.Commit, nil, true); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = s.db.QueryRow("SELECT count(*) FROM publication_jobs WHERE project_id=?", p.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	if err = s.AdvanceGitHubPipeline(t.Context(), pipeline.ID, pipeline.Commit, nil, true); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRow("SELECT count(*) FROM publication_jobs WHERE project_id=?", p.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	if _, _, err = s.PublicationJobs(t.Context(), session.Token, p.ID); err != nil {
		t.Fatal(err)
	}
}

func TestGitHubPipelineStaleHeadAndRevokedConnection(t *testing.T) {
	s, _, p := readyStaticPipeline(t)
	defer s.Close()
	pipeline, err := s.NextGitHubPipeline(t.Context())
	if err != nil || pipeline == nil {
		t.Fatal(pipeline, err)
	}
	if err = s.AdvanceGitHubPipeline(t.Context(), pipeline.ID, strings.Repeat("c", 40), nil, true); err != nil {
		t.Fatal(err)
	}
	var state string
	if err = s.db.QueryRow("SELECT state FROM github_pipelines WHERE id=?", pipeline.ID).Scan(&state); err != nil || state != "superseded" {
		t.Fatal(state, err)
	}
	if _, err = s.NextGitHubPipeline(t.Context()); err != nil {
		t.Fatal(err)
	}
	_ = p
}

func TestGitHubPipelineSchema51MigrationPreservesInbox(t *testing.T) {
	s, _, _ := readyStaticPipeline(t)
	var path string
	if err := s.db.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&path); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("DROP TABLE github_pipelines; PRAGMA user_version=51"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var count int
	if err = s.db.QueryRow("SELECT count(*) FROM github_push_events").Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	if err = s.db.QueryRow("PRAGMA user_version").Scan(&count); err != nil || count != 57 {
		t.Fatal(count, err)
	}
}
