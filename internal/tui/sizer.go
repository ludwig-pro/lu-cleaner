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

type sizeBatchMsg struct{ results []sizeResult }

// sizePool measures directories with a fixed number of workers. Jobs form a
// LIFO stack: the directory the user just entered is measured first, while
// older jobs (directories left behind) still complete and fill the cache.
type sizePool struct {
	ctx    context.Context
	mu     sync.Mutex
	cond   *sync.Cond
	stack  []string
	closed bool
	out    chan sizeResult
	sizeFn func(context.Context, string) (fsx.Stats, error)
}

func newSizePool(ctx context.Context, workers int) *sizePool {
	p := &sizePool{
		ctx: ctx,
		out: make(chan sizeResult, 512),
		sizeFn: func(ctx context.Context, path string) (fsx.Stats, error) {
			return fsx.Size(ctx, path, nil)
		},
	}
	p.cond = sync.NewCond(&p.mu)
	for i := 0; i < workers; i++ {
		go p.worker()
	}
	go func() {
		<-ctx.Done()
		p.mu.Lock()
		p.closed = true
		p.cond.Broadcast()
		p.mu.Unlock()
	}()
	return p
}

// push queues paths; the first one is measured first.
func (p *sizePool) push(paths ...string) {
	if len(paths) == 0 {
		return
	}
	p.mu.Lock()
	for i := len(paths) - 1; i >= 0; i-- {
		p.stack = append(p.stack, paths[i])
	}
	p.cond.Broadcast()
	p.mu.Unlock()
}

func (p *sizePool) worker() {
	for {
		p.mu.Lock()
		for len(p.stack) == 0 && !p.closed {
			p.cond.Wait()
		}
		if p.closed {
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
func waitSizes(ctx context.Context, out <-chan sizeResult) tea.Cmd {
	return func() tea.Msg {
		var b sizeBatchMsg
		select {
		case r := <-out:
			b.results = append(b.results, r)
		case <-ctx.Done():
			return nil
		}
		t := time.NewTimer(30 * time.Millisecond)
		defer t.Stop()
		for len(b.results) < 2048 {
			select {
			case r := <-out:
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
