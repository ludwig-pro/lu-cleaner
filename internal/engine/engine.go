// Package engine runs providers concurrently and streams their items.
package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/diagnostics"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
	"github.com/ludwig-pro/lu-cleaner/internal/scanctl"
	"golang.org/x/sys/unix"
)

// Event is sent for every item upsert and when a provider finishes.
type Event struct {
	Item     *core.Item // item upsert (nil for Done events)
	Provider string
	Done     bool  // provider finished
	Err      error // provider error (with Done)
	Took     time.Duration
}

// Run starts every provider in its own goroutine and streams events on the
// returned channel. The channel is closed once all providers are done.
// A panicking provider is reported as an error instead of crashing the app.
//
// Sizes: providers measuring the same directory share one walk, and trees
// that did not change since a previous run are answered by the persistent
// size cache (fsx.SizeStore, validated with the FSEvents history; LU_NO_CACHE=1
// disables it). The cache is saved before the channel is closed.
func Run(ctx context.Context, env *core.Env, providers []core.Provider) <-chan Event {
	owned := scanctl.From(ctx) == nil
	if owned {
		ctx = scanctl.With(ctx, scanctl.New(env.ScanLimits))
	}
	ctl := scanctl.From(ctx)
	trees0, files0 := fsx.Walked()
	store := fsx.OpenSizeStore(ctx)
	ctx = fsx.WithSizeStore(ctx, store)
	ch := make(chan Event, 256)
	var wg sync.WaitGroup
	for _, p := range providers {
		wg.Add(1)
		go func(p core.Provider) {
			defer wg.Done()
			start := time.Now()
			var err error
			func() {
				defer func() {
					if recover() != nil {
						err = diagnostics.NewFault(p.ID())
					}
				}()
				err = p.Scan(ctx, env, func(it *core.Item) {
					if it == nil {
						return
					}
					if it.Provider == "" {
						it.Provider = p.ID()
					}
					if !it.Sizing && it.Method != core.MethodCommand && it.Method != core.MethodReport {
						it.Inodes = SnapshotContext(ctx, it.Targets())
					}
					select {
					case ch <- Event{Item: it, Provider: p.ID()}:
					case <-ctx.Done():
					}
				})
			}()
			diagnostics.Capture(ctx, err)
			if err != nil && ctx.Err() == nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, os.ErrPermission) && !errors.Is(err, os.ErrNotExist) {
				var fault *diagnostics.Fault
				if !errors.As(err, &fault) {
					diagnostics.Report(ctx, p.ID(), "scan_failed")
				}
			}
			select {
			case ch <- Event{Provider: p.ID(), Done: true, Err: err, Took: time.Since(start)}:
			case <-ctx.Done():
			}
		}(p)
	}
	go func() {
		wg.Wait()
		fsx.WaitCache(ctx)
		store.Close() // prints its own summary with LU_TRACE
		if owned {
			ctl.Close()
		}
		if store == nil && fsx.Tracing() {
			trees, files := fsx.Walked()
			fmt.Fprintf(os.Stderr, "[trace] cache disabled; walked %d trees, %d files\n", trees-trees0, files-files0)
		}
		close(ch)
	}()
	return ch
}

// Snapshot returns the inode of each path (0 when missing), without following symlinks.
func Snapshot(paths []string) []uint64 {
	return SnapshotContext(context.Background(), paths)
}

func SnapshotContext(ctx context.Context, paths []string) []uint64 {
	if len(paths) == 0 {
		return nil
	}
	out := make([]uint64, len(paths))
	var st unix.Stat_t
	for first := 0; first < len(paths); first += 256 {
		if err := scanctl.DoIO(ctx, func() error {
			for i := first; i < min(first+256, len(paths)); i++ {
				if err := ctx.Err(); err != nil {
					return err
				}
				if unix.Lstat(paths[i], &st) == nil {
					out[i] = st.Ino
				}
			}
			return nil
		}); err != nil {
			break
		}
	}
	return out
}

// Result is the outcome of Collect.
type Result struct {
	Items  []*core.Item
	Errors map[string]error
	Timing map[string]time.Duration
	Took   time.Duration
}

// Collect runs the providers to completion and returns the final items
// (upserts applied, insertion order kept). onEvent may be nil.
func Collect(ctx context.Context, env *core.Env, providers []core.Provider, onEvent func(Event)) *Result {
	start := time.Now()
	res := &Result{Errors: map[string]error{}, Timing: map[string]time.Duration{}}
	idx := map[string]int{}
	for ev := range Run(ctx, env, providers) {
		if onEvent != nil {
			onEvent(ev)
		}
		if ev.Done {
			res.Timing[ev.Provider] = ev.Took
			if ev.Err != nil && ctx.Err() == nil {
				res.Errors[ev.Provider] = ev.Err
			}
			continue
		}
		if i, ok := idx[ev.Item.ID]; ok {
			res.Items[i] = ev.Item
		} else {
			idx[ev.Item.ID] = len(res.Items)
			res.Items = append(res.Items, ev.Item)
		}
	}
	res.Took = time.Since(start)
	return res
}
