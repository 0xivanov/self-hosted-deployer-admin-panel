package portal

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var ErrRetained = errors.New("upload retained for publication")
var ErrPublishing = errors.New("a publication is already pending")
var ErrConflict = errors.New("publication request conflicts with existing request")

type PublicationJob struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	UploadID  string `json:"upload_id"`
	Revision  int64  `json:"revision"`
	State     string `json:"state"`
	Attempts  int    `json:"attempts"`
	CreatedAt int64  `json:"created_at"`
}
type PublicationClaim struct {
	Job     PublicationJob
	Lease   string
	SHA256  string
	Archive []byte
}

const jobColumns = "id,project_id,upload_id,revision,state,attempts,created_at"

func scanJob(row interface{ Scan(...any) error }) (PublicationJob, error) {
	var j PublicationJob
	err := row.Scan(&j.ID, &j.ProjectID, &j.UploadID, &j.Revision, &j.State, &j.Attempts, &j.CreatedAt)
	return j, err
}

// RequestPublication retains the selected immutable upload. Retrying the same
// key returns the same job; a different payload cannot silently reuse that key.
// Selecting an earlier retained upload uses the same path for rollback.
func (s *Store) RequestPublication(ctx context.Context, token, project, upload, key string) (PublicationJob, error) {
	if len(key) < 16 || len(key) > 128 {
		return PublicationJob{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PublicationJob{}, err
	}
	defer tx.Rollback()
	p, actor, err := s.uploadProject(ctx, tx, token, project, true)
	if err != nil {
		return PublicationJob{}, err
	}
	if p.Kind != "static" {
		return PublicationJob{}, ErrInvalid
	}
	existing, err := scanJob(tx.QueryRowContext(ctx, "SELECT "+jobColumns+" FROM publication_jobs WHERE project_id=? AND request_key=?", project, key))
	if err == nil {
		if existing.UploadID != upload {
			return PublicationJob{}, ErrConflict
		}
		return existing, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return PublicationJob{}, err
	}
	var valid int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM uploads WHERE id=? AND project_id=?", upload, project).Scan(&valid); err != nil {
		return PublicationJob{}, err
	}
	if valid != 1 {
		return PublicationJob{}, ErrDenied
	}
	var pending int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM publication_jobs WHERE project_id=? AND state IN ('queued','running')", project).Scan(&pending); err != nil {
		return PublicationJob{}, err
	}
	if pending != 0 {
		return PublicationJob{}, ErrPublishing
	}
	var revision int64
	if err = tx.QueryRowContext(ctx, "SELECT COALESCE(max(revision),0)+1 FROM publication_jobs WHERE project_id=?", project).Scan(&revision); err != nil {
		return PublicationJob{}, err
	}
	j := PublicationJob{ID: randomToken(), ProjectID: project, UploadID: upload, Revision: revision, State: "queued", CreatedAt: s.now().Unix()}
	if _, err = tx.ExecContext(ctx, "INSERT INTO publication_jobs(id,project_id,upload_id,actor_id,request_key,revision,state,created_at) VALUES(?,?,?,?,?,?,?,?)", j.ID, project, upload, actor, key, revision, j.State, j.CreatedAt); err != nil {
		return PublicationJob{}, err
	}
	if err = audit(ctx, tx, actor, p.WorkspaceID, "publication.requested:"+j.ID, j.CreatedAt); err != nil {
		return PublicationJob{}, err
	}
	return j, tx.Commit()
}

func (s *Store) PublicationJobs(ctx context.Context, token, project string) ([]PublicationJob, string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()
	if _, _, err = s.uploadProject(ctx, tx, token, project, false); err != nil {
		return nil, "", err
	}
	rows, err := tx.QueryContext(ctx, "SELECT "+jobColumns+" FROM publication_jobs WHERE project_id=? ORDER BY revision DESC LIMIT 100", project)
	if err != nil {
		return nil, "", err
	}
	jobs := []PublicationJob{}
	for rows.Next() {
		j, e := scanJob(rows)
		if e != nil {
			rows.Close()
			return nil, "", e
		}
		jobs = append(jobs, j)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, "", err
	}
	var active string
	err = tx.QueryRowContext(ctx, "SELECT job_id FROM publications WHERE project_id=?", project).Scan(&active)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, "", err
	}
	return jobs, active, tx.Commit()
}

// publicationActor rechecks current account and workspace write permission.
// A job does not depend on a browser session remaining open during deployment.
func publicationActor(ctx context.Context, tx *sql.Tx, job string) (bool, error) {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM publication_jobs j JOIN projects p ON p.id=j.project_id JOIN users u ON u.id=j.actor_id JOIN memberships m ON m.user_id=u.id AND m.workspace_id=p.workspace_id WHERE j.id=? AND u.verified=1 AND u.disabled=0 AND m.role IN ('owner','developer')`, job).Scan(&n)
	return n == 1, err
}

// ClaimPublication is a trusted worker operation, never a customer API. The
// worker must resolve project to an operator-assigned runtime and enforce the
// monotonically increasing revision at that runtime before applying anything.
// An expired lease can be reclaimed, so remote application must be idempotent.
func (s *Store) ClaimPublication(ctx context.Context, project string) (*PublicationClaim, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	j, err := scanJob(tx.QueryRowContext(ctx, "SELECT "+jobColumns+" FROM publication_jobs WHERE project_id=? AND (state='queued' OR (state='running' AND lease_until<=?))", project, s.now().Unix()))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	valid, err := publicationActor(ctx, tx, j.ID)
	if err != nil {
		return nil, err
	}
	if !valid {
		if _, err = tx.ExecContext(ctx, "UPDATE publication_jobs SET state='failed',lease_hash='',lease_until=0 WHERE id=?", j.ID); err != nil {
			return nil, err
		}
		return nil, tx.Commit()
	}
	c := &PublicationClaim{Job: j, Lease: randomToken()}
	if err = tx.QueryRowContext(ctx, "SELECT sha256,archive FROM uploads WHERE id=? AND project_id=?", j.UploadID, project).Scan(&c.SHA256, &c.Archive); err != nil {
		return nil, err
	}
	c.Job.State = "running"
	c.Job.Attempts++
	if _, err = tx.ExecContext(ctx, "UPDATE publication_jobs SET state='running',lease_hash=?,lease_until=?,attempts=attempts+1 WHERE id=?", digest(c.Lease), s.now().Add(time.Minute).Unix(), j.ID); err != nil {
		return nil, err
	}
	return c, tx.Commit()
}

// FinishPublication acknowledges a current lease only. Success requires the
// runtime's observed immutable release hash. Failure leaves the previous active
// publication intact. A lost/expired lease must reconcile, never blindly finish.
func (s *Store) FinishPublication(ctx context.Context, job, lease, observedHash string, success bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var project, upload, expected, actor, workspace string
	err = tx.QueryRowContext(ctx, `SELECT j.project_id,j.upload_id,u.sha256,j.actor_id,p.workspace_id FROM publication_jobs j JOIN uploads u ON u.id=j.upload_id JOIN projects p ON p.id=j.project_id WHERE j.id=? AND j.state='running' AND j.lease_hash=? AND j.lease_until>?`, job, digest(lease), s.now().Unix()).Scan(&project, &upload, &expected, &actor, &workspace)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	valid, err := publicationActor(ctx, tx, job)
	if err != nil {
		return err
	}
	if !valid {
		return ErrDenied
	}
	state := "failed"
	if success {
		if observedHash != expected {
			return ErrConflict
		}
		state = "succeeded"
	}
	if _, err = tx.ExecContext(ctx, "UPDATE publication_jobs SET state=?,lease_hash='',lease_until=0 WHERE id=?", state, job); err != nil {
		return err
	}
	if success {
		if _, err = tx.ExecContext(ctx, "INSERT INTO publications VALUES(?,?) ON CONFLICT(project_id) DO UPDATE SET job_id=excluded.job_id", project, job); err != nil {
			return err
		}
	}
	if err = audit(ctx, tx, actor, workspace, "publication."+state+":"+job, s.now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}
