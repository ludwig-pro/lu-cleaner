package sysx

import (
	"context"
	"errors"
	"sync"
	"time"
)

// probeCache shares one refresh without holding its mutex during inspection.
// A failed or cancelled refresh never replaces the last complete snapshot.
type probeCache[T any] struct {
	mu         sync.Mutex
	value      T
	at         time.Time
	valid      bool
	generation uint64
	flight     *probeFlight[T]
}

type probeFlight[T any] struct {
	done       chan struct{}
	value      T
	err        error
	generation uint64
}

func (c *probeCache[T]) get(ctx context.Context, ttl time.Duration, load func(context.Context) (T, error)) (T, error) {
	var zero T
	for {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		c.mu.Lock()
		if c.valid && time.Since(c.at) < ttl {
			value := c.value
			c.mu.Unlock()
			if err := ctx.Err(); err != nil {
				return zero, err
			}
			return value, nil
		}
		f := c.flight
		leader := f == nil
		if leader {
			f = &probeFlight[T]{done: make(chan struct{}), generation: c.generation}
			c.flight = f
		}
		c.mu.Unlock()
		if leader {
			value, err := load(ctx)
			if ctx.Err() != nil {
				err = ctx.Err()
			}
			c.mu.Lock()
			if ctx.Err() != nil {
				err = ctx.Err()
			}
			f.value, f.err = value, err
			if err == nil && f.generation == c.generation {
				c.value, c.at, c.valid = value, time.Now(), true
			}
			c.flight = nil
			close(f.done)
			c.mu.Unlock()
		} else {
			select {
			case <-ctx.Done():
				return zero, ctx.Err()
			case <-f.done:
			}
		}
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		c.mu.Lock()
		invalidated := f.generation != c.generation
		c.mu.Unlock()
		// A caller still active must not inherit another caller's cancellation.
		if invalidated || (!leader && (errors.Is(f.err, context.Canceled) || errors.Is(f.err, context.DeadlineExceeded))) {
			continue
		}
		if f.err != nil {
			return zero, f.err
		}
		return f.value, nil
	}
}

func (c *probeCache[T]) invalidate() {
	c.mu.Lock()
	c.valid = false
	c.generation++
	c.mu.Unlock()
}
