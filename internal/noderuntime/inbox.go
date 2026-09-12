// Package noderuntime stores accepted deployment work on the assigned runtime.
package noderuntime

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/nodeartifact"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/portal"
	_ "modernc.org/sqlite"
)

var ErrAssignment = errors.New("invalid deployment inbox assignment")
var ErrConflict = errors.New("deployment operation conflicts with accepted work")
var ErrCapacity = errors.New("deployment inbox capacity reached")

type Assignment struct{ ProjectID, RuntimeID, ToolchainSHA256, Architecture string }
type Inbox struct {
	db         *sql.DB
	assignment Assignment
}
type Work struct {
	Request portal.NodeRuntimeRequest
	State   string
}

func validID(s string) bool {
	data, err := hex.DecodeString(s)
	return err == nil && len(data) == 32 && hex.EncodeToString(data) == s
}

// Open binds a private durable inbox to one project's runtime and toolchain.
// Keep this database and its operation records across restarts and retirement.
func Open(database string, a Assignment) (*Inbox, error) {
	if !validID(a.ProjectID) || !validID(a.RuntimeID) || !validID(a.ToolchainSHA256) || (a.Architecture != "arm64" && a.Architecture != "amd64") {
		return nil, ErrAssignment
	}
	absolute, err := filepath.Abs(database)
	if err != nil {
		return nil, err
	}
	directory := filepath.Dir(absolute)
	if err = os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, ErrAssignment
	}
	f, err := os.OpenFile(absolute, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err == nil {
		if err = f.Close(); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	info, err = os.Lstat(absolute)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, ErrAssignment
	}
	location := url.URL{Scheme: "file", Path: absolute}
	q := url.Values{}
	q.Set("_txlock", "immediate")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "synchronous(FULL)")
	location.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", location.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	inbox := &Inbox{db: db, assignment: a}
	if err = inbox.initialize(); err != nil {
		db.Close()
		return nil, err
	}
	return inbox, nil
}
func (i *Inbox) Close() error { return i.db.Close() }
func (i *Inbox) initialize() error {
	tx, err := i.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err = tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > 1 {
		return ErrAssignment
	}
	if version == 0 {
		if _, err = tx.Exec(`CREATE TABLE runtime_assignment(id INTEGER PRIMARY KEY CHECK(id=1),config BLOB NOT NULL);CREATE TABLE deployments(operation TEXT PRIMARY KEY,deployment TEXT NOT NULL UNIQUE,revision INTEGER NOT NULL UNIQUE,request BLOB NOT NULL,archive BLOB NOT NULL,state TEXT NOT NULL CHECK(state IN ('queued','processing','settled')));CREATE UNIQUE INDEX deployment_processing ON deployments(state) WHERE state='processing';PRAGMA user_version=1;`); err != nil {
			return err
		}
	}
	raw, _ := json.Marshal(i.assignment)
	var previous []byte
	err = tx.QueryRow("SELECT config FROM runtime_assignment WHERE id=1").Scan(&previous)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = tx.Exec("INSERT INTO runtime_assignment VALUES(1,?)", raw)
	} else if err == nil && !bytes.Equal(raw, previous) {
		return ErrAssignment
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

var _ portal.NodeRuntimeSubmitter = (*Inbox)(nil)

// SubmitNodeRuntime validates and durably retains bytes before acknowledging.
// Exact duplicate submissions never enqueue another launch. Metadata is immutable;
// an existing operation cannot change artifact, revision or activation deadline.
func (i *Inbox) SubmitNodeRuntime(ctx context.Context, r portal.NodeRuntimeRequest) error {
	a := i.assignment
	if r.ProjectID != a.ProjectID || r.RuntimeID != a.RuntimeID || r.ToolchainSHA256 != a.ToolchainSHA256 || r.Architecture != a.Architecture || !validID(r.OperationID) || !validID(r.DeploymentID) || !validID(r.ReleaseID) || !validID(r.ArtifactSHA256) || r.Revision < 1 {
		return ErrAssignment
	}
	if _, err := nodeartifact.Validate(ctx, r.Archive, r.ArtifactSHA256); err != nil {
		return err
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	tx, err := i.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var previous []byte
	err = tx.QueryRowContext(ctx, "SELECT request FROM deployments WHERE operation=?", r.OperationID).Scan(&previous)
	if err == nil {
		if !bytes.Equal(raw, previous) {
			return ErrConflict
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if r.ActivateBefore <= time.Now().Unix() || r.ActivateBefore > time.Now().Add(90*time.Second).Unix() {
		return ErrAssignment
	}
	var maximum, total, size int64
	if err = tx.QueryRowContext(ctx, "SELECT COALESCE(max(revision),0),count(*),COALESCE(sum(length(archive)),0) FROM deployments").Scan(&maximum, &total, &size); err != nil {
		return err
	}
	if r.Revision <= maximum {
		return ErrConflict
	}
	if total >= 1000 || size+int64(len(r.Archive)) > 200<<20 {
		return ErrCapacity
	}
	var duplicate int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM deployments WHERE deployment=?", r.DeploymentID).Scan(&duplicate); err != nil {
		return err
	}
	if duplicate != 0 {
		return ErrConflict
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO deployments VALUES(?,?,?,?,?,'queued')", r.OperationID, r.DeploymentID, r.Revision, raw, r.Archive); err != nil {
		return err
	}
	return tx.Commit()
}
func readWork(row interface{ Scan(...any) error }) (*Work, error) {
	var work Work
	var raw, archive []byte
	if err := row.Scan(&raw, &archive, &work.State); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &work.Request); err != nil {
		return nil, err
	}
	work.Request.Archive = archive
	return &work, nil
}
func (i *Inbox) Lookup(ctx context.Context, operation string) (*Work, error) {
	if !validID(operation) {
		return nil, ErrAssignment
	}
	return readWork(i.db.QueryRowContext(ctx, "SELECT request,archive,state FROM deployments WHERE operation=?", operation))
}

// Claim selects one request exactly once. Processing work survives restart and
// prevents another claim until the runtime reconciles it. A passed deadline is
// not permission to skip cleanup: the runner must reject activation and settle
// the operation through its runtime fences before marking it complete.
func (i *Inbox) Claim(ctx context.Context) (*Work, error) {
	tx, err := i.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var pending int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM deployments WHERE state='processing'").Scan(&pending); err != nil {
		return nil, err
	}
	if pending != 0 {
		return nil, nil
	}
	work, err := readWork(tx.QueryRowContext(ctx, "SELECT request,archive,state FROM deployments WHERE state='queued' ORDER BY revision LIMIT 1"))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err = nodeartifact.Validate(ctx, work.Request.Archive, work.Request.ArtifactSHA256); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE deployments SET state='processing' WHERE operation=?", work.Request.OperationID); err != nil {
		return nil, err
	}
	work.State = "processing"
	return work, tx.Commit()
}

// Settle is trusted runner bookkeeping after verified activation or retirement.
// It never provides runtime evidence by itself and retains archive and identity.
func (i *Inbox) Settle(ctx context.Context, operation string) error {
	if !validID(operation) {
		return ErrAssignment
	}
	result, err := i.db.ExecContext(ctx, "UPDATE deployments SET state='settled' WHERE operation=? AND state IN ('processing','settled')", operation)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	return nil
}

// Processing returns unfinished work for startup reconciliation, never a fresh
// start authorization. The caller must inspect durable runtime gates first.
func (i *Inbox) Processing(ctx context.Context) (*Work, error) {
	work, err := readWork(i.db.QueryRowContext(ctx, "SELECT request,archive,state FROM deployments WHERE state='processing'"))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return work, err
}
