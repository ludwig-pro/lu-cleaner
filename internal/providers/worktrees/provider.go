// Package worktrees finds git linked worktrees — the ones AI tools (Codex,
// Cursor, Conductor, Claude Code, Claude desktop...) spawn by the dozen, each
// with its own node_modules, Pods and native builds — and proposes removing
// whole worktrees through `git worktree remove`, plus `git worktree prune`
// for stale metadata.
//
// Discovery:
//  1. walk env.WorktreeRoots (depth ≤ 4) for directories holding a .git FILE;
//  2. walk env.Roots (depth ≤ env.MaxDepth) for main repositories (.git
//     DIRECTORY) and stray linked worktrees, without descending into repos;
//  3. `git worktree list --porcelain` on every main repository found (and on
//     the main of every worktree found), which catches worktrees anywhere;
//  4. <main>/.claude/worktrees, <main>/.worktrees, <main>/worktrees and
//     <main>-worktrees.
//
// Worktrees are deduplicated by device/inode. Each one gets its git state
// (dirty, unpushed, merged, locked, orphaned), the state of the tool that
// created it (Codex thread, Claude desktop session, Conductor workspace),
// processes whose cwd is inside, ignored secrets that would be lost (.env,
// keystores...), nested repositories, and a size breakdown (node_modules,
// Pods, native builds...).
//
// Deletion safety: a worktree is an orphan (removed with rm -rf, never
// recommended) only when its git metadata verifiably does not exist (ENOENT
// under a readable folder). Unreadable metadata (permissions, macOS privacy
// protection) is report-only, and so is a checkout still listed by a renamed
// or moved main repository (it needs `git worktree repair`). Orphans, prune
// items and worktrees are re-verified right before cleaning (Item.Recheck).
package worktrees

import (
	"context"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/diagnostics"
	"github.com/ludwig-pro/lu-cleaner/internal/safety"
	"github.com/ludwig-pro/lu-cleaner/internal/scanctl"
)

// Provider implements core.Provider.
type Provider struct {
	// getwd returns the current directory (overridable in tests).
	getwd func() (string, error)
}

// New returns the provider.
func New() *Provider { return &Provider{getwd: os.Getwd} }

func (p *Provider) ID() string    { return "worktrees" }
func (p *Provider) Title() string { return "Git worktrees" }
func (p *Provider) Categories() []core.Category {
	return []core.Category{core.CatWorktrees}
}

const (
	gitWorkers  = 6 // concurrent git inspections
	sizeWorkers = 4 // concurrent worktree size walks (fsx parallelises each walk too)

	wtRootDepth = 4 // depth below each WorktreeRoot
)

// Scan emits one item per linked worktree and one prune item per main
// repository holding stale worktree metadata.
func (p *Provider) Scan(ctx context.Context, env *core.Env, emit core.Emit) (err error) {
	ctx, group := diagnostics.NewGroup(ctx, "worktrees")
	defer group.Close()
	defer func() {
		if fault := group.Err(); fault != nil {
			err = fault
		}
	}()
	ctx = scanctl.Ensure(ctx)
	if env == nil || env.Runner == nil || env.Home == "" || !env.Has("git") {
		return nil
	}
	getwd := p.getwd
	if getwd == nil {
		getwd = os.Getwd
	}
	s := newScan(ctx, env, emit)
	defer group.Finish(func() {})
	if wd, err := getwd(); err == nil {
		s.cwd = realPath(wd)
	}
	return s.run()
}

// scan holds the state of one Scan call.
type scan struct {
	ctx  context.Context
	env  *core.Env
	emit core.Emit
	now  time.Time

	home    string   // real home
	tmp     string   // real per-user temp dir ("" if unknown)
	allowed []string // where the safety guard allows deletions (real paths)
	cwd     string   // real current directory

	mu      sync.Mutex
	repos   map[string]*repo      // main path -> repo
	byKey   map[fileKey]*worktree // dedup by device/inode
	ordered []*worktree

	tools *toolState
	live  liveTools // tool state re-read by rechecks at clean time
}

func newScan(ctx context.Context, env *core.Env, emit core.Emit) *scan {
	s := &scan{
		ctx:   ctx,
		env:   env,
		emit:  emit,
		now:   env.Now,
		home:  realPath(env.Home),
		repos: map[string]*repo{},
		byKey: map[fileKey]*worktree{},
	}
	if s.now.IsZero() {
		s.now = time.Now()
	}
	s.allowed = []string{s.home}
	if env.TmpDir != "" {
		s.tmp = realPath(env.TmpDir)
		// Mirror the guard exactly: the per-user folder holding T/ and C/ when
		// $TMPDIR is the per-user temp dir, else only what is inside $TMPDIR
		// (never / or /private for an unset or shared TMPDIR such as /tmp).
		s.allowed = append(s.allowed, safety.TempAreas(env.TmpDir)...)
	}
	return s
}

func (s *scan) run() error {
	// 1-2. filesystem discovery.
	s.discover()
	if s.ctx.Err() != nil {
		return s.ctx.Err()
	}
	// 3. ask every main repository for its worktrees (repeat when new mains
	// appear), then re-attach the checkouts of renamed / moved main repositories.
	s.listAll()
	if s.ctx.Err() != nil {
		return s.ctx.Err()
	}
	s.reconcile()

	wts := s.snapshot()

	// 6. sizes, the slowest part, need nothing from git: they are measured
	// (in discovery order) while the tool state and the git state are read,
	// and each item gets its size once both its git state and its
	// measurement are known.
	join := newSizeJoin(s, wts)
	sized := make(chan struct{})
	go func() {
		defer close(sized)
		defer diagnostics.Recover(s.ctx, "worktrees")
		parallel(s.ctx, len(wts), sizeWorkers, func(i int) {
			if !wts[i].external { // no internal gain: other volumes are never walked
				join.measured(i, measure(s.ctx, wts[i].path))
			}
		})
	}()
	defer func() { <-sized }()
	defer diagnostics.Recover(s.ctx, "worktrees")

	s.tools = loadToolState(s.ctx, s.env, s.home, wts)

	// 4. git state of every worktree, then a Sizing placeholder.
	parallel(s.ctx, len(wts), gitWorkers, func(i int) {
		w := wts[i]
		s.inspect(w)
		it := s.item(w)
		if w.external {
			s.emit(it)
			return
		}
		ph := it.Clone()
		ph.Sizing = true
		s.emit(ph)
		join.ready(i, it)
	})
	if s.ctx.Err() != nil {
		return s.ctx.Err()
	}

	// 5. stale metadata of main repositories.
	for _, it := range s.pruneItems() {
		s.emit(it)
	}
	<-sized
	return s.ctx.Err()
}

// sizeJoin pairs each worktree's item (built from its git state) with its
// measurement, which run concurrently: whichever comes second applies the
// size and emits the final item.
type sizeJoin struct {
	s     *scan
	wts   []*worktree
	mu    sync.Mutex
	items []*core.Item
	ms    []*measurement
}

func newSizeJoin(s *scan, wts []*worktree) *sizeJoin {
	return &sizeJoin{s: s, wts: wts, items: make([]*core.Item, len(wts)), ms: make([]*measurement, len(wts))}
}

// ready records the item of worktree i (its placeholder was emitted).
func (j *sizeJoin) ready(i int, it *core.Item) {
	j.mu.Lock()
	j.items[i] = it
	m := j.ms[i]
	j.mu.Unlock()
	if m != nil {
		j.finish(i, it, *m)
	}
}

// measured records the measurement of worktree i.
func (j *sizeJoin) measured(i int, m measurement) {
	j.mu.Lock()
	j.ms[i] = &m
	it := j.items[i]
	j.mu.Unlock()
	if it != nil {
		j.finish(i, it, m)
	}
}

func (j *sizeJoin) finish(i int, it *core.Item, m measurement) {
	if j.s.ctx.Err() != nil {
		return // partial measurement
	}
	j.s.applySize(j.wts[i], it, m)
	j.s.emit(it)
}

// snapshot returns the worktrees found so far, sorted by path.
func (s *scan) snapshot() []*worktree {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]*worktree(nil), s.ordered...)
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out
}

// parallel runs fn(0..n-1) on at most workers goroutines, stopping early when
// ctx is cancelled.
func parallel(ctx context.Context, n, workers int, fn func(i int)) {
	if n == 0 {
		return
	}
	next := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < min(workers, n); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer diagnostics.Recover(ctx, "worktrees")
			for i := range next {
				fn(i)
			}
		}()
	}
queue:
	for i := 0; i < n; i++ {
		if ctx.Err() != nil {
			break
		}
		select {
		case next <- i:
		case <-ctx.Done():
			break queue
		}
	}
	close(next)
	wg.Wait()
}
