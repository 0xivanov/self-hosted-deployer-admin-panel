package portal

import (
	"strings"
	"testing"
)

func TestGitHubPipelineWorkerStaticPublicationLifecycle(t *testing.T) {
	s, owner, session, _, _, p := projectClientFixture(t)
	defer s.Close()
	if _, err := s.SaveGitHubConnection(t.Context(), session.Token, githubConnectionFixture(t, s, session.Token, p.ID), githubAccessFixture(), "main", "", true); err != nil {
		t.Fatal(err)
	}
	push := githubPushFixture()
	push.After = strings.Repeat("a", 40)
	push.Before = strings.Repeat("b", 40)
	if err := s.AcceptGitHubPush(t.Context(), push, githubPushHash(90)); err != nil {
		t.Fatal(err)
	}
	provider := &githubWorkerSource{archive: githubWorkerArchive(t)}
	if _, err := s.WorkGitHubPush(t.Context(), provider); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WorkGitHubImport(t.Context(), provider); err != nil {
		t.Fatal(err)
	}
	sites := map[string]string{p.ID: "https://content.example.test"}
	if worked, err := s.WorkGitHubPipeline(t.Context(), provider, nil, sites); err != nil || !worked {
		t.Fatal(worked, err)
	}
	claim, err := s.ClaimPublication(t.Context(), p.ID)
	if err != nil || claim == nil {
		t.Fatal(claim, err)
	}
	if err = s.SetGitHubAutoDeploy(t.Context(), session.Token, p.ID, false); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishPublication(t.Context(), claim.Job.ID, claim.Lease, claim.SHA256, true); err != nil {
		t.Fatal(err)
	}
	if _, err = s.WorkGitHubPipeline(t.Context(), provider, nil, sites); err != nil {
		t.Fatal(err)
	}
	jobs, err := s.GitHubPipelines(t.Context(), session.Token, p.ID)
	if err != nil || len(jobs) != 1 || jobs[0].State != "succeeded" || jobs[0].Commit != push.After {
		t.Fatal(jobs, err)
	}
	if err = s.SetGitHubAutoDeploy(t.Context(), session.Token, p.ID, true); err != nil {
		t.Fatal(err)
	}
	// A waiting site must not prevent another imported site from entering the pipeline.
	second, err := s.CreateProject(t.Context(), session.Token, owner.WorkspaceID, "Second GitHub site", "static")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveGitHubConnection(t.Context(), session.Token, githubConnectionFixture(t, s, session.Token, second.ID), githubAccessFixture(), "main", "", true); err != nil {
		t.Fatal(err)
	}
	push.Before = strings.Repeat("c", 40)
	if err = s.AcceptGitHubPush(t.Context(), push, githubPushHash(91)); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err = s.WorkGitHubPush(t.Context(), provider); err != nil {
			t.Fatal(err)
		}
		if _, err = s.WorkGitHubImport(t.Context(), provider); err != nil {
			t.Fatal(err)
		}
	}
	// No assigned origin for either site, so each remains waiting.
	for range 3 {
		if _, err = s.WorkGitHubPipeline(t.Context(), provider, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	other, err := s.GitHubPipelines(t.Context(), session.Token, second.ID)
	if err != nil || len(other) != 1 {
		t.Fatal(other, err)
	}
}

func TestGitHubPipelineDisconnectPreventsPublicationClaim(t *testing.T) {
	s, _, session, _, _, p := projectClientFixture(t)
	defer s.Close()
	if _, err := s.SaveGitHubConnection(t.Context(), session.Token, githubConnectionFixture(t, s, session.Token, p.ID), githubAccessFixture(), "main", "", true); err != nil {
		t.Fatal(err)
	}
	push := githubPushFixture()
	push.After = strings.Repeat("a", 40)
	if err := s.AcceptGitHubPush(t.Context(), push, githubPushHash(92)); err != nil {
		t.Fatal(err)
	}
	provider := &githubWorkerSource{archive: githubWorkerArchive(t)}
	if _, err := s.WorkGitHubPush(t.Context(), provider); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WorkGitHubImport(t.Context(), provider); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WorkGitHubPipeline(t.Context(), provider, nil, map[string]string{p.ID: "https://content.example.test"}); err != nil {
		t.Fatal(err)
	}
	if err := s.DisconnectGitHub(t.Context(), session.Token, p.ID); err != nil {
		t.Fatal(err)
	}
	if claim, err := s.ClaimPublication(t.Context(), p.ID); err != nil || claim != nil {
		t.Fatal(claim, err)
	}
	if _, err := s.WorkGitHubPipeline(t.Context(), provider, nil, nil); err != nil {
		t.Fatal(err)
	}
	jobs, err := s.GitHubPipelines(t.Context(), session.Token, p.ID)
	if err != nil || len(jobs) != 1 || jobs[0].State != "failed" {
		t.Fatal(jobs, err)
	}
}
