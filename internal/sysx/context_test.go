package sysx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/scanctl"
)

type waitingContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *waitingContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func awaitProbe[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case got := <-ch:
		return got
	case <-time.After(2 * time.Second):
		t.Fatal("process probe did not finish")
		var zero T
		return zero
	}
}

func TestProcessRefreshWaitCanCancel(t *testing.T) {
	old := listProcesses
	started, release := make(chan struct{}), make(chan struct{})
	var releases sync.Once
	var calls atomic.Int32
	listProcesses = func(context.Context) (processList, error) {
		calls.Add(1)
		close(started)
		<-release
		return processList{paths: []string{"/Applications/Claude"}, base: map[string]bool{"Claude": true}}, nil
	}
	InvalidateProcesses()
	t.Cleanup(func() {
		releases.Do(func() { close(release) })
		listProcesses = old
		InvalidateProcesses()
	})
	leader := make(chan error, 1)
	go func() { _, err := RunningContext(context.Background(), "Claude"); leader <- err }()
	awaitProbe(t, started)
	ctx, cancel := context.WithCancel(scanctl.Ensure(context.Background()))
	defer cancel()
	waiterCtx := &waitingContext{Context: ctx, waiting: make(chan struct{})}
	waiter := make(chan error, 1)
	go func() { _, err := RunningContext(waiterCtx, "Claude"); waiter <- err }()
	awaitProbe(t, waiterCtx.waiting)
	cancel()
	if err := awaitProbe(t, waiter); !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting caller: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("%d refreshes; callers must share the in-flight inspection", calls.Load())
	}
	releases.Do(func() { close(release) })
	if err := awaitProbe(t, leader); err != nil {
		t.Fatal(err)
	}
	if hit, err := RunningContext(context.Background(), "Claude"); err != nil || len(hit) != 1 || calls.Load() != 1 {
		t.Fatalf("complete refresh not cached: hit=%v calls=%d err=%v", hit, calls.Load(), err)
	}
}

func TestLiveProbeWaiterRetriesCancelledOwner(t *testing.T) {
	var c probeCache[int]
	var calls atomic.Int32
	started := make(chan struct{})
	load := func(ctx context.Context) (int, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-ctx.Done()
			return 12, ctx.Err() // an incomplete result
		}
		return 42, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	leader := make(chan error, 1)
	go func() { _, err := c.get(ctx, time.Minute, load); leader <- err }()
	awaitProbe(t, started)
	waiterCtx := &waitingContext{Context: context.Background(), waiting: make(chan struct{})}
	waiter := make(chan int, 1)
	go func() { value, _ := c.get(waiterCtx, time.Minute, load); waiter <- value }()
	awaitProbe(t, waiterCtx.waiting)
	cancel()
	if err := awaitProbe(t, leader); !errors.Is(err, context.Canceled) {
		t.Fatalf("owner: %v", err)
	}
	if got := awaitProbe(t, waiter); got != 42 || calls.Load() != 2 {
		t.Fatalf("live waiter got %d after %d refreshes", got, calls.Load())
	}
}

func TestCancelledProcessRefreshDoesNotPublish(t *testing.T) {
	old := listProcesses
	InvalidateProcesses()
	t.Cleanup(func() { listProcesses = old; InvalidateProcesses() })
	ctx, cancel := context.WithCancel(context.Background())
	listProcesses = func(context.Context) (processList, error) {
		cancel()
		return processList{paths: []string{"/partial"}, base: map[string]bool{"partial": true}}, nil
	}
	if hit, err := RunningContext(ctx, "partial"); len(hit) != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled refresh: hit=%v err=%v", hit, err)
	}
	listProcesses = func(context.Context) (processList, error) {
		return processList{paths: []string{"/complete"}, base: map[string]bool{"complete": true}}, nil
	}
	if hit, err := RunningContext(context.Background(), "partial", "complete"); err != nil || len(hit) != 1 || hit[0] != "complete" {
		t.Fatalf("cancelled result was cached: hit=%v err=%v", hit, err)
	}
}

func TestInvalidationDuringProbeRefresh(t *testing.T) {
	var c probeCache[int]
	started, release := make(chan struct{}), make(chan struct{})
	var releases sync.Once
	defer releases.Do(func() { close(release) })
	var calls atomic.Int32
	load := func(context.Context) (int, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
			return 12, nil
		}
		return 42, nil
	}
	done := make(chan int, 1)
	go func() { value, _ := c.get(context.Background(), time.Minute, load); done <- value }()
	awaitProbe(t, started)
	c.invalidate() // must not wait for the slow inspection
	releases.Do(func() { close(release) })
	if got := awaitProbe(t, done); got != 42 || calls.Load() != 2 {
		t.Fatalf("invalidated refresh survived: value=%d calls=%d", got, calls.Load())
	}
}

func TestCancelledPathInspectionIsUnknownAndNotCached(t *testing.T) {
	old := inspectExecs
	invalidateFDs()
	t.Cleanup(func() { inspectExecs = old; invalidateFDs() })
	ctx, cancel := context.WithCancel(context.Background())
	inspectExecs = func(context.Context) ([]procPath, bool, error) {
		cancel()
		return []procPath{{pid: "1", path: "/partial/bin"}}, true, nil
	}
	if pids, err := ExecInsideContext(ctx, "/partial"); pids != "" || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled inspection: pids=%q err=%v", pids, err)
	}
	inspectExecs = func(context.Context) ([]procPath, bool, error) {
		return []procPath{{pid: "2", path: "/complete/bin"}}, true, nil
	}
	if pids, err := ExecInsideContext(context.Background(), "/complete"); err != nil || pids != "2" {
		t.Fatalf("fresh inspection: pids=%q err=%v", pids, err)
	}
}

func TestCommandTimeoutStartsAfterAdmission(t *testing.T) {
	// Use virtual time for admission: process startup must not race a 100 ms
	// wall-clock budget when the machine is busy. The real command still runs.
	synctest.Test(t, func(t *testing.T) {
		c := scanctl.New(scanctl.Limits{IO: 1, Commands: 1, BatchSize: 256})
		defer c.Close()
		ctx, cancel := context.WithTimeout(scanctl.With(context.Background(), c), 3*time.Second)
		defer cancel()
		started, release := make(chan struct{}), make(chan struct{})
		var releases sync.Once
		defer releases.Do(func() { close(release) })
		leader := make(chan error, 1)
		go func() {
			_, err := scanctl.Command(ctx, 0, func(context.Context) ([]byte, error) { close(started); <-release; return nil, nil })
			leader <- err
		}()
		awaitProbe(t, started)
		type result struct {
			out []byte
			err error
		}
		done := make(chan result, 1)
		go func() { out, err := output(ctx, 100*time.Millisecond, "/bin/echo", "ready"); done <- result{out, err} }()
		synctest.Wait() // The command is waiting for the occupied slot.
		select {
		case got := <-done:
			t.Fatalf("command ran or timed out before admission: %q, %v", got.out, got.err)
		case <-time.After(200 * time.Millisecond):
		}
		releases.Do(func() { close(release) })
		if err := awaitProbe(t, leader); err != nil {
			t.Fatal(err)
		}
		if got := awaitProbe(t, done); got.err != nil || string(got.out) != "ready\n" {
			t.Fatalf("command did not get its execution timeout: %q, %v", got.out, got.err)
		}
	})
}

func TestCommandCancellationKillsProcessGroup(t *testing.T) {
	dir := t.TempDir()
	marker, ready := filepath.Join(dir, "survived"), filepath.Join(dir, "ready")
	ctx, cancel := context.WithCancel(scanctl.Ensure(context.Background()))
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := output(ctx, 2*time.Second, "/bin/sh", "-c", `(sleep 0.4; printf survived > "$1") & printf ready > "$2"; wait`, "probe", marker, ready)
		done <- err
	}()
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("helper did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := awaitProbe(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled helper: %v", err)
	}
	time.Sleep(500 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("helper child survived cancellation: %v", err)
	}
}
