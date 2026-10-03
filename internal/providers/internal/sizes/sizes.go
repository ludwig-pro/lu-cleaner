// Package sizes holds measuring helpers shared by the providers on top of
// fsx.Size.
package sizes

import (
	"context"
	"sync"

	"github.com/ludwig-pro/lu-cleaner/internal/diagnostics"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
)

// Prefetcher measures paths in the background through the run's size cache
// (fsx.Size without options), so a walk starts as soon as its path is known,
// while the provider still works out whether (and how) to show it: git
// state... Later fsx.Size calls without options on those paths get the
// result, or wait for the walk in flight. Without a run cache
// (fsx.WithCache) it only wastes walks.
//
// Close drops pending work and waits for the walks in flight. Cancelling the
// scan also stops those walks; fsx never caches their partial results.
type Prefetcher struct {
	ctx    context.Context
	mu     sync.Mutex
	cond   *sync.Cond
	queue  []string
	seen   map[string]bool
	closed bool
	wg     sync.WaitGroup
	done   chan struct{}
}

const maxPending = 128

// NewPrefetcher starts workers walking the paths given to Add, in order.
func NewPrefetcher(ctx context.Context, workers int) *Prefetcher {
	p := &Prefetcher{ctx: ctx, seen: map[string]bool{}, done: make(chan struct{})}
	p.cond = sync.NewCond(&p.mu)
	for range max(workers, 1) {
		p.wg.Add(1)
		go p.work()
	}
	go func() { p.wg.Wait(); close(p.done) }()
	go func() {
		select {
		case <-ctx.Done():
			p.stop()
		case <-p.done:
		}
	}()
	return p
}

// Add queues a path once when space is available. Dropping speculative
// work never prevents the provider from measuring that path later.
func (p *Prefetcher) Add(path string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.ctx.Err() != nil || p.seen[path] || len(p.queue) >= maxPending {
		return
	}
	p.seen[path] = true
	p.queue = append(p.queue, path)
	p.cond.Signal()
}

// Close drops the paths not started yet and waits for the walks in flight.
// It may be called more than once.
func (p *Prefetcher) Close() {
	p.stop()
	<-p.done
}

func (p *Prefetcher) stop() {
	p.mu.Lock()
	p.closed = true
	p.queue = nil
	p.cond.Broadcast()
	p.mu.Unlock()
}

func (p *Prefetcher) work() {
	defer p.wg.Done()
	defer diagnostics.Recover(p.ctx, "fsx")
	for {
		p.mu.Lock()
		for len(p.queue) == 0 && !p.closed && p.ctx.Err() == nil {
			p.cond.Wait()
		}
		if p.closed || p.ctx.Err() != nil {
			p.mu.Unlock()
			return
		}
		path := p.queue[0]
		p.queue = p.queue[1:]
		p.mu.Unlock()
		if p.ctx.Err() != nil {
			continue // drain without walking; Close ends the loop
		}
		fsx.Size(p.ctx, path, nil)
	}
}
