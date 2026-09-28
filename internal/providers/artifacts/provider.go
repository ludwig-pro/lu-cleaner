// Package artifacts is the npkill/kondo-like scanner of lu-cleaner, React
// Native aware: it walks the project roots and the AI worktree roots for
// build and dependency outputs (node_modules, ios/Pods, ios/build,
// android/app/build, android/.gradle, .cxx, .expo, .next, dist, target,
// .venv, .yarn/cache...) and proposes deleting them one by one, without ever
// touching the checkouts themselves.
//
// Safety model:
//   - a directory only matches a rule when one of its markers exists next to
//     it (package.json next to node_modules, Podfile next to Pods...) — the
//     markers become Item.RequireSibling, re-checked at clean time;
//   - inside a git work tree nothing holding tracked files is ever proposed,
//     and generic names (build, dist, out, target, coverage, vendor/bundle,
//     .yarn/*) must be ignored by git (or be untracked with unambiguous
//     output content such as XCBuildData or CACHEDIR.TAG); outside git they
//     need that content;
//   - symlinks are never followed, other devices are never entered, and
//     items living on another volume than the home are report-only.
//
// LastUsed is the activity of the PROJECT (sources, lockfiles, git index),
// not the artifact's own mtime (builds and installs touch it constantly).
// A generic pass lists the heavy git-ignored folders no rule knows (AI agent
// QA caches, staging copies...) as caution items.
package artifacts

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/config"
	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
	"github.com/ludwig-pro/lu-cleaner/internal/sysx"
	"golang.org/x/sys/unix"
)

const (
	walkWorkers = 16 // concurrent directory reads while walking
	gitWorkers  = 6  // concurrent git work tree inspections
	sizeWorkers = 6  // concurrent artifact size walks (fsx parallelises each walk too)

	defaultMaxDepth = 8
	// idleForRecommend: safe outputs of projects idle for longer are forced
	// into smart select.
	idleForRecommend = 24 * time.Hour
)

// ignoredMin is the residual size (not counting known artifacts inside) from
// which an unknown git-ignored folder is reported (a var for tests).
var ignoredMin int64 = 200 * 1000 * 1000

// Provider implements core.Provider.
type Provider struct {
	// extra returns the config extra_artifacts names (nil = none).
	extra func() []string
	// cwdInside returns the PIDs of processes whose cwd is inside dir.
	cwdInside func(dir string) string
	// devOf returns the device of a path (overridable in tests).
	devOf func(p string) (uint64, bool)
	// extraRoots are home-relative folders that hide build artifacts.
	extraRoots []extraRoot
}

type extraRoot struct{ rel, label string }

// New returns the provider.
func New() *Provider {
	return &Provider{
		extra:     loadExtraArtifacts,
		cwdInside: sysx.CwdInside,
		devOf:     statDev,
		extraRoots: []extraRoot{
			{"conductor/archived-contexts", "conductor-archive"},
			{"Documents/Codex", "codex-chat"},
			{".gemini/antigravity/scratch", "antigravity"},
		},
	}
}

func (p *Provider) ID() string    { return "artifacts" }
func (p *Provider) Title() string { return "Project artifacts" }
func (p *Provider) Categories() []core.Category {
	return []core.Category{core.CatArtifacts}
}

// loadExtraArtifacts reads extra_artifacts from the config file. The CLI
// already validated it; errors simply mean "none".
func loadExtraArtifacts() []string {
	cfg, err := config.Load()
	if err != nil || cfg == nil {
		return nil
	}
	return cfg.ExtraArtifacts
}

func statDev(p string) (uint64, bool) {
	var st unix.Stat_t
	if unix.Stat(p, &st) != nil {
		return 0, false
	}
	return uint64(st.Dev), true
}

// worktreeTools labels the well-known worktree homes (home-relative).
var worktreeTools = []struct{ rel, tool string }{
	{".codex/worktrees", "codex"},
	{".cursor/worktrees", "cursor"},
	{"conductor/workspaces", "conductor"},
	{".claude/worktrees", "claude"},
	{".claude-worktrees", "claude"},
	{".claude-squad/worktrees", "claude-squad"},
	{".superset/worktrees", "superset"},
	{".worktrees", "worktree"},
	{"Library/Application Support/Claude/worktrees", "claude-desktop"},
}

// fileKey identifies a directory (device + inode).
type fileKey struct{ dev, ino uint64 }

// cand is a directory matched by a rule.
type cand struct {
	path    string
	key     fileKey
	rule    *rule
	kind    string
	parent  string // directory holding the markers
	root    *scanRoot
	git     *gitRoot
	tool    string
	depth   int
	content map[string]bool // entries of the artifact (when read)

	contentOK     bool
	tracked       int8 // -1 unknown, 0 no, 1 yes
	trackedSample string
	ignored       int8 // -1 unknown, 0 no, 1 yes

	project, pkg string
	placeholder  bool
	accepted     bool
	item         *core.Item
}

// ignDir is a heavy git-ignored folder no rule knows.
type ignDir struct {
	path string
	git  *gitRoot
	root *scanRoot
}

// scan holds the state of one Scan call.
type scan struct {
	ctx  context.Context
	env  *core.Env
	emit core.Emit
	p    *Provider
	rs   *ruleSet
	now  time.Time

	home, homeLibrary string
	homeDev           uint64
	icloud            []string // iCloud-synced folders (Desktop & Documents)
	maxDepth          int
	hasGit            bool
	self              string // our own PID (ignored by the in-use check)
	logf              func(format string, args ...any)
	devOf             func(p string) (uint64, bool)

	roots    []*scanRoot
	rootSet  map[string]*scanRoot
	rootKeys map[fileKey]bool

	mu       sync.Mutex
	cands    map[string]*cand
	candKeys map[fileKey]bool
	order    []*cand
	gits     map[string]*gitRoot
	visited  map[string]bool
	ignDirs  []*ignDir
	ignSeen  map[string]bool
	dirCount atomic.Int64

	projMu   sync.Mutex
	projects map[string]*projInfo
	useMu    sync.Mutex
	inUse    map[string]string
}

// Scan walks every root, verifies candidates with git, then sizes them.
func (p *Provider) Scan(ctx context.Context, env *core.Env, emit core.Emit) error {
	if env == nil || env.Home == "" {
		return nil
	}
	s := p.newScan(ctx, env, emit)
	s.setupRoots()
	if len(s.roots) == 0 {
		return nil
	}
	// 1. walk (placeholders of unambiguous artifacts stream out right away).
	var wg sync.WaitGroup
	for _, r := range s.roots {
		wg.Add(1)
		go func(r *scanRoot) {
			defer wg.Done()
			wc := walkCtx{root: r, tool: r.tool, git: s.findGitAbove(r)}
			if wc.git != nil && wc.git.linked {
				wc.tool = wc.git.tool
			}
			newWalker(s).run(r.path, wc)
		}(r)
	}
	wg.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// 2. git: ignored/tracked state, unknown ignored folders.
	s.processGits()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// 3. decisions, placeholders of the verified generic artifacts, groups.
	items := s.decideAll()
	// 4. sizes.
	s.sizeAll(items)
	// 5. heavy unknown ignored folders.
	s.sizeIgnored()
	return ctx.Err()
}

func (p *Provider) newScan(ctx context.Context, env *core.Env, emit core.Emit) *scan {
	var extra []string
	if p.extra != nil {
		extra = p.extra()
	}
	s := &scan{
		ctx: ctx, env: env, emit: emit, p: p,
		rs:       newRuleSet(extra),
		now:      env.Now,
		maxDepth: env.MaxDepth,
		rootSet:  map[string]*scanRoot{},
		rootKeys: map[fileKey]bool{},
		cands:    map[string]*cand{},
		candKeys: map[fileKey]bool{},
		gits:     map[string]*gitRoot{},
		visited:  map[string]bool{},
		ignSeen:  map[string]bool{},
		projects: map[string]*projInfo{},
		inUse:    map[string]string{},
		self:     itoa(os.Getpid()),
	}
	if s.now.IsZero() {
		s.now = time.Now()
	}
	if s.maxDepth <= 0 {
		s.maxDepth = defaultMaxDepth
	}
	s.logf = env.Logf
	if s.logf == nil {
		s.logf = func(string, ...any) {}
	}
	s.devOf = p.devOf
	if s.devOf == nil {
		s.devOf = statDev
	}
	s.home = realPath(env.Home)
	s.homeLibrary = filepath.Join(s.home, "Library")
	s.homeDev, _ = s.devOf(s.home)
	s.hasGit = env.Runner != nil && env.Has("git")
	cloud := filepath.Join(s.home, "Library", "Mobile Documents", "com~apple~CloudDocs")
	for _, d := range []string{"Documents", "Desktop"} {
		if fi, err := os.Lstat(filepath.Join(cloud, d)); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			s.icloud = append(s.icloud, filepath.Join(s.home, d))
		}
	}
	return s
}

// setupRoots resolves, deduplicates and labels the roots: worktree roots
// first (their tool labels win), then project roots, then extra roots.
func (s *scan) setupRoots() {
	add := func(p, tool, label string, extra bool) {
		p = realPath(p)
		if p == "" || p == "/" || s.env.Excluded(p) || !realDir(p) {
			return
		}
		if _, ok := s.rootSet[p]; ok {
			return
		}
		key, ok := keyOf(p)
		if !ok || s.rootKeys[key] {
			return // same folder under another name (~/code and ~/Code...)
		}
		if extra {
			for _, r := range s.roots {
				if fsx.Within(p, r.path) {
					return // already walked as part of a root
				}
			}
		}
		r := &scanRoot{path: p, tool: tool, label: label, extra: extra, dev: key.dev}
		dev, ok := s.devOf(p)
		if !ok {
			return
		}
		r.external = s.homeDev != 0 && dev != s.homeDev
		r.icloud = s.inICloud(p)
		s.roots = append(s.roots, r)
		s.rootSet[p] = r
		s.rootKeys[key] = true
	}
	for _, w := range s.env.WorktreeRoots {
		add(w, s.toolOf(w), "", false)
	}
	for _, r := range s.env.Roots {
		add(r, "", "", false)
	}
	for _, x := range s.p.extraRoots {
		add(filepath.Join(s.home, x.rel), "", x.label, true)
	}
	// A root inside another one is walked on its own (the outer walk skips it).
	sort.SliceStable(s.roots, func(i, j int) bool { return s.roots[i].path < s.roots[j].path })
}

// toolOf labels a worktree root.
func (s *scan) toolOf(p string) string {
	rp := realPath(p)
	for _, t := range worktreeTools {
		if rp == filepath.Join(s.home, t.rel) || filepath.Clean(p) == filepath.Join(s.env.Home, t.rel) {
			return t.tool
		}
	}
	return "worktree"
}

func (s *scan) inICloud(p string) bool {
	for _, d := range s.icloud {
		if fsx.Within(p, d) {
			return true
		}
	}
	return false
}

// addCand registers a candidate (deduplicated by path and inode) and streams
// a Sizing placeholder for unambiguous rules.
func (s *scan) addCand(c *cand) {
	if s.env.Excluded(c.path) || s.env.IsProtected(c.path) {
		return
	}
	c.tracked, c.ignored = -1, -1
	s.mu.Lock()
	if s.cands[c.path] != nil || s.candKeys[c.key] {
		s.mu.Unlock()
		return
	}
	s.cands[c.path] = c
	s.candKeys[c.key] = true
	s.order = append(s.order, c)
	s.mu.Unlock()
	if c.git != nil {
		c.git.mu.Lock()
		c.git.cands = append(c.git.cands, c)
		c.git.mu.Unlock()
	}
	if !c.rule.Generic && !c.rule.Group && !c.root.external {
		it := s.baseItem(c)
		ph := it.Clone()
		ph.Sizing = true
		c.placeholder = true
		s.emit(ph)
	}
}

// ---------------------------------------------------------------- git phase

func (s *scan) processGits() {
	for s.ctx.Err() == nil {
		s.mu.Lock()
		var pending []*gitRoot
		for _, g := range s.gits {
			if !g.processed {
				g.processed = true
				pending = append(pending, g)
			}
		}
		s.mu.Unlock()
		if len(pending) == 0 {
			return
		}
		sort.Slice(pending, func(i, j int) bool { return pending[i].path < pending[j].path })
		parallel(s.ctx, len(pending), gitWorkers, func(i int) { s.processGit(pending[i]) })
	}
}

func (s *scan) processGit(g *gitRoot) {
	if !s.hasGit {
		return
	}
	discover := !g.root.extra && !g.root.external && !g.root.icloud && !s.inICloud(g.path)
	if discover {
		s.listIgnored(g)
	}
	if g.listed {
		for _, rel := range g.outer {
			s.considerIgnored(g, rel)
		}
	}
	g.mu.Lock()
	cs := append([]*cand(nil), g.cands...)
	g.mu.Unlock()
	if len(cs) == 0 {
		return
	}
	if !g.checked {
		var rels []string
		for _, c := range cs {
			if r := g.rel(c.path); r != "" {
				rels = append(rels, r)
			}
		}
		s.checkIgnore(g, rels)
	}
	s.trackedCands(g, cs)
}

// ignoredSkip are ignored folders never reported: AI tool / editor state,
// worktree homes, VCS metadata.
var ignoredSkip = map[string]bool{
	".git": true, ".hg": true, ".svn": true, ".jj": true,
	".claude": true, ".cursor": true, ".codex": true, ".conductor": true, ".gemini": true, ".windsurf": true,
	".continue": true, ".superset": true, ".vscode": true, ".idea": true, ".fleet": true, ".zed": true,
	".github": true, ".husky": true, ".devcontainer": true, ".direnv": true, ".env": true,
	"worktrees": true, ".worktrees": true,
}

// considerIgnored records an outermost ignored folder of g unless a rule,
// a worktree or a protection covers it. Folders the walk never entered
// (hidden ones) are walked now so the artifacts inside them are found.
func (s *scan) considerIgnored(g *gitRoot, rel string) {
	for _, part := range strings.Split(rel, "/") {
		if ignoredSkip[part] {
			return
		}
	}
	p := filepath.Join(g.path, rel)
	if !fsx.Within(p, g.root.path) || p == g.root.path || s.env.Excluded(p) || s.env.IsProtected(p) {
		return
	}
	var st unix.Stat_t
	if unix.Lstat(p, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR || uint64(st.Dev) != g.root.dev {
		return
	}
	s.mu.Lock()
	covered := s.ignSeen[p]
	for d := p; !covered && fsx.Within(d, g.path) && d != g.path; d = filepath.Dir(d) {
		if s.cands[d] != nil {
			covered = true
		}
	}
	for _, r := range s.roots {
		if fsx.Within(r.path, p) {
			covered = true
		}
	}
	if !covered {
		s.ignSeen[p] = true
	}
	s.mu.Unlock()
	if covered {
		return
	}
	s.mu.Lock()
	s.ignDirs = append(s.ignDirs, &ignDir{path: p, git: g, root: g.root})
	s.mu.Unlock()
	if !s.wasVisited(p) {
		depth := g.depth + strings.Count(rel, "/") + 1
		if depth < s.maxDepth {
			tool := g.tool
			if tool == "" {
				tool = g.root.tool
			}
			newWalker(s).run(p, walkCtx{root: g.root, git: g, tool: tool, depth: depth})
		}
	}
}

// ---------------------------------------------------------------- decisions

// decideAll applies the git / content rules to every candidate. Rejected
// candidates that already showed a placeholder are replaced by an
// explanation; accepted generic ones get their placeholder now. It returns
// the items to size (groups included).
func (s *scan) decideAll() []*sizeJob {
	s.mu.Lock()
	cs := append([]*cand(nil), s.order...)
	s.mu.Unlock()
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].path < cs[j].path })

	var jobs []*sizeJob
	groups := map[string]*sizeJob{}
	var groupOrder []string
	for _, c := range cs {
		if c.git != nil && c.git.checked {
			if r := c.git.rel(c.path); r != "" {
				c.ignored = c.git.isIgnored(r)
			}
		}
		ok, why := s.decide(c)
		if !ok {
			if c.placeholder {
				s.emit(s.rejected(c, why))
			}
			continue
		}
		c.accepted = true
		if c.root.external {
			s.emit(s.externalItem(c))
			continue
		}
		if c.rule.Group {
			project, _ := s.projectOf(c)
			key := c.kind + "\x00" + project
			j := groups[key]
			if j == nil {
				j = &sizeJob{}
				groups[key] = j
				groupOrder = append(groupOrder, key)
			}
			j.cands = append(j.cands, c)
			continue
		}
		it := s.baseItem(c)
		if !c.placeholder {
			ph := it.Clone()
			ph.Sizing = true
			s.emit(ph)
		}
		jobs = append(jobs, &sizeJob{cands: []*cand{c}, item: it})
	}
	for _, k := range groupOrder {
		j := groups[k]
		j.item = s.groupItem(j.cands)
		ph := j.item.Clone()
		ph.Sizing = true
		s.emit(ph)
		jobs = append(jobs, j)
	}
	return jobs
}

// decide applies the safety rules to one candidate.
func (s *scan) decide(c *cand) (bool, string) {
	r := c.rule
	if c.tracked == 1 {
		return false, "contains files tracked by git (" + c.trackedSample + ")"
	}
	if !r.Generic {
		return true, ""
	}
	if c.git != nil { // inside a git work tree
		if c.tracked != 0 {
			return false, "git state unknown"
		}
		if c.ignored == 1 {
			return true, ""
		}
		if r.ContentBeatsIgnore && c.contentOK {
			return true, ""
		}
		return false, "not ignored by git"
	}
	if c.contentOK || r.FreeOutsideGit {
		return true, ""
	}
	return false, "no build output inside"
}

// ---------------------------------------------------------------- sizing

type sizeJob struct {
	cands []*cand
	item  *core.Item
}

func (s *scan) sizeAll(jobs []*sizeJob) {
	parallel(s.ctx, len(jobs), sizeWorkers, func(i int) {
		j := jobs[i]
		it := j.item
		var total, reclaim, files, apparent int64
		for _, c := range j.cands {
			st, err := fsx.Size(s.ctx, c.path, nil)
			if err != nil && s.ctx.Err() != nil {
				return
			}
			total += st.Bytes
			reclaim += st.Reclaim
			files += st.Files
			apparent += st.Apparent
		}
		s.finish(j.cands[0], it, total, reclaim, files, apparent)
		for _, c := range j.cands {
			c.item = it
		}
		s.emit(it)
	})
}

// sizeIgnored measures the heavy unknown ignored folders: the residual (known
// artifacts and nested checkouts excluded) decides whether they are shown.
func (s *scan) sizeIgnored() {
	s.mu.Lock()
	dirs := append([]*ignDir(nil), s.ignDirs...)
	s.mu.Unlock()
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].path < dirs[j].path })
	parallel(s.ctx, len(dirs), sizeWorkers, func(i int) {
		d := dirs[i]
		s.mu.Lock()
		c := s.cands[d.path]
		s.mu.Unlock()
		if c != nil {
			return // identified by content while walking it (virtualenv, CACHEDIR.TAG)
		}
		nested, gits := s.inside(d.path)
		skip := map[string]bool{}
		for _, c := range nested {
			skip[c.path] = true
		}
		for _, g := range gits {
			skip[g.path] = true
		}
		st, err := fsx.Size(s.ctx, d.path, &fsx.Options{Skip: func(p, _ string) bool { return skip[p] }})
		if err != nil && s.ctx.Err() != nil {
			return
		}
		if st.Bytes < ignoredMin {
			return
		}
		s.emit(s.ignoredItem(d, st, nested, gits))
	})
}

// inside returns the accepted candidates and git work trees below p.
func (s *scan) inside(p string) ([]*cand, []*gitRoot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var cs []*cand
	for _, c := range s.order {
		if c.accepted && c.path != p && fsx.Within(c.path, p) {
			cs = append(cs, c)
		}
	}
	var gs []*gitRoot
	for _, g := range s.gits {
		if fsx.Within(g.path, p) {
			gs = append(gs, g)
		}
	}
	sort.Slice(gs, func(i, j int) bool { return gs[i].path < gs[j].path })
	return cs, gs
}

// ---------------------------------------------------------------- helpers

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
			for i := range next {
				fn(i)
			}
		}()
	}
	for i := 0; i < n; i++ {
		if ctx.Err() != nil {
			break
		}
		next <- i
	}
	close(next)
	wg.Wait()
}

// realPath resolves symlinks when possible, else returns the cleaned path.
func realPath(p string) string {
	if p == "" {
		return ""
	}
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
