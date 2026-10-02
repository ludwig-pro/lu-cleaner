package scanctl

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func limits(t *testing.T, mode, walkers string) Limits {
	t.Helper()
	l, _, err := Resolve(mode, walkers)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestEcoDoesNotDelayAlreadyBoundedIO(t *testing.T) {
	c := New(limits(t, "eco", ""))
	ctx := With(context.Background(), c)
	defer c.Close()
	now := time.Unix(1, 0)
	c.now = func() time.Time { return now }
	c.sleep = func(context.Context, time.Duration) error {
		t.Error("eco added an artificial pause after bounded I/O")
		return nil
	}
	for range 4 {
		if err := DoIO(ctx, func() error {
			now = now.Add(time.Second) // A slow syscall is not sustained CPU work.
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if c.Limits().IO != 2 || c.Limits().Commands != 1 || c.Limits().Prefetch != 1 {
		t.Fatal("eco resource limits must stay in place")
	}
}

func TestResolve(t *testing.T) {
	for _, tc := range []struct {
		mode, walkers     string
		io, cmd, prefetch int
		pause             time.Duration
	}{{"", "", 2, 1, 1, 0}, {"fast", "", 8, 4, 4, 0}, {"eco", "1", 1, 1, 1, 0}} {
		l, warning, err := Resolve(tc.mode, tc.walkers)
		if err != nil || warning != "" || l.IO != tc.io || l.Commands != tc.cmd || l.Prefetch != tc.prefetch || l.Pause != tc.pause || l.BatchSize != 256 {
			t.Fatalf("%+v: %+v %q %v", tc, l, warning, err)
		}
	}
	if _, _, err := Resolve("turbo", ""); err == nil {
		t.Fatal("unknown mode accepted")
	}
	for _, v := range []string{"-1", "0", "oops"} {
		l, w, e := Resolve("eco", v)
		if e != nil || l.IO != 2 || w == "" {
			t.Fatalf("invalid override %q: %+v %q %v", v, l, w, e)
		}
	}
}

func TestSharedAdmissionAndCanceledWait(t *testing.T) {
	c := New(limits(t, "fast", "1"))
	ctx := With(context.Background(), c)
	defer c.Close()
	release, err := c.AcquireIO(ctx)
	if err != nil {
		t.Fatal(err)
	}
	waiting, cancel := context.WithCancel(ctx)
	ready, done := make(chan struct{}), make(chan error, 1)
	go func() {
		close(ready)
		r, err := c.AcquireIO(waiting)
		if r != nil {
			r()
		}
		done <- err
	}()
	<-ready
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("quota wait did not cancel")
	}
	release()
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := DoIO(ctx, func() error { return nil }); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if s := c.Snapshot(); s.IOMax != 1 || s.IOActive != 0 {
		t.Fatalf("actual admission stats: %+v", s)
	}
}

func TestCooldownRetainsSlotAndCancels(t *testing.T) {
	l := limits(t, "eco", "1")
	l.Pause = 5 * time.Millisecond // Optional controller pacing; public profiles do not sleep.
	c := New(l)
	ctx, cancel := context.WithCancel(context.Background())
	ctx = With(ctx, c)
	defer c.Close()
	entered, wake := make(chan struct{}), make(chan struct{})
	c.sleep = func(ctx context.Context, d time.Duration) error {
		if d != 5*time.Millisecond {
			t.Errorf("pause %s", d)
		}
		close(entered)
		select {
		case <-wake:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	release, _ := c.AcquireIO(ctx)
	released := make(chan struct{})
	go func() { release(); close(released) }()
	<-entered
	if s := c.Snapshot(); s.IOActive != 0 || s.IOCooling != 1 {
		t.Fatalf("work vs cooldown %+v", s)
	}
	waitCtx, stop := context.WithCancel(ctx)
	result := make(chan error, 1)
	go func() {
		r, e := c.AcquireIO(waitCtx)
		if r != nil {
			r()
		}
		result <- e
	}()
	// Cancellation proves the permit cannot be reused by another caller while
	// the injectable clock is still holding its cooldown.
	stop()
	if e := <-result; !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	cancel()
	select {
	case <-released:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("cooldown did not cancel")
	}
	if s := c.Snapshot(); s.IOCooling != 0 {
		t.Fatalf("leaked cooldown %+v", s)
	}
}

func TestPanicReleasesAndCanceledContextNeverStarts(t *testing.T) {
	c := New(limits(t, "fast", "1"))
	ctx := With(context.Background(), c)
	defer c.Close()
	func() {
		defer func() {
			if recover() == nil {
				t.Error("missing panic")
			}
		}()
		_ = DoIO(ctx, func() error { panic("fixture") })
	}()
	if err := DoIO(ctx, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := DoIO(canceled, func() error { t.Fatal("started after cancellation"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestCommandTimeoutStartsAfterAdmission(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := New(limits(t, "eco", ""))
		ctx := With(context.Background(), c)
		defer c.Close()
		running, finish := make(chan struct{}), make(chan struct{})
		first := make(chan error, 1)
		go func() {
			_, err := Command(ctx, 0, func(context.Context) ([]byte, error) { close(running); <-finish; return nil, nil })
			first <- err
		}()
		<-running
		// The queued command's timeout is deliberately shorter than its wait. The
		// wait is controlled by a barrier and virtual time crosses its deadline.
		queued := make(chan error, 1)
		go func() {
			_, err := Command(ctx, time.Millisecond, func(run context.Context) ([]byte, error) {
				if err := run.Err(); err != nil {
					return nil, err
				}
				return []byte("ok"), nil
			})
			queued <- err
		}()
		synctest.Wait() // Ensure the second command reached the occupied slot.
		timer := time.NewTimer(10 * time.Millisecond)
		<-timer.C
		close(finish)
		if err := <-first; err != nil {
			t.Fatal(err)
		}
		if err := <-queued; err != nil {
			t.Fatalf("queue consumed execution timeout: %v", err)
		}
		if s := c.Snapshot(); s.CommandMax != 1 || s.CommandActive != 0 {
			t.Fatalf("commands %+v", s)
		}
	})
}
