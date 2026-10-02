package artifacts

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
	"github.com/ludwig-pro/lu-cleaner/internal/scanctl"
)

func TestSharedWalkerVisitsEveryRootWithBoundedPool(t *testing.T) {
	f := newFixture(t)
	var roots []string
	for r := range 8 {
		root := fmt.Sprintf("root-%d", r)
		roots = append(roots, root)
		for d := range 12 {
			f.file(fmt.Sprintf("%s/branch-%d/source.go", root, d), "package x")
		}
	}
	f.setup(roots, nil)
	limits, _, _ := scanctl.Resolve("fast", "2")
	controller := scanctl.New(limits)
	ctx := scanctl.With(context.Background(), controller)
	s := f.prov.newScan(ctx, f.env, nil)
	s.setupRoots()
	walker := s.walker
	walker.runRoots(s.roots)
	if len(s.visited) != 8*13 || cap(walker.sem) != 2 || len(walker.sem) != 0 {
		t.Fatalf("visited=%d pool=%d active=%d", len(s.visited), cap(walker.sem), len(walker.sem))
	}
	// The same pool is reused by concurrent follow-up walks. Their task
	// counters remain independent, including when one finishes immediately.
	var wg sync.WaitGroup
	for _, r := range s.roots {
		wg.Add(1)
		go func(r *scanRoot) {
			defer wg.Done()
			walker.run(r.path, walkCtx{root: r})
		}(r)
	}
	wg.Wait()
	if snapshot := controller.Snapshot(); snapshot.IOMax > 2 || snapshot.IOActive != 0 {
		t.Fatalf("unbounded or leaked I/O admission: %+v", snapshot)
	}
}

func TestCheckoutProbeWaitsForIOAndStopsOnCancellation(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "deploy")
	if err := os.MkdirAll(filepath.Join(child, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	limits, _, _ := scanctl.Resolve("fast", "1")
	controller := scanctl.New(limits)
	owner := scanctl.With(t.Context(), controller)
	release, err := controller.AcquireIO(owner)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(owner, 20*time.Millisecond)
	defer cancel()
	probe := newCheckoutProbe(ctx, root, nil, nil)
	done := make(chan bool, 1)
	go func() { done <- probe.skip(child, "deploy") }()
	select {
	case skip := <-done:
		if !skip || !errors.Is(ctx.Err(), context.DeadlineExceeded) || probe.found() != "" {
			t.Fatalf("cancelled probe: skip=%v ctx=%v found=%q", skip, ctx.Err(), probe.found())
		}
	case <-time.After(time.Second):
		t.Fatal("checkout callback did not cancel while waiting for admission")
	}
	if snapshot := controller.Snapshot(); snapshot.IOMax != 1 || snapshot.IOActive != 1 {
		t.Fatalf("callback bypassed or leaked its admission: %+v", snapshot)
	}
}

func TestCheckoutProbeInsideSizeWalkWithSingleIOWorker(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "deploy")
	if err := os.MkdirAll(filepath.Join(child, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	limits, _, _ := scanctl.Resolve("fast", "1")
	controller := scanctl.New(limits)
	ctx, cancel := context.WithTimeout(scanctl.With(t.Context(), controller), time.Second)
	defer cancel()
	probe := newCheckoutProbe(ctx, root, nil, nil)
	if _, err := fsx.Size(ctx, root, &fsx.Options{Skip: probe.skip}); err != nil {
		t.Fatal(err)
	}
	if got, want := probe.found(), filepath.Join(filepath.Base(root), "deploy"); got != want {
		t.Fatalf("nested checkout = %q, want %q", got, want)
	}
	if snapshot := controller.Snapshot(); snapshot.IOMax != 1 || snapshot.IOActive != 0 {
		t.Fatalf("size callback bypassed or leaked admission: %+v", snapshot)
	}
}
