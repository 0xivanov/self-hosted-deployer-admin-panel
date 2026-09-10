//go:build integration

package publisher

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/x509"
	"database/sql"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/portal"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/staticpublish"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/staticsite"
)

type lostResponse struct {
	client *staticpublish.Client
	lose   bool
}

func (r *lostResponse) Project() string { return r.client.Project() }
func (r *lostResponse) Publish(ctx context.Context, revision int64, data []byte) (staticpublish.Status, error) {
	status, err := r.client.Publish(ctx, revision, data)
	if err == nil && r.lose {
		r.lose = false
		return staticpublish.Status{}, errors.New("simulated lost response")
	}
	return status, err
}
func TestWorkerRecoversLostRuntimeAcknowledgement(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	dbpath := filepath.Join(t.TempDir(), "private", "portal.db")
	store, err := portal.Open(dbpath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	a, verify, err := store.Register(ctx, "worker@example.test", "synthetic test password", "workspace")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Verify(ctx, verify); err != nil {
		t.Fatal(err)
	}
	session, err := store.Login(ctx, a.Email, "synthetic test password")
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.CreateProject(ctx, session.Token, a.WorkspaceID, "site", "static")
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	f, err := z.Create("index.html")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write([]byte("Worker fixture")); err != nil {
		t.Fatal(err)
	}
	if err = z.Close(); err != nil {
		t.Fatal(err)
	}
	upload, err := store.SaveUpload(ctx, session.Token, project.ID, b.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.RequestPublication(ctx, session.Token, project.ID, upload.ID, "worker-request-key")
	if err != nil {
		t.Fatal(err)
	}
	site, err := staticsite.Open(filepath.Join(t.TempDir(), "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	defer site.Close()
	server := httptest.NewUnstartedServer(nil)
	defer server.Close()
	secret := strings.Repeat("b", 64)
	h, err := staticpublish.Handler(site, server.Listener.Addr().String(), project.ID, secret)
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = h
	server.StartTLS()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	client, err := staticpublish.NewClient(server.URL, project.ID, secret, roots)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	worker, err := New(store, &lostResponse{client: client, lose: true})
	if err != nil {
		t.Fatal(err)
	}
	if worked, err := worker.Once(ctx); !worked || err == nil {
		t.Fatal("lost response not retained", worked, err)
	}
	jobs, active, err := store.PublicationJobs(ctx, session.Token, project.ID)
	if err != nil || active != "" || jobs[0].State != "running" || site.Active() != upload.SHA256 {
		t.Fatal("uncertain state lost", jobs, active, err)
	}
	if worked, err := worker.Once(ctx); worked || err != nil {
		t.Fatal("unexpired lease reclaimed", worked, err)
	}
	// Emulate expiration without waiting a real minute. No job payload is changed.
	fixture, err := sql.Open("sqlite", dbpath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.Exec("UPDATE publication_jobs SET lease_until=0 WHERE id=?", job.ID); err != nil {
		t.Fatal(err)
	}
	fixture.Close()
	if worked, err := worker.Once(ctx); !worked || err != nil {
		t.Fatal("retry did not reconcile", worked, err)
	}
	jobs, active, err = store.PublicationJobs(ctx, session.Token, project.ID)
	if err != nil || active != job.ID || jobs[0].State != "succeeded" || jobs[0].Attempts != 2 || site.Revision() != job.Revision {
		t.Fatal(jobs, active, err)
	}
	if worked, err := worker.Once(ctx); worked || err != nil {
		t.Fatal("completed job repeated", worked, err)
	}
}
