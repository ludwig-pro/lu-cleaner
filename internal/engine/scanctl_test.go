package engine

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
	"github.com/ludwig-pro/lu-cleaner/internal/scanctl"
)

type discoverySizer struct{ sizer }

func (p discoverySizer) Scan(ctx context.Context, e *core.Env, emit core.Emit) error {
	for _, path := range p.paths {
		if _, err := fsx.ReadDir(ctx, path); err != nil {
			return err
		}
	}
	return p.sizer.Scan(ctx, e, emit)
}

func TestProvidersRootsAndDiscoveryShareBudget(t *testing.T) {
	t.Setenv("LU_NO_CACHE", "1")
	var roots []string
	base := t.TempDir()
	for i := 0; i < 12; i++ {
		path := filepath.Join(base, fmt.Sprint(i))
		writeFiles(t, path, 4, 4096)
		roots = append(roots, path)
	}
	limits, _, _ := scanctl.Resolve("fast", "1")
	ctrl := scanctl.New(limits)
	ctx := scanctl.With(context.Background(), ctrl)
	defer ctrl.Close()
	providers := []core.Provider{discoverySizer{sizer{roots}}, sizer{roots}, discoverySizer{sizer{roots}}}
	res := Collect(ctx, &core.Env{ScanLimits: limits}, providers, nil)
	if len(res.Items) != 12 || len(res.Errors) != 0 {
		t.Fatalf("results: %d items %v", len(res.Items), res.Errors)
	}
	if s := ctrl.Snapshot(); s.IOMax != 1 || s.IOActive != 0 || s.CacheMisses != 12 || s.CacheHits != 24 {
		t.Fatalf("actual admission and shared cache: %+v", s)
	}
}

func TestEngineCancellationJoinsSharedSizeWorkers(t *testing.T) {
	t.Setenv("LU_NO_CACHE", "1")
	root := t.TempDir()
	writeFiles(t, root, 2048, 1)
	l, _, _ := scanctl.Resolve("eco", "1")
	ctrl := scanctl.New(l)
	ctx, cancel := context.WithCancel(context.Background())
	ctx = scanctl.With(ctx, ctrl)
	defer ctrl.Close()
	// Hold the only disk slot: cancellation must release every provider waiting
	// for discovery and the shared size cache without doing any disk work.
	release, err := ctrl.AcquireIO(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ch := Run(ctx, &core.Env{ScanLimits: l}, []core.Provider{sizer{[]string{root}}, discoverySizer{sizer{[]string{root}}}})
	start := time.Now()
	cancel()
	release()
	done := make(chan struct{})
	go func() {
		for range ch {
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("software cancellation did not join providers and cache")
	}
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Fatalf("join took %s", d)
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal(ctx.Err())
	}
	if s := ctrl.Snapshot(); s.IOActive != 0 || s.IOCooling != 0 {
		t.Fatalf("workers retained disk slots: %+v", s)
	}
}
