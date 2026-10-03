package fsx

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ludwig-pro/lu-cleaner/internal/diagnostics"
)

func TestRecursivePanicIsIncompleteBulkAndFallback(t *testing.T) {
	for _, bulk := range []bool{false, true} {
		t.Run(map[bool]string{false: "fallback", true: "bulk"}[bulk], func(t *testing.T) {
			previous := useBulk
			useBulk = bulk
			defer func() { useBulk = previous }()
			root := t.TempDir()
			write(t, filepath.Join(root, "child", "boom", "payload"), 4096)
			ctx, group := diagnostics.NewGroup(context.Background(), "ai")
			defer group.Close()
			st, err := Size(ctx, root, &Options{Skip: func(path, name string) bool {
				if name == "boom" {
					panic("private contents")
				}
				return false
			}})
			var fault *diagnostics.Fault
			if !errors.As(err, &fault) || st.Errors == 0 || group.Err() == nil {
				t.Fatalf("panic became complete stats: %+v %v", st, err)
			}
		})
	}
}

// Signals the exact point where get evaluates the consumer's cancellation
// channel, after selecting the shared entry and releasing the cache mutex.
type waitingContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *waitingContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func TestSharedMeasurementPanicWakesAllConsumersAndCanRetry(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var loads atomic.Int64
	c := &sizeCache{ctx: context.Background(), m: map[string]*cacheEntry{}}
	c.load = func(context.Context, string) (Stats, error) {
		loads.Add(1)
		close(started)
		<-release
		panic("private error")
	}
	results := make(chan error, 2)
	first := &waitingContext{Context: context.Background(), waiting: make(chan struct{})}
	second := &waitingContext{Context: context.Background(), waiting: make(chan struct{})}
	go func() { _, err := c.get(first, "shared"); results <- err }()
	<-started
	<-first.waiting
	go func() { _, err := c.get(second, "shared"); results <- err }()
	<-second.waiting
	close(release)
	for range 2 {
		var fault *diagnostics.Fault
		if err := <-results; !errors.As(err, &fault) {
			t.Fatalf("consumer did not see panic: %v", err)
		}
	}
	c.wg.Wait()
	if loads.Load() != 1 {
		t.Fatal("measurement not shared")
	}
	c.load = func(context.Context, string) (Stats, error) { loads.Add(1); return Stats{Bytes: 42}, nil }
	st, err := c.get(context.Background(), "shared")
	c.wg.Wait()
	if err != nil || st.Bytes != 42 || loads.Load() != 2 {
		t.Fatalf("failed result reused: %+v %v", st, err)
	}
}

func TestCacheValidationPanicReleasesWaitersWithoutTrustingEntries(t *testing.T) {
	previous := listWriters
	defer func() { listWriters = previous }()
	listWriters = func(context.Context) ([]string, bool, error) { panic("inspection interrupted") }
	s := &SizeStore{ready: make(chan struct{}), valid: map[*storedSize]bool{}}
	s.validate(context.Background())
	select {
	case <-s.ready:
	default:
		t.Fatal("cache waiters left blocked")
	}
	if s.state != replayFailed || len(s.valid) != 0 {
		t.Fatal("failed cache validation trusted entries")
	}
}
