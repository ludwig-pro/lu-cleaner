package scanctl

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

func TestWorktreePermitRequiresOwnedSuccessfulActivation(t *testing.T) {
	old := applyBackground
	t.Cleanup(func() { applyBackground = old })
	for _, tc := range []struct {
		name              string
		mode              Mode
		owned, fail, want bool
	}{
		{"owned", Eco, true, false, true}, {"preexisting", Eco, false, false, false},
		{"failed", Eco, true, true, false}, {"fast", Fast, true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var restored int
			applyBackground = func() (func() error, bool, error) {
				var err error
				if tc.fail {
					err = errors.New("fixture activation failure")
				}
				return func() error { restored++; return nil }, tc.owned, err
			}
			restore, permit, _ := Activate(Limits{Mode: tc.mode})
			ctx := WithWorktreePermit(context.Background(), permit)
			called := false
			_, attempted, err := WithOwnedBackground(ctx, func() ([]byte, error) { called = true; return []byte("ok"), nil })
			if called != tc.want || attempted != tc.want || err != nil {
				t.Fatalf("called=%v attempted=%v err=%v", called, attempted, err)
			}
			if err := restore(); err != nil {
				t.Fatal(err)
			}
			if err := restore(); err != nil {
				t.Fatal(err)
			}
			_, attempted, _ = WithOwnedBackground(ctx, func() ([]byte, error) { t.Error("expired permit admitted work"); return nil, nil })
			if attempted {
				t.Fatal("permit remained active")
			}
			if tc.mode == Eco && restored != 1 {
				t.Fatalf("restore count %d", restored)
			}
		})
	}
}

func TestRestoreWaitsForAdmittedChildAndThenClosesPermit(t *testing.T) {
	old := applyBackground
	t.Cleanup(func() { applyBackground = old })
	var restored atomic.Int32
	applyBackground = func() (func() error, bool, error) { return func() error { restored.Add(1); return nil }, true, nil }
	restore, permit, err := Activate(Limits{Mode: Eco})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithWorktreePermit(context.Background(), permit)
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		WithOwnedBackground(ctx, func() ([]byte, error) {
			close(entered)
			<-release
			if restored.Load() != 0 {
				t.Error("restore ran during admitted child")
			}
			return nil, nil
		})
	}()
	<-entered
	if permit.lease.mu.TryLock() {
		permit.lease.mu.Unlock()
		t.Fatal("child does not hold the lease")
	}
	restoredDone := make(chan struct{})
	go func() { defer close(restoredDone); _ = restore() }()
	close(release)
	<-finished
	<-restoredDone
	if restored.Load() != 1 {
		t.Fatal("restore did not finish")
	}
	_, attempted, _ := WithOwnedBackground(ctx, func() ([]byte, error) { t.Error("expired permit reused"); return nil, nil })
	if attempted {
		t.Fatal("expired permit")
	}
}

func TestAbsentAndCancelledPermitDoNotCall(t *testing.T) {
	for _, ctx := range []context.Context{context.Background(), WithWorktreePermit(context.Background(), WorktreePermit{})} {
		_, attempted, _ := WithOwnedBackground(ctx, func() ([]byte, error) { t.Error("absent permit admitted work"); return nil, nil })
		if attempted {
			t.Fatal("absent permit")
		}
	}
	ctx, cancel := context.WithCancel(WithWorktreePermit(context.Background(), WorktreePermit{lease: &backgroundLease{active: true}}))
	cancel()
	_, attempted, _ := WithOwnedBackground(ctx, func() ([]byte, error) { t.Error("cancelled permit admitted work"); return nil, nil })
	if attempted {
		t.Fatal("cancelled permit")
	}
}
