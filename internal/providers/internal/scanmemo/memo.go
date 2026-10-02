// Package scanmemo shares provider inspections without holding cache locks
// while their work waits for scan admission. Every consumer wait is cancellable.
package scanmemo

import (
	"context"
	"errors"
	"sync"
	"time"
)

var errInterrupted = errors.New("provider inspection interrupted")

type result[V any] struct {
	done    chan struct{}
	value   V
	err     error
	expires time.Time // protected by Cache.mu
}

// Cache shares one inspection per key. The calling provider owns the loader:
// it runs synchronously outside the mutex, with no detached work to join.
type Cache[K comparable, V any] struct {
	mu      sync.Mutex
	entries map[K]*result[V]
}

func (c *Cache[K, V]) Get(ctx context.Context, key K, load func(context.Context) (V, error)) (V, error) {
	return c.GetTTL(ctx, key, 0, load)
}

// GetTTL expires completed results after ttl; zero retains them for this run.
// Cancelled inspections are not cached, so a later live consumer may retry.
func (c *Cache[K, V]) GetTTL(ctx context.Context, key K, ttl time.Duration, load func(context.Context) (V, error)) (V, error) {
	var zero V
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	c.mu.Lock()
	if c.entries == nil {
		c.entries = make(map[K]*result[V])
	}
	r := c.entries[key]
	if r != nil && !r.expires.IsZero() && !time.Now().Before(r.expires) {
		delete(c.entries, key)
		r = nil
	}
	leader := r == nil
	if leader {
		r = &result[V]{done: make(chan struct{})}
		c.entries[key] = r
	}
	c.mu.Unlock()
	if leader {
		func() {
			completed := false
			defer func() {
				if !completed {
					r.err = errInterrupted // release waiters even when load panics
				}
				c.mu.Lock()
				if !completed || ctx.Err() != nil {
					delete(c.entries, key)
				} else if ttl > 0 {
					r.expires = time.Now().Add(ttl)
				}
				close(r.done)
				c.mu.Unlock()
			}()
			r.value, r.err = load(ctx)
			if ctx.Err() != nil {
				r.err = ctx.Err()
			}
			completed = true
		}()
	}
	return wait(ctx, r)
}

func wait[V any](ctx context.Context, r *result[V]) (V, error) {
	var zero V
	select {
	case <-ctx.Done():
		return zero, ctx.Err()
	case <-r.done:
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		return r.value, r.err
	}
}

// Once initializes fields owned by one scan. Readers may inspect those fields
// only after Do returns nil. A cancelled waiter returns without reading fields
// that the loader may still be filling. Like sync.Once, initialization is not
// retried; the scan context owns its lifetime.
type Once struct {
	mu     sync.Mutex
	flight *result[struct{}]
}

func (o *Once) Do(ctx context.Context, fn func()) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	o.mu.Lock()
	r := o.flight
	leader := r == nil
	if leader {
		r = &result[struct{}]{done: make(chan struct{})}
		o.flight = r
	}
	o.mu.Unlock()
	if leader {
		func() {
			completed := false
			defer func() {
				if completed {
					r.err = ctx.Err()
				} else {
					r.err = errInterrupted
				}
				close(r.done)
			}()
			fn()
			completed = true
		}()
	}
	_, err := wait(ctx, r)
	return err
}
