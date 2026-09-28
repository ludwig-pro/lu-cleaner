// Package engine runs providers concurrently and streams their items.
package engine

import (
	"context"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
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
func Run(ctx context.Context, env *core.Env, providers []core.Provider) <-chan Event {
	ctx = fsx.WithCache(ctx) // providers measuring the same directory share one walk
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
					if r := recover(); r != nil {
						err = fmt.Errorf("panic in provider %s: %v\n%s", p.ID(), r, debug.Stack())
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
						it.Inodes = Snapshot(it.Targets())
					}
					select {
					case ch <- Event{Item: it, Provider: p.ID()}:
					case <-ctx.Done():
					}
				})
			}()
			select {
			case ch <- Event{Provider: p.ID(), Done: true, Err: err, Took: time.Since(start)}:
			case <-ctx.Done():
			}
		}(p)
	}
	go func() { wg.Wait(); close(ch) }()
	return ch
}

// Snapshot returns the inode of each path (0 when missing), without following symlinks.
func Snapshot(paths []string) []uint64 {
	if len(paths) == 0 {
		return nil
	}
	out := make([]uint64, len(paths))
	var st unix.Stat_t
	for i, p := range paths {
		if unix.Lstat(p, &st) == nil {
			out[i] = st.Ino
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
