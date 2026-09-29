// Package sizes holds measuring helpers shared by the providers on top of
// fsx.Size.
package sizes

import (
	"context"
	"sync"

	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
)

// Prefetcher measures paths in the background through the run's size cache
// (fsx.Size without options), so a walk starts as soon as its path is known,
// while the provider still works out whether (and how) to show it: git
// state... Later fsx.Size calls without options on those paths get the
// result, or wait for the walk in flight. Without a run cache
// (fsx.WithCache) it only wastes walks.
//
// Walks are never cancelled early (a cancelled walk would hand its partial
// result to every provider waiting on the same path): Close drops the paths
// not started yet and waits for the walks in flight.
type Prefetcher struct {
	ctx    context.Context
	mu     sync.Mutex
	cond   *sync.Cond
	queue  []string
	seen   map[string]bool
	closed bool
	wg     sync.WaitGroup
}

// NewPrefetcher starts workers walking the paths given to Add, in order.
func NewPrefetcher(ctx context.Context, workers int) *Prefetcher {
	p := &Prefetcher{ctx: ctx, seen: map[string]bool{}}
	p.cond = sync.NewCond(&p.mu)
	for range max(workers, 1) {
		p.wg.Add(1)
		go p.work()
	}
	return p
}

// Add queues path (once).
func (p *Prefetcher) Add(path string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.seen[path] {
		return
	}
	p.seen[path] = true
	p.queue = append(p.queue, path)
	p.cond.Signal()
}

// Close drops the paths not started yet and waits for the walks in flight.
// It may be called more than once.
func (p *Prefetcher) Close() {
	p.mu.Lock()
	p.closed = true
	p.queue = nil
	p.cond.Broadcast()
	p.mu.Unlock()
	p.wg.Wait()
}

func (p *Prefetcher) work() {
	defer p.wg.Done()
	for {
		p.mu.Lock()
		for len(p.queue) == 0 && !p.closed {
			p.cond.Wait()
		}
		if p.closed {
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
