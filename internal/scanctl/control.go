// Package scanctl bounds the work performed by all scanners of one invocation.
package scanctl

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

type Mode string

const (
	Eco  Mode = "eco"
	Fast Mode = "fast"
)

type Limits struct {
	Mode      Mode          `json:"mode"`
	IO        int           `json:"io_workers"`
	Commands  int           `json:"command_workers"`
	Prefetch  int           `json:"prefetch_workers"`
	BatchSize int           `json:"batch_size"`
	Pause     time.Duration `json:"pause"`
}

// Resolve is pure: inspecting configuration never changes process policy.
func Resolve(mode, walkers string) (Limits, string, error) {
	if mode == "" {
		mode = string(Eco)
	}
	l := Limits{Mode: Mode(mode), BatchSize: 256}
	switch l.Mode {
	case Eco:
		l.IO, l.Commands, l.Prefetch, l.Pause = 2, 1, 1, 5*time.Millisecond
	case Fast:
		l.IO, l.Commands, l.Prefetch = 8, 4, 4
	default:
		return Limits{}, "", fmt.Errorf("scan mode must be eco or fast (got %q)", mode)
	}
	if walkers != "" {
		n, err := strconv.Atoi(walkers)
		if err != nil || n <= 0 {
			return l, fmt.Sprintf("invalid LU_WALKERS=%q: using %d I/O workers", walkers, l.IO), nil
		}
		l.IO = n
	}
	return l, "", nil
}

type counters struct {
	active, peak, cooling, wait, pause atomic.Int64
}

type Controller struct {
	limits                 Limits
	io, commands           chan struct{}
	ioStats, commandStats  counters
	files, dirs            atomic.Int64
	cacheHits, cacheMisses atomic.Int64
	cancelAt, finished     atomic.Int64
	bind                   sync.Once
	stop                   func() bool
	watchDone              chan struct{}
	closeOnce              sync.Once
	now                    func() time.Time
	sleep                  func(context.Context, time.Duration) error
}

func New(l Limits) *Controller {
	if l.IO <= 0 || l.Commands <= 0 || l.BatchSize <= 0 {
		l, _, _ = Resolve(string(Fast), "")
	}
	return &Controller{limits: l, io: make(chan struct{}, l.IO), commands: make(chan struct{}, l.Commands), now: time.Now, sleep: wait}
}

func (c *Controller) Limits() Limits { return c.limits }

type key struct{}

func With(ctx context.Context, c *Controller) context.Context {
	c.bind.Do(func() {
		c.watchDone = make(chan struct{})
		c.stop = context.AfterFunc(ctx, func() { c.cancelAt.CompareAndSwap(0, c.now().UnixNano()); close(c.watchDone) })
	})
	return context.WithValue(ctx, key{}, c)
}

func From(ctx context.Context) *Controller {
	c, _ := ctx.Value(key{}).(*Controller)
	return c
}

// Ensure gives standalone internal scans a controller without changing the
// process policy. The CLI supplies its resolved controller before this point.
func Ensure(ctx context.Context) context.Context {
	if From(ctx) != nil {
		return ctx
	}
	l, _, _ := Resolve(string(Fast), "")
	return With(ctx, New(l))
}

func wait(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return ctx.Err()
	}
}

func (c *Controller) acquire(ctx context.Context, sem chan struct{}, s *counters, cooldown time.Duration) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	start := c.now()
	defer func() { s.wait.Add(int64(c.now().Sub(start))) }()
	select {
	case sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		<-sem
		return nil, err
	}
	n := s.active.Add(1)
	for old := s.peak.Load(); n > old && !s.peak.CompareAndSwap(old, n); old = s.peak.Load() {
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			s.active.Add(-1)
			if cooldown > 0 {
				s.cooling.Add(1)
				start := c.now()
				_ = c.sleep(ctx, cooldown)
				s.pause.Add(int64(c.now().Sub(start)))
				s.cooling.Add(-1)
			}
			<-sem
		})
	}, nil
}

func (c *Controller) AcquireIO(ctx context.Context) (func(), error) {
	return c.acquire(ctx, c.io, &c.ioStats, c.limits.Pause)
}

// DoIO holds a slot only for a bounded synchronous operation. fn must not
// recurse, wait on a cache, execute commands or call another controlled helper.
func DoIO(ctx context.Context, fn func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c := From(ctx); c != nil {
		release, err := c.AcquireIO(ctx)
		if err != nil {
			return err
		}
		defer release()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn()
}

// Command admits work before starting its execution timeout. A deadline on
// the parent context still bounds both queuing and execution.
func Command(ctx context.Context, timeout time.Duration, fn func(context.Context) ([]byte, error)) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c := From(ctx); c != nil {
		release, err := c.acquire(ctx, c.commands, &c.commandStats, 0)
		if err != nil {
			return nil, err
		}
		defer release()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	return fn(ctx)
}

func (c *Controller) MarkEntries(files, dirs int64) { c.files.Add(files); c.dirs.Add(dirs) }

func (c *Controller) MarkCache(hit bool) {
	if hit {
		c.cacheHits.Add(1)
	} else {
		c.cacheMisses.Add(1)
	}
}

func (c *Controller) Close() {
	c.closeOnce.Do(func() {
		if c.stop != nil {
			if !c.stop() {
				<-c.watchDone
			}
		}
		c.finished.Store(c.now().UnixNano())
	})
}

type Stats struct {
	IOActive, IOMax, IOCooling int64
	IOWait, ThrottleWait       time.Duration
	CommandActive, CommandMax  int64
	CommandWait                time.Duration
	Files, Dirs                int64
	CacheHits, CacheMisses     int64
	CancelLatency              time.Duration
}

func (c *Controller) Snapshot() Stats {
	s := Stats{IOActive: c.ioStats.active.Load(), IOMax: c.ioStats.peak.Load(), IOCooling: c.ioStats.cooling.Load(), IOWait: time.Duration(c.ioStats.wait.Load()), ThrottleWait: time.Duration(c.ioStats.pause.Load()), CommandActive: c.commandStats.active.Load(), CommandMax: c.commandStats.peak.Load(), CommandWait: time.Duration(c.commandStats.wait.Load()), Files: c.files.Load(), Dirs: c.dirs.Load(), CacheHits: c.cacheHits.Load(), CacheMisses: c.cacheMisses.Load()}
	if at, end := c.cancelAt.Load(), c.finished.Load(); at > 0 && end >= at {
		s.CancelLatency = time.Duration(end - at)
	}
	return s
}
