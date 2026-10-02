package fsx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/scanctl"
)

func controlled(t *testing.T, ctx context.Context, n string) (context.Context, *scanctl.Controller) {
	t.Helper()
	l, _, err := scanctl.Resolve("fast", n)
	if err != nil {
		t.Fatal(err)
	}
	c := scanctl.New(l)
	return scanctl.With(ctx, c), c
}

func TestTwelveRootsShareActualIOAdmission(t *testing.T) {
	base := t.TempDir()
	for i := 0; i < 12; i++ {
		write(t, filepath.Join(base, fmt.Sprint(i), "nested", "file"), 4096)
	}
	ctx, c := controlled(t, context.Background(), "1")
	defer c.Close()
	ctx = WithCache(ctx)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			st, err := Size(ctx, filepath.Join(base, fmt.Sprint(i)), nil)
			if err != nil || st.Files != 1 || st.Dirs != 2 {
				t.Errorf("root %d: %+v %v", i, st, err)
			}
		}(i)
	}
	wg.Wait()
	WaitCache(ctx)
	if s := c.Snapshot(); s.IOMax != 1 || s.IOActive != 0 || s.Files != 12 {
		t.Fatalf("real I/O admissions %+v", s)
	}
}

func TestCancelStopsCurrentDirectoryBulkAndFallback(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 2048; i++ {
		if err := os.Mkdir(filepath.Join(root, fmt.Sprintf("%04d", i)), 0700); err != nil {
			t.Fatal(err)
		}
	}
	original := useBulk
	defer func() { useBulk = original }()
	for _, bulk := range []bool{true, false} {
		t.Run(fmt.Sprint(bulk), func(t *testing.T) {
			useBulk = bulk
			ctx, cancel := context.WithCancel(context.Background())
			ctx, c := controlled(t, ctx, "1")
			defer c.Close()
			seen := 0
			start := time.Now()
			_, err := Size(ctx, root, &Options{Skip: func(path, name string) bool { seen++; cancel(); return true }})
			if !errors.Is(err, context.Canceled) || seen != 1 {
				t.Fatalf("processed %d/2048 after cancel (%v)", seen, err)
			}
			if d := time.Since(start); d > time.Second {
				t.Fatalf("join took %s", d)
			}
		})
	}
}

func TestBudgetOneReentrantCallbackAndCache(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "child", "file"), 4096)
	ctx, c := controlled(t, context.Background(), "1")
	defer c.Close()
	ctx = WithCache(ctx)
	done := make(chan error, 1)
	go func() {
		_, err := Size(ctx, root, &Options{Skip: func(path, name string) bool {
			_, e := Size(ctx, path, nil)
			if e != nil {
				t.Error(e)
			}
			return false
		}})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("permit held across reentrant callback")
	}
	WaitCache(ctx)
}

func TestConsumerCancellationPreservesSharedMeasurement(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "file"), 4096)
	owner, c := controlled(t, context.Background(), "1")
	defer c.Close()
	ctx := WithCache(owner)
	release, _ := c.AcquireIO(owner)
	consumer, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { _, err := Size(consumer, root, nil); done <- err }()
	cache := ctx.Value(cacheKey{}).(*sizeCache)
	// Wait for the shared entry to exist while the actual I/O is held behind
	// the admission barrier. No sleeps determine the ordering.
	for {
		cache.mu.Lock()
		started := cache.m[root] != nil
		cache.mu.Unlock()
		if started {
			break
		}
		select {
		case <-consumer.Done():
			t.Fatal("consumer canceled early")
		default:
		}
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	release()
	st, err := Size(ctx, root, nil)
	if err != nil || st.Files != 1 {
		t.Fatalf("shared walk canceled: %+v %v", st, err)
	}
	WaitCache(ctx)
	if s := c.Snapshot(); s.CacheMisses != 1 || s.CacheHits != 1 {
		t.Fatalf("dedup stats %+v", s)
	}
}
