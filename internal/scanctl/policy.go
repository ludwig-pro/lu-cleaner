package scanctl

import (
	"context"
	"runtime"
	"sync"
)

var applyBackground = background

// WorktreePermit attests that this activation set a previously absent process
// background bit. Its zero value cannot authorize changing a child policy.
type WorktreePermit struct{ lease *backgroundLease }

type backgroundLease struct {
	mu     sync.RWMutex
	active bool
}

type worktreePermitKey struct{}

func WithWorktreePermit(ctx context.Context, permit WorktreePermit) context.Context {
	return context.WithValue(ctx, worktreePermitKey{}, permit)
}

// WithOwnedBackground holds the activation alive through one child execution.
// A restored, absent or cancelled permit never calls f. Restore waits until
// admitted children finish; callers must cancel/join cleanup before restoring.
func WithOwnedBackground(ctx context.Context, f func() ([]byte, error)) ([]byte, bool, error) {
	permit, _ := ctx.Value(worktreePermitKey{}).(WorktreePermit)
	if permit.lease == nil {
		return nil, false, nil
	}
	permit.lease.mu.RLock()
	defer permit.lease.mu.RUnlock()
	if !permit.lease.active || ctx.Err() != nil {
		return nil, false, nil
	}
	out, err := f()
	return out, true, err
}

// Activate changes only this process, until the command returns. Keeping
// process policy outside Controller makes library scans and tests pure.
func Activate(l Limits) (func() error, WorktreePermit, error) {
	if l.Mode != Eco {
		return func() error { return nil }, WorktreePermit{}, nil
	}
	previous := runtime.GOMAXPROCS(0)
	runtime.GOMAXPROCS(min(previous, 2))
	restorePriority, owned, err := applyBackground()
	lease := &backgroundLease{active: owned && err == nil}
	permit := WorktreePermit{}
	if lease.active {
		permit.lease = lease
	}
	var once sync.Once
	var restoreErr error
	return func() error {
		once.Do(func() {
			lease.mu.Lock()
			defer lease.mu.Unlock()
			lease.active = false
			restoreErr = restorePriority()
			runtime.GOMAXPROCS(previous)
		})
		return restoreErr
	}, permit, err
}
