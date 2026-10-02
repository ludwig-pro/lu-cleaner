package tui

import (
	"context"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
)

type sizeResult struct {
	path string
	st   fsx.Stats
	err  error
}

type sizeBatchMsg struct {
	pool    *sizePool
	results []sizeResult
}

// sizePool measures directories with a fixed number of workers. Jobs form a
// LIFO stack: the directory the user just entered is measured first, while
// older jobs (directories left behind) still complete and fill the cache.
// These are required row/total measurements, not speculative prefetch: the
// stack follows the listed directories and must not drop paths at a 128-job cap.
type sizePool struct {
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	cond   *sync.Cond
	stack  []string
	closed bool
	out    chan sizeResult
	sizeFn func(context.Context, string) (fsx.Stats, error)
	wg     sync.WaitGroup
	done   chan struct{}
}

func newSizePool(ctx context.Context, workers int) *sizePool {
	ctx, cancel := context.WithCancel(ctx)
	p := &sizePool{
		ctx:    ctx,
		cancel: cancel,
		out:    make(chan sizeResult, 512),
		done:   make(chan struct{}),
		sizeFn: func(ctx context.Context, path string) (fsx.Stats, error) {
			return fsx.Size(ctx, path, nil)
		},
	}
	p.cond = sync.NewCond(&p.mu)
	for range max(1, workers) {
		p.wg.Go(p.worker)
	}
	go func() {
		<-ctx.Done()
		p.mu.Lock()
		p.closed = true
		p.stack = nil
		p.cond.Broadcast()
		p.mu.Unlock()
		p.wg.Wait()
		close(p.out)
		close(p.done)
	}()
	return p
}

// close cancels queued and active work, wakes idle workers and joins them.
// The size function receives the pool context and must respect cancellation.
func (p *sizePool) close() {
	p.cancel()
	<-p.done
}

// push queues paths; the first one is measured first.
func (p *sizePool) push(paths ...string) {
	if len(paths) == 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.ctx.Err() != nil {
		return
	}
	for i := len(paths) - 1; i >= 0; i-- {
		p.stack = append(p.stack, paths[i])
	}
	p.cond.Broadcast()
}

func (p *sizePool) worker() {
	for {
		p.mu.Lock()
		for len(p.stack) == 0 && !p.closed && p.ctx.Err() == nil {
			p.cond.Wait()
		}
		if p.closed || p.ctx.Err() != nil {
			p.mu.Unlock()
			return
		}
		path := p.stack[len(p.stack)-1]
		p.stack = p.stack[:len(p.stack)-1]
		p.mu.Unlock()

		st, err := p.sizeFn(p.ctx, path)
		select {
		case p.out <- sizeResult{path: path, st: st, err: err}:
		case <-p.ctx.Done():
			return
		}
	}
}

// waitSizes delivers measured sizes to the program, coalescing results that
// arrive within a short window into one message.
func waitSizes(ctx context.Context, pool *sizePool) tea.Cmd {
	return func() tea.Msg {
		b := sizeBatchMsg{pool: pool}
		out := pool.out
		select {
		case r, ok := <-out:
			if !ok {
				return nil
			}
			b.results = append(b.results, r)
		case <-ctx.Done():
			return nil
		}
		t := time.NewTimer(30 * time.Millisecond)
		defer t.Stop()
		for len(b.results) < 2048 {
			select {
			case r, ok := <-out:
				if !ok {
					return b
				}
				b.results = append(b.results, r)
			case <-t.C:
				return b
			case <-ctx.Done():
				return b
			}
		}
		return b
	}
}
