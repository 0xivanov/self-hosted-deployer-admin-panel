//go:build integration

package nodelaunch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func poolConfig() PoolConfig {
	a := assignment()
	return PoolConfig{ProjectID: a.ProjectID, RuntimeID: a.RuntimeID, ToolchainSHA256: a.ToolchainSHA256, Architecture: a.Architecture, Slots: []Slot{{60000, 31001}, {60001, 31002}}}
}
func poolAssignment(n int) Assignment {
	a := assignment()
	a.UID = 0
	a.Port = 0
	a.OperationID = fmt.Sprintf("%064x", n)
	return a
}
func openPool(t *testing.T, path string, config PoolConfig) *Pool {
	t.Helper()
	p, err := OpenPool(path, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

type retirementReader func(context.Context, Assignment) (RetirementObservation, error)

func (f retirementReader) ObserveNodeRetirement(ctx context.Context, a Assignment) (RetirementObservation, error) {
	return f(ctx, a)
}
func retirement(a Assignment) RetirementObservation {
	return RetirementObservation{Assignment: a, ObservedAt: time.Now(), StartsFenced: true, ProcessesGone: true, ListenerGone: true, RoutingDetached: true}
}
func TestReservationRestartRetirementAndSlotReuse(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "private", "pool.db")
	config := poolConfig()
	p := openPool(t, path, config)
	first, err := p.Reserve(ctx, poolAssignment(1))
	if err != nil {
		t.Fatal(err)
	}
	if first.State != "reserved" || first.Assignment.UID != 60000 || first.Assignment.Port != 31001 {
		t.Fatal(first)
	}
	if _, err = Render(first.Assignment); err != nil {
		t.Fatal(err)
	}
	if _, err = p.ClaimStart(ctx, first.Assignment.OperationID); err != nil {
		t.Fatal(err)
	}
	if err = p.Close(); err != nil {
		t.Fatal(err)
	}
	p = openPool(t, path, config)
	retry, err := p.Reserve(ctx, poolAssignment(1))
	if err != nil || retry.State != "starting" || retry.Assignment != first.Assignment {
		t.Fatal(retry, err)
	}
	if _, err = p.ClaimStart(ctx, first.Assignment.OperationID); !errors.Is(err, ErrConflict) {
		t.Fatal("restart authorized duplicate start", err)
	}
	other := poolAssignment(1)
	other.ArtifactSHA256 = fmt.Sprintf("%064x", 42)
	if _, err = p.Reserve(ctx, other); !errors.Is(err, ErrConflict) {
		t.Fatal("operation identity changed", err)
	}
	second, err := p.Reserve(ctx, poolAssignment(2))
	if err != nil {
		t.Fatal(err)
	}
	if second.Assignment.UID == first.Assignment.UID || second.Assignment.Port == first.Assignment.Port {
		t.Fatal("overlapping slot")
	}
	if _, err = p.Reserve(ctx, poolAssignment(3)); !errors.Is(err, ErrCapacity) {
		t.Fatal(err)
	}
	if _, err = p.BeginRetirement(ctx, first.Assignment.OperationID); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Reserve(ctx, poolAssignment(3)); !errors.Is(err, ErrCapacity) {
		t.Fatal("unproven retirement freed slot", err)
	}
	if _, err = p.ClaimStart(ctx, first.Assignment.OperationID); !errors.Is(err, ErrConflict) {
		t.Fatal("retiring operation started", err)
	}
	calls := 0
	reader := retirementReader(func(_ context.Context, a Assignment) (RetirementObservation, error) {
		calls++
		return retirement(a), nil
	})
	done, err := p.ReconcileRetirement(ctx, first.Assignment.OperationID, reader)
	if err != nil || done.State != "retired" {
		t.Fatal(done, err)
	}
	third, err := p.Reserve(ctx, poolAssignment(3))
	if err != nil || third.Assignment.UID != first.Assignment.UID || third.Assignment.Port != first.Assignment.Port {
		t.Fatal(third, err)
	}
	if err = p.Close(); err != nil {
		t.Fatal(err)
	}
	p = openPool(t, path, config)
	old, err := p.Reserve(ctx, poolAssignment(1))
	if err != nil || old.State != "retired" {
		t.Fatal("tombstone lost", old, err)
	}
	if old.Retirement == nil || old.Retirement.Assignment != first.Assignment || !old.Retirement.StartsFenced {
		t.Fatal("retirement evidence lost", old)
	}
	if _, err = p.ClaimStart(ctx, first.Assignment.OperationID); !errors.Is(err, ErrConflict) {
		t.Fatal("tombstone resurrected", err)
	}
	if _, err = p.ReconcileRetirement(ctx, first.Assignment.OperationID, reader); err != nil || calls != 1 {
		t.Fatal("terminal retirement reprobed", calls, err)
	}
	current, err := p.Lookup(ctx, third.Assignment.OperationID)
	if err != nil || current.State != "reserved" {
		t.Fatal("late old retirement affected reused slot", current, err)
	}
	if _, err = p.Reserve(ctx, poolAssignment(4)); !errors.Is(err, ErrCapacity) {
		t.Fatal("old retirement released new reservation", err)
	}
}
func TestRetirementRejectsMissingStaleOrWrongEvidence(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		change func(*RetirementObservation)
	}{
		{"unfenced_starts", func(o *RetirementObservation) { o.StartsFenced = false }},
		{"processes_survive", func(o *RetirementObservation) { o.ProcessesGone = false }},
		{"listener_survives", func(o *RetirementObservation) { o.ListenerGone = false }},
		{"routing_reference", func(o *RetirementObservation) { o.RoutingDetached = false }},
		{"cached", func(o *RetirementObservation) { o.ObservedAt = time.Now().Add(-time.Second) }},
		{"future", func(o *RetirementObservation) { o.ObservedAt = time.Now().Add(time.Minute) }},
		{"other_operation", func(o *RetirementObservation) { o.Assignment.OperationID = fmt.Sprintf("%064x", 9) }},
		{"other_uid", func(o *RetirementObservation) { o.Assignment.UID++ }},
		{"other_port", func(o *RetirementObservation) { o.Assignment.Port++ }},
		{"other_artifact", func(o *RetirementObservation) { o.Assignment.ArtifactSHA256 = fmt.Sprintf("%064x", 8) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			p := openPool(t, filepath.Join(t.TempDir(), "private", "pool.db"), poolConfig())
			r, err := p.Reserve(ctx, poolAssignment(1))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = p.BeginRetirement(ctx, r.Assignment.OperationID); err != nil {
				t.Fatal(err)
			}
			reader := retirementReader(func(_ context.Context, a Assignment) (RetirementObservation, error) {
				o := retirement(a)
				tc.change(&o)
				return o, nil
			})
			if _, err = p.ReconcileRetirement(ctx, r.Assignment.OperationID, reader); !errors.Is(err, ErrRetirement) {
				t.Fatal("unsafe evidence accepted", err)
			}
			current, err := p.Lookup(ctx, r.Assignment.OperationID)
			if err != nil || current.State != "retiring" {
				t.Fatal(current, err)
			}
		})
	}
}
func TestPoolConcurrentHandlesCannotDoubleAllocateOrStart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "private", "pool.db")
	a := openPool(t, path, poolConfig())
	b := openPool(t, path, poolConfig())
	pools := []*Pool{a, b}
	type result struct {
		reservation Reservation
		err         error
	}
	results := make(chan result, 12)
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Go(func() { r, e := pools[i%2].Reserve(ctx, poolAssignment(i+1)); results <- result{r, e} })
	}
	wg.Wait()
	close(results)
	slots := map[Slot]bool{}
	var operation string
	success := 0
	for result := range results {
		if errors.Is(result.err, ErrCapacity) {
			continue
		}
		if result.err != nil {
			t.Fatal(result.err)
		}
		slot := Slot{result.reservation.Assignment.UID, result.reservation.Assignment.Port}
		if slots[slot] {
			t.Fatal("slot allocated twice")
		}
		slots[slot] = true
		success++
		operation = result.reservation.Assignment.OperationID
	}
	if success != 2 {
		t.Fatal(success)
	}
	starts := make(chan error, 12)
	for i := range 12 {
		wg.Go(func() { _, err := pools[i%2].ClaimStart(ctx, operation); starts <- err })
	}
	wg.Wait()
	close(starts)
	success = 0
	for err := range starts {
		if err == nil {
			success++
		} else if !errors.Is(err, ErrConflict) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatal("multiple start authorizations", success)
	}
}
func TestPoolConfigurationIdentityAndPaths(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "private", "pool.db")
	config := poolConfig()
	p := openPool(t, path, config)
	changed := poolConfig()
	changed.Slots[0].Port += 100
	if q, err := OpenPool(path, changed); !errors.Is(err, ErrConflict) {
		if q != nil {
			q.Close()
		}
		t.Fatal(err)
	}
	a := poolAssignment(1)
	a.RuntimeID = fmt.Sprintf("%064x", 99)
	if _, err := p.Reserve(context.Background(), a); !errors.Is(err, ErrAssignment) {
		t.Fatal("foreign runtime allowed", err)
	}
	if _, err := p.Reserve(context.Background(), assignment()); !errors.Is(err, ErrAssignment) {
		t.Fatal("caller chose slot", err)
	}
	config.Slots[1].UID = config.Slots[0].UID
	if q, err := OpenPool(filepath.Join(t.TempDir(), "other.db"), config); !errors.Is(err, ErrAssignment) {
		if q != nil {
			q.Close()
		}
		t.Fatal("duplicate UID allowed", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, err)
	}
	link := filepath.Join(filepath.Dir(path), "link.db")
	if err = os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if q, err := OpenPool(link, poolConfig()); !errors.Is(err, ErrAssignment) {
		if q != nil {
			q.Close()
		}
		t.Fatal("symlink DB allowed", err)
	}
}
func TestReservedRetirementAndProviderFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p := openPool(t, filepath.Join(t.TempDir(), "private", "pool.db"), poolConfig())
	r, err := p.Reserve(ctx, poolAssignment(1))
	if err != nil {
		t.Fatal(err)
	}
	reader := retirementReader(func(_ context.Context, a Assignment) (RetirementObservation, error) { return retirement(a), nil })
	if _, err = p.ReconcileRetirement(ctx, r.Assignment.OperationID, reader); !errors.Is(err, ErrRetirement) {
		t.Fatal("retired without closing claims", err)
	}
	if _, err = p.BeginRetirement(ctx, r.Assignment.OperationID); err != nil {
		t.Fatal(err)
	}
	providerErr := errors.New("unavailable")
	failed := retirementReader(func(context.Context, Assignment) (RetirementObservation, error) {
		return RetirementObservation{}, providerErr
	})
	if _, err = p.ReconcileRetirement(ctx, r.Assignment.OperationID, failed); !errors.Is(err, providerErr) {
		t.Fatal(err)
	}
	if _, err = p.ReconcileRetirement(ctx, r.Assignment.OperationID, nil); !errors.Is(err, ErrRetirement) {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancelReader := retirementReader(func(_ context.Context, a Assignment) (RetirementObservation, error) {
		cancel()
		return retirement(a), nil
	})
	if _, err = p.ReconcileRetirement(cancelled, r.Assignment.OperationID, cancelReader); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err = p.ClaimStart(ctx, r.Assignment.OperationID); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if final, err := p.ReconcileRetirement(ctx, r.Assignment.OperationID, reader); err != nil || final.State != "retired" {
		t.Fatal(final, err)
	}
}

func TestPoolSurvivesProcessExitWithoutClose(t *testing.T) {
	if path := os.Getenv("NODE_POOL_EXIT_FIXTURE"); path != "" {
		p, err := OpenPool(path, poolConfig())
		if err != nil {
			t.Fatal(err)
		}
		r, err := p.Reserve(context.Background(), poolAssignment(1))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = p.ClaimStart(context.Background(), r.Assignment.OperationID); err != nil {
			t.Fatal(err)
		}
		if _, err = p.Reserve(context.Background(), poolAssignment(2)); err != nil {
			t.Fatal(err)
		}
		os.Exit(0) // Deliberately bypass Close and test cleanup in this child process.
	}
	t.Parallel()
	path := filepath.Join(t.TempDir(), "private", "pool.db")
	child := exec.Command(os.Args[0], "-test.run=^TestPoolSurvivesProcessExitWithoutClose$")
	child.Env = append(os.Environ(), "NODE_POOL_EXIT_FIXTURE="+path)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("child: %v %s", err, output)
	}
	p := openPool(t, path, poolConfig())
	ctx := context.Background()
	outstanding, err := p.Outstanding(ctx)
	if err != nil || len(outstanding) != 2 || outstanding[0].State != "starting" || outstanding[1].State != "reserved" {
		t.Fatal(outstanding, err)
	}
	if _, err = p.ClaimStart(ctx, poolAssignment(1).OperationID); !errors.Is(err, ErrConflict) {
		t.Fatal("start repeated after process exit", err)
	}
	if _, err = p.Reserve(ctx, poolAssignment(3)); !errors.Is(err, ErrCapacity) {
		t.Fatal("lost slot after process exit", err)
	}
}
