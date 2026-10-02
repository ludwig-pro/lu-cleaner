package scanmemo

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func await[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(time.Second):
		t.Fatal("inspection stayed blocked")
		var zero T
		return zero
	}
}

func TestCacheSharesSlowInspectionWithoutBlockingOtherKeysOrCancellation(t *testing.T) {
	var cache Cache[string, int]
	started, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	var calls atomic.Int32
	leader := make(chan int, 1)
	go func() {
		value, _ := cache.Get(t.Context(), "slow", func(context.Context) (int, error) {
			calls.Add(1)
			close(started)
			<-release // stands in for an inspection waiting for its admission
			return 42, nil
		})
		leader <- value
	}()
	await(t, started)
	other := make(chan int, 1)
	go func() {
		value, _ := cache.Get(t.Context(), "other", func(context.Context) (int, error) { return 7, nil })
		other <- value
	}()
	if got := await(t, other); got != 7 {
		t.Fatalf("unrelated lookup = %d", got)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	waiter := make(chan error, 1)
	go func() {
		_, err := cache.Get(ctx, "slow", func(context.Context) (int, error) {
			calls.Add(1)
			return -1, nil
		})
		waiter <- err
	}()
	if err := await(t, waiter); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting consumer = %v, want deadline", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("shared inspection ran %d times", got)
	}
	// The cancelled consumer must leave the provider-owned inspection alive.
	select {
	case <-leader:
		t.Fatal("cancelled waiter ended the leader")
	default:
	}
	release <- struct{}{}
	if got := await(t, leader); got != 42 {
		t.Fatalf("provider-owned inspection = %d", got)
	}
}

func TestCacheCancelledLeaderDoesNotPoisonRetry(t *testing.T) {
	var cache Cache[string, int]
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if _, err := cache.Get(ctx, "key", func(context.Context) (int, error) {
		cancel()
		return 1, nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled inspection = %v", err)
	}
	value, err := cache.Get(t.Context(), "key", func(context.Context) (int, error) { return 2, nil })
	if err != nil || value != 2 {
		t.Fatalf("retry = %d, %v", value, err)
	}
}

func TestOnceWaiterCancelsBeforeFieldsArePublished(t *testing.T) {
	var once Once
	started, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	leader := make(chan error, 1)
	go func() {
		leader <- once.Do(t.Context(), func() { close(started); <-release })
	}()
	await(t, started)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	waiter := make(chan error, 1)
	go func() {
		waiter <- once.Do(ctx, func() { t.Error("duplicate initialization") })
	}()
	if err := await(t, waiter); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting consumer = %v, want deadline", err)
	}
	select {
	case <-leader:
		t.Fatal("initializer exited before publishing")
	default:
	}
	release <- struct{}{}
	if err := await(t, leader); err != nil {
		t.Fatalf("initializer = %v", err)
	}
}
