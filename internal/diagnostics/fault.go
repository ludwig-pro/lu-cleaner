// Package diagnostics reports technical failures using a closed schema. Raw
// errors, panic values, paths, commands and environment are never serialized.
package diagnostics

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync"
)

type Frame struct {
	Filename string `json:"filename"`
	Function string `json:"function"`
	Lineno   int    `json:"lineno"`
	InApp    bool   `json:"in_app"`
}

type Fault struct {
	Component string
	Frames    []Frame
	code      string
}

func (f *Fault) Error() string {
	return "internal failure in " + f.Component + " (panic; scan may be incomplete)"
}

func NewFault(component string) *Fault {
	pcs := make([]uintptr, 48)
	n := runtime.Callers(2, pcs)
	frames := runtime.CallersFrames(pcs[:n])
	f := &Fault{Component: componentName(component), code: "internal_panic"}
	for {
		frame, more := frames.Next()
		const module = "github.com/ludwig-pro/lu-cleaner/"
		if strings.HasPrefix(frame.Function, module) {
			fn := strings.TrimPrefix(frame.Function, module)
			// Only repository paths are retained; no absolute build directory.
			for _, prefix := range []string{"/internal/", "/cmd/"} {
				if i := strings.LastIndex(frame.File, prefix); i >= 0 {
					f.Frames = append(f.Frames, Frame{Filename: frame.File[i+1:], Function: fn, Lineno: frame.Line, InApp: true})
					break
				}
			}
		}
		if !more || len(f.Frames) == 24 {
			break
		}
	}
	return f
}

// Report accepts only fixed technical error families; it never examines the
// error string or gathers a stack, paths or arguments for an operational error.
func Report(ctx context.Context, component, code string) {
	switch code {
	case "scan_failed", "cleanup_failed", "history_write_failed", "command_failed":
		Capture(ctx, &Fault{Component: componentName(component), code: code})
	}
}

func componentName(s string) string {
	switch s {
	case "cli", "engine", "clean", "history", "fsx", "tui", "ai", "apple", "android", "js", "artifacts", "worktrees", "catalog", "system":
		return s
	default:
		return "internal"
	}
}

// Catch contains a synchronous task's panic without exposing its value.
func Catch(ctx context.Context, component string, fn func() error) (err error) {
	defer func() {
		if recover() != nil {
			err = NewFault(component)
			Capture(ctx, err)
		}
	}()
	return fn()
}

// Group gives sibling workers a shared failure and cancellation scope. Each
// worker must defer Recover directly and release its permits / Done separately.
// The caller still joins every worker before returning Err.
type Group struct {
	ctx       context.Context
	cancel    context.CancelFunc
	component string
	mu        sync.Mutex
	fault     error
}

type groupKey struct{}

func NewGroup(ctx context.Context, component string) (context.Context, *Group) {
	child, cancel := context.WithCancel(ctx)
	g := &Group{ctx: child, cancel: cancel, component: component}
	return context.WithValue(child, groupKey{}, g), g
}

func (g *Group) Recover() {
	if recover() == nil {
		return
	}
	f := NewFault(g.component)
	g.fail(f)
}

// Finish contains the owner's panic before joining its children. Defer it
// after the worker state is allocated, and read Err only after it has run.
func (g *Group) Finish(join func()) {
	if recover() != nil {
		g.fail(NewFault(g.component))
	}
	join()
}

func (g *Group) fail(f error) {
	g.mu.Lock()
	if g.fault == nil {
		g.fault = f
	}
	g.mu.Unlock()
	Capture(g.ctx, f)
	g.cancel()
}

// Recover is used by nested workers whose owner installed a Group. WaitGroups,
// output channels and permits must still be released by the worker's defers.
func Recover(ctx context.Context, component string) {
	if recover() == nil {
		return
	}
	f := NewFault(component)
	if g, ok := ctx.Value(groupKey{}).(*Group); ok {
		g.fail(f)
	} else {
		Capture(ctx, f)
	}
}

// Fail propagates a contained lower-level failure to its consuming scan. Shared
// size calculations keep their own owner context; a consumer cancels only its
// provider, not a calculation another provider is awaiting.
func Fail(ctx context.Context, err error) {
	var f *Fault
	if errors.As(err, &f) {
		if g, ok := ctx.Value(groupKey{}).(*Group); ok {
			g.fail(f)
		}
		Capture(ctx, f)
	}
}

func (g *Group) Err() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.fault != nil {
		return g.fault
	}
	return g.ctx.Err()
}

func (g *Group) Close() { g.cancel() }

type reporterKey struct{}
type Reporter interface{ Capture(error) }

func With(ctx context.Context, r Reporter) context.Context {
	return context.WithValue(ctx, reporterKey{}, r)
}

// Capture ignores cancellation and reports only typed internal failures. Normal
// filesystem errors, safety refusals and command exit messages are not uploads.
func Capture(ctx context.Context, err error) {
	var f *Fault
	if !errors.As(err, &f) {
		return
	}
	if r, ok := ctx.Value(reporterKey{}).(Reporter); ok {
		r.Capture(f)
	}
}
