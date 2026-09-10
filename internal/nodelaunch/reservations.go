package nodelaunch

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

var (
	ErrConflict   = errors.New("Node reservation conflicts with recorded state")
	ErrCapacity   = errors.New("Node runtime has no free service slots")
	ErrNotFound   = errors.New("Node reservation not found")
	ErrRetirement = errors.New("Node runtime retirement is not proven")
)

type Slot struct{ UID, Port int }
type PoolConfig struct {
	ProjectID, RuntimeID, ToolchainSHA256, Architecture string
	Slots                                               []Slot
}
type Reservation struct {
	Assignment            Assignment
	State                 string
	Retirement            *RetirementObservation
	InstallationAttempted bool
	Installed             *InstalledRelease
}
type Pool struct {
	db     *sql.DB
	config PoolConfig
}

// OpenPool pins a dedicated project's runtime identity and operator-reserved
// UID/port pairs. This database must outlive services and must never be restored
// to an earlier snapshot while a process or delayed start could still exist.
// The directory and all its ancestors must be controlled by the trusted agent.
func OpenPool(database string, config PoolConfig) (*Pool, error) {
	if !id(config.ProjectID) || !id(config.RuntimeID) || !id(config.ToolchainSHA256) || (config.Architecture != "arm64" && config.Architecture != "amd64") || len(config.Slots) < 2 || len(config.Slots) > 16 {
		return nil, ErrAssignment
	}
	uids, ports := map[int]bool{}, map[int]bool{}
	for _, slot := range config.Slots {
		if slot.UID < 60000 || slot.UID > 60999 || slot.Port < 1024 || slot.Port > 65535 || uids[slot.UID] || ports[slot.Port] {
			return nil, ErrAssignment
		}
		uids[slot.UID], ports[slot.Port] = true, true
	}
	config.Slots = append([]Slot(nil), config.Slots...)
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
	p := &Pool{db: db, config: config}
	if err = p.initialize(); err != nil {
		db.Close()
		return nil, err
	}
	return p, nil
}
func (p *Pool) Close() error { return p.db.Close() }
func (p *Pool) initialize() error {
	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err = tx.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > 2 {
		return ErrAssignment
	}
	if version == 0 {
		_, err = tx.Exec(`CREATE TABLE pool_config(id INTEGER PRIMARY KEY CHECK(id=1),config BLOB NOT NULL);
CREATE TABLE reservations(operation TEXT PRIMARY KEY,assignment BLOB NOT NULL,uid INTEGER NOT NULL,port INTEGER NOT NULL,state TEXT NOT NULL CHECK(state IN ('reserved','starting','retiring','retired')),retirement BLOB);
CREATE UNIQUE INDEX live_uid ON reservations(uid) WHERE state!='retired';
CREATE UNIQUE INDEX live_port ON reservations(port) WHERE state!='retired';
PRAGMA user_version=1;`)
		if err != nil {
			return err
		}
	}
	if version < 2 {
		if _, err = tx.Exec("ALTER TABLE reservations ADD COLUMN installation_attempted INTEGER NOT NULL DEFAULT 0 CHECK(installation_attempted IN (0,1)); ALTER TABLE reservations ADD COLUMN installed BLOB; PRAGMA user_version=2;"); err != nil {
			return err
		}
	}
	expected, err := json.Marshal(p.config)
	if err != nil {
		return err
	}
	var current []byte
	err = tx.QueryRow("SELECT config FROM pool_config WHERE id=1").Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = tx.Exec("INSERT INTO pool_config VALUES(1,?)", expected)
	} else if err == nil && !bytes.Equal(current, expected) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}
func readReservation(row interface{ Scan(...any) error }) (Reservation, error) {
	var r Reservation
	var raw, evidence, installed []byte
	if err := row.Scan(&raw, &r.State, &evidence, &r.InstallationAttempted, &installed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = ErrNotFound
		}
		return r, err
	}
	err := json.Unmarshal(raw, &r.Assignment)
	if err == nil && evidence != nil {
		err = json.Unmarshal(evidence, &r.Retirement)
	}
	if err == nil && installed != nil {
		err = json.Unmarshal(installed, &r.Installed)
	}
	return r, err
}
func (p *Pool) Lookup(ctx context.Context, operation string) (Reservation, error) {
	if !id(operation) {
		return Reservation{}, ErrAssignment
	}
	return readReservation(p.db.QueryRowContext(ctx, "SELECT assignment,state,retirement,installation_attempted,installed FROM reservations WHERE operation=?", operation))
}

// Reserve allocates a slot before any host mutation. UID and Port must be zero;
// the pool chooses them. An identical operation returns its persisted state,
// including a permanent retirement tombstone. It never resurrects old work.
func (p *Pool) Reserve(ctx context.Context, a Assignment) (Reservation, error) {
	if a.UID != 0 || a.Port != 0 || a.ProjectID != p.config.ProjectID || a.RuntimeID != p.config.RuntimeID || a.ToolchainSHA256 != p.config.ToolchainSHA256 || a.Architecture != p.config.Architecture {
		return Reservation{}, ErrAssignment
	}
	check := a
	check.UID = p.config.Slots[0].UID
	check.Port = p.config.Slots[0].Port
	if _, err := Render(check); err != nil {
		return Reservation{}, err
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return Reservation{}, err
	}
	defer tx.Rollback()
	old, err := readReservation(tx.QueryRowContext(ctx, "SELECT assignment,state,retirement,installation_attempted,installed FROM reservations WHERE operation=?", a.OperationID))
	if err == nil {
		a.UID = old.Assignment.UID
		a.Port = old.Assignment.Port
		if a != old.Assignment {
			return Reservation{}, ErrConflict
		}
		return old, tx.Commit()
	}
	if !errors.Is(err, ErrNotFound) {
		return Reservation{}, err
	}
	var aliases int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM reservations WHERE json_extract(assignment,'$.ReleaseDirectory')=?", a.ReleaseDirectory).Scan(&aliases); err != nil {
		return Reservation{}, err
	}
	if aliases != 0 {
		return Reservation{}, ErrConflict
	}
	for _, slot := range p.config.Slots {
		var count int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM reservations WHERE state!='retired' AND (uid=? OR port=?)", slot.UID, slot.Port).Scan(&count); err != nil {
			return Reservation{}, err
		}
		if count != 0 {
			continue
		}
		a.UID = slot.UID
		a.Port = slot.Port
		raw, e := json.Marshal(a)
		if e != nil {
			return Reservation{}, e
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO reservations(operation,assignment,uid,port,state) VALUES(?,?,?,?, 'reserved')", a.OperationID, raw, a.UID, a.Port); err != nil {
			return Reservation{}, err
		}
		return Reservation{Assignment: a, State: "reserved"}, tx.Commit()
	}
	return Reservation{}, ErrCapacity
}

// ClaimStart requires the matching installation receipt, then durably records a
// single start attempt. Only the successful caller may dispatch it. A lost response is not
// permission to retry: reconcile/retire instead. The service manager must also
// fence delayed starts against retirement; this ledger cannot fence OS calls.
func (p *Pool) ClaimStart(ctx context.Context, operation string) (Reservation, error) {
	if !id(operation) {
		return Reservation{}, ErrAssignment
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return Reservation{}, err
	}
	defer tx.Rollback()
	r, err := readReservation(tx.QueryRowContext(ctx, "SELECT assignment,state,retirement,installation_attempted,installed FROM reservations WHERE operation=?", operation))
	if err != nil {
		return r, err
	}
	if r.State != "reserved" || !r.InstallationAttempted || r.Installed == nil || r.Installed.Assignment != r.Assignment {
		return r, ErrConflict
	}
	if _, err = tx.ExecContext(ctx, "UPDATE reservations SET state='starting' WHERE operation=?", operation); err != nil {
		return r, err
	}
	r.State = "starting"
	return r, tx.Commit()
}

// BeginRetirement closes new start claims. It does not prove the runtime stopped,
// and intentionally keeps the UID and port occupied until trusted reconciliation.
func (p *Pool) BeginRetirement(ctx context.Context, operation string) (Reservation, error) {
	if !id(operation) {
		return Reservation{}, ErrAssignment
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return Reservation{}, err
	}
	defer tx.Rollback()
	r, err := readReservation(tx.QueryRowContext(ctx, "SELECT assignment,state,retirement,installation_attempted,installed FROM reservations WHERE operation=?", operation))
	if err != nil {
		return r, err
	}
	if r.State != "retired" {
		if _, err = tx.ExecContext(ctx, "UPDATE reservations SET state='retiring' WHERE operation=?", operation); err != nil {
			return r, err
		}
		r.State = "retiring"
	}
	return r, tx.Commit()
}

type RetirementObservation struct {
	Assignment                                                 Assignment
	ObservedAt                                                 time.Time
	StartsFenced, ProcessesGone, ListenerGone, RoutingDetached bool
}

// RetirementReader is a trusted host/runtime adapter, never customer-supplied
// flags. StartsFenced means a durable service-manager tombstone prevents all late
// start/install requests, including an already claimed but not dispatched start.
// RoutingDetached includes pending activation/probe/draining references.
// ProcessesGone covers the UID and entire cgroup, not just the service's main PID.
type RetirementReader interface {
	ObserveNodeRetirement(context.Context, Assignment) (RetirementObservation, error)
}

func (p *Pool) ReconcileRetirement(ctx context.Context, operation string, reader RetirementReader) (Reservation, error) {
	r, err := p.Lookup(ctx, operation)
	if err != nil {
		return r, err
	}
	if r.State == "retired" {
		return r, nil
	}
	if r.State != "retiring" || reader == nil {
		return r, ErrRetirement
	}
	started := time.Now()
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	observation, err := reader.ObserveNodeRetirement(probeCtx, r.Assignment)
	if err != nil {
		return r, err
	}
	if err = probeCtx.Err(); err != nil {
		return r, err
	}
	now := time.Now()
	if observation.Assignment != r.Assignment || observation.ObservedAt.Before(started) || observation.ObservedAt.After(now) || !observation.StartsFenced || !observation.ProcessesGone || !observation.ListenerGone || !observation.RoutingDetached {
		return r, ErrRetirement
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return r, err
	}
	defer tx.Rollback()
	current, err := readReservation(tx.QueryRowContext(ctx, "SELECT assignment,state,retirement,installation_attempted,installed FROM reservations WHERE operation=?", operation))
	if err != nil {
		return r, err
	}
	if current.Assignment != r.Assignment || (current.State != "retiring" && current.State != "retired") {
		return r, ErrConflict
	}
	if current.State == "retired" {
		return current, tx.Commit()
	}
	raw, err := json.Marshal(observation)
	if err != nil {
		return r, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE reservations SET state='retired',retirement=? WHERE operation=?", raw, operation); err != nil {
		return r, err
	}
	r.State = "retired"
	r.Retirement = &observation
	return r, tx.Commit()
}

// Outstanding returns all occupied slots for startup reconciliation. Reading this
// list never authorizes another start attempt.
func (p *Pool) Outstanding(ctx context.Context) ([]Reservation, error) {
	rows, err := p.db.QueryContext(ctx, "SELECT assignment,state,retirement,installation_attempted,installed FROM reservations WHERE state!='retired' ORDER BY uid")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Reservation{}
	for rows.Next() {
		r, err := readReservation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
