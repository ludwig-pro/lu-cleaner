package tui

import (
	"context"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
)

// scanTaskGroup owns scan work independently of Bubble Tea's Cmd goroutines,
// which the library does not join when its program exits.
type scanTaskGroup struct {
	ctx    context.Context
	mu     sync.Mutex
	closed bool
	wg     sync.WaitGroup
}

func newScanTaskGroup(ctx context.Context) *scanTaskGroup {
	return &scanTaskGroup{ctx: ctx}
}

// run admits and starts the work before returning its Cmd. An abandoned Cmd
// therefore cannot leave an unstarted task registered in the group.
func (g *scanTaskGroup) run(fn func() tea.Msg) tea.Cmd {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed || g.ctx.Err() != nil {
		return nil
	}
	g.wg.Add(1)
	done := make(chan struct{})
	var msg tea.Msg
	var panicValue any
	go func() {
		defer g.wg.Done()
		defer close(done)
		// Propagate a panic through the Cmd so Bubble Tea keeps handling it.
		defer func() { panicValue = recover() }()
		if g.ctx.Err() == nil {
			msg = fn()
		}
	}()
	return func() tea.Msg {
		<-done
		if panicValue != nil {
			panic(panicValue)
		}
		return msg
	}
}

// close seals admissions before waiting, so Add can never race with a Wait
// on an empty group. The caller cancels the scan context before closing it.
func (g *scanTaskGroup) close() {
	g.mu.Lock()
	g.closed = true
	g.mu.Unlock()
	g.wg.Wait()
}
