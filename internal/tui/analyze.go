package tui

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/ludwig-pro/lu-cleaner/internal/clean"
	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
	"github.com/ludwig-pro/lu-cleaner/internal/safety"
	"github.com/ludwig-pro/lu-cleaner/internal/sysx"
)

// anEntry is one child of a listed directory.
type anEntry struct {
	name     string
	path     string
	isDir    bool
	isLink   bool
	size     int64
	files    int64
	newest   time.Time
	sized    bool
	errs     int64
	tag      string
	worktree string // main repo of a linked worktree ("" if not a worktree)
	gitRepo  bool
}

// anDir is a listed directory with its own cursor, so going back is instant.
type anDir struct {
	path    string
	entries []*anEntry
	byName  map[string]*anEntry
	loaded  bool
	err     error
	cursor  int
	offset  int
	curName string
	view    []*anEntry
	dirty   bool
}

type anSort int

const (
	anBySize anSort = iota
	anByName
	anByAge
)

func (s anSort) String() string { return [...]string{"size ↓", "name", "age (oldest first)"}[s] }

type anMode int

const (
	anBrowse anMode = iota
	anHelp
	anConfirm
	anDeleting
	anResult
)

type dirListedMsg struct {
	path    string
	entries []*anEntry
	err     error
}

type deleteDoneMsg struct{ sum *clean.Summary }

type analyzeModel struct {
	opt    AnalyzeOptions
	env    *core.Env
	ctx    context.Context
	cancel context.CancelFunc
	now    time.Time

	cwd     string
	dirs    map[string]*anDir
	sizes   map[string]fsx.Stats
	pending map[string]bool
	pool    *sizePool

	sort       anSort
	showHidden bool
	marked     map[string]*anEntry
	mode       anMode
	confirm    []*anEntry
	confTotal  int64
	input      lineInput
	hint       string
	result     *clean.Summary
	status     string
	statusKind statusKind
	w, h       int
	spin       spinner.Model
	disk       sysx.Disk
	diskErr    error
	quitting   bool
	spinning   bool
	deleting   *deleteRun
	pendingCmd tea.Cmd

	diskFn   func(string) (sysx.Disk, error)
	revealFn func(string) error
	cleanFn  func(context.Context, []*core.Item, clean.Options, func(clean.Result)) *clean.Summary
	listFn   func(string) tea.Msg
}

func newAnalyzer(ctx context.Context, opt AnalyzeOptions) *analyzeModel {
	if opt.Env == nil {
		opt.Env = core.NewEnv()
	}
	env := opt.Env
	if opt.Clean.Home == "" {
		opt.Clean.Home = env.Home
	}
	if opt.Clean.Runner == nil {
		opt.Clean.Runner = env.Runner
	}
	if opt.Clean.Guard == nil {
		roots := append(append([]string(nil), env.Roots...), env.WorktreeRoots...)
		opt.Clean.Guard = safety.New(env.Home, env.TmpDir, roots, env.Exclude)
	}
	now := env.Now
	if now.IsZero() {
		now = time.Now()
	}
	cctx, cancel := context.WithCancel(ctx)
	m := &analyzeModel{
		opt:        opt,
		env:        env,
		ctx:        cctx,
		cancel:     cancel,
		now:        now,
		cwd:        filepath.Clean(opt.Root),
		dirs:       map[string]*anDir{},
		sizes:      map[string]fsx.Stats{},
		pending:    map[string]bool{},
		showHidden: true,
		marked:     map[string]*anEntry{},
		w:          100,
		h:          30,
		spin:       spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(sAccent)),
		diskFn:     sysx.DiskOf,
		revealFn:   revealInFinder,
		cleanFn:    clean.Run,
		listFn:     listDir,
	}
	m.pool = newSizePool(cctx, 4)
	return m
}

func (m *analyzeModel) Init() tea.Cmd {
	m.refreshDisk()
	m.spinning = true
	return tea.Batch(m.enter(m.cwd, ""), waitSizes(m.ctx, m.pool.out), m.spin.Tick)
}

func (m *analyzeModel) needsSpin() bool {
	if len(m.pending) > 0 || m.mode == anDeleting {
		return true
	}
	d := m.cur()
	return d == nil || !d.loaded
}

func (m *analyzeModel) refreshDisk() {
	if m.diskFn != nil {
		m.disk, m.diskErr = m.diskFn(m.cwd)
	}
}

func (m *analyzeModel) setStatus(k statusKind, s string) { m.statusKind, m.status = k, s }

// enter makes path the current directory, listing it if needed. focus is the
// entry name to put the cursor on (when going up).
func (m *analyzeModel) enter(path, focus string) tea.Cmd {
	m.cwd = path
	d := m.dirs[path]
	if d == nil {
		d = &anDir{path: path, curName: focus}
		m.dirs[path] = d
		fn := m.listFn
		return func() tea.Msg { return fn(path) }
	}
	if focus != "" {
		d.curName = focus
		d.dirty = true
	}
	// re-queue children that were never measured (e.g. after a rescan)
	m.queueSizes(d)
	return nil
}

func (m *analyzeModel) cur() *anDir { return m.dirs[m.cwd] }

// listDir reads a directory (in a background command).
func listDir(path string) tea.Msg {
	f, err := os.Open(path)
	if err != nil {
		return dirListedMsg{path: path, err: err}
	}
	names, err := f.Readdirnames(-1)
	f.Close()
	msg := dirListedMsg{path: path, err: err}
	if err != nil && len(names) == 0 {
		return msg
	}
	msg.err = nil
	for _, name := range names {
		p := filepath.Join(path, name)
		fi, err := os.Lstat(p)
		if err != nil {
			continue
		}
		e := &anEntry{name: name, path: p, newest: fi.ModTime()}
		switch {
		case fi.Mode()&os.ModeSymlink != 0:
			e.isLink = true
			e.sized = true
			e.size = allocated(fi)
		case fi.IsDir():
			e.isDir = true
			if gi, err := os.Lstat(filepath.Join(p, ".git")); err == nil {
				if gi.IsDir() {
					e.gitRepo = true
				} else {
					e.worktree = worktreeMain(filepath.Join(p, ".git"))
					if e.worktree == "" {
						e.worktree = "?"
					}
				}
			}
		default:
			e.sized = true
			e.size = allocated(fi)
			e.files = 1
		}
		e.tag = annotate(e)
		msg.entries = append(msg.entries, e)
	}
	return msg
}

func allocated(fi os.FileInfo) int64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Blocks * 512
	}
	return fi.Size()
}

// worktreeMain parses a linked worktree's .git file ("gitdir: <main>/.git/worktrees/<name>")
// and returns the main repository directory.
func worktreeMain(gitFile string) string {
	f, err := os.Open(gitFile)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		return ""
	}
	line := strings.TrimSpace(sc.Text())
	gitdir, ok := strings.CutPrefix(line, "gitdir:")
	if !ok {
		return ""
	}
	gitdir = strings.TrimSpace(gitdir)
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(filepath.Dir(gitFile), gitdir)
	}
	gitdir = filepath.Clean(gitdir)
	// <main>/.git/worktrees/<name>
	wt := filepath.Dir(gitdir)
	if filepath.Base(wt) != "worktrees" {
		return ""
	}
	dotgit := filepath.Dir(wt)
	if filepath.Base(dotgit) != ".git" {
		return ""
	}
	return filepath.Dir(dotgit)
}

var knownDirs = map[string]string{
	"node_modules":      "📦 node_modules — regenerable",
	"Pods":              "📦 CocoaPods — regenerable (pod install)",
	"DerivedData":       "🔨 Xcode build data — regenerable",
	"Archives":          "🔨 Xcode archives",
	"iOS DeviceSupport": "🔨 device symbols — regenerable",
	"CoreSimulator":     "📱 iOS simulators",
	".gradle":           "🤖 Gradle — caches regenerable",
	".android":          "🤖 Android emulators & settings",
	"system-images":     "🤖 Android system images",
	"ndk":               "🤖 Android NDK versions",
	".expo":             "📦 Expo cache — regenerable",
	".next":             "🏗 Next.js build — regenerable",
	".turbo":            "🏗 Turborepo cache — regenerable",
	".parcel-cache":     "🏗 Parcel cache — regenerable",
	".cxx":              "🏗 native build — regenerable",
	"build":             "🏗 build output — usually regenerable",
	"dist":              "🏗 build output — usually regenerable",
	"coverage":          "🏗 test coverage — regenerable",
	".cache":            "🗄 cache",
	"Caches":            "🗄 caches — mostly regenerable",
	"Logs":              "🗄 logs",
	".npm":              "📦 npm cache",
	".yarn":             "📦 yarn",
	".pnpm-store":       "📦 pnpm store (hardlinked)",
	".bun":              "📦 bun",
	".nvm":              "🟨 node versions",
	".cocoapods":        "📦 CocoaPods specs",
	".claude":           "🧠 Claude Code data",
	".codex":            "🧠 Codex data",
	".cursor":           "🧠 Cursor data",
	"worktrees":         "🌳 worktrees",
	".worktrees":        "🌳 worktrees",
	".Trash":            "🗑 Trash — empty it to free space",
}

func annotate(e *anEntry) string {
	if !e.isDir {
		return ""
	}
	if e.worktree != "" {
		return "🌳 linked worktree"
	}
	if t, ok := knownDirs[e.name]; ok {
		return t
	}
	if e.gitRepo {
		return "git repo"
	}
	return ""
}

// ------------------------------------------------------------------ sizes

func (m *analyzeModel) queueSizes(d *anDir) {
	var paths []string
	for _, e := range d.entries {
		if !e.isDir || e.isLink {
			continue
		}
		if st, ok := m.sizes[e.path]; ok {
			m.fill(e, st)
			continue
		}
		if !m.pending[e.path] {
			m.pending[e.path] = true
			paths = append(paths, e.path)
		}
	}
	m.pool.push(paths...)
}

func (m *analyzeModel) fill(e *anEntry, st fsx.Stats) {
	e.size, e.files, e.errs, e.sized = st.Bytes, st.Files, st.Errors, true
	if !st.Newest.IsZero() {
		e.newest = st.Newest
	}
}

// setSize stores st for path and updates the entry shown in its parent.
func (m *analyzeModel) setSize(path string, st fsx.Stats) {
	m.sizes[path] = st
	if d := m.dirs[filepath.Dir(path)]; d != nil && d.byName != nil {
		if e := d.byName[filepath.Base(path)]; e != nil && e.path == path {
			m.fill(e, st)
			d.dirty = true
		}
	}
}

func (m *analyzeModel) applySizes(rs []sizeResult) {
	parents := map[string]bool{}
	for _, r := range rs {
		delete(m.pending, r.path)
		if r.err != nil && m.ctx.Err() != nil {
			continue
		}
		m.setSize(r.path, r.st)
		parents[filepath.Dir(r.path)] = true
	}
	for p := range parents {
		m.aggregate(p)
	}
}

// aggregate caches a directory's size from its children once they are all
// measured, so that going above the start directory reuses the work.
func (m *analyzeModel) aggregate(path string) {
	if _, ok := m.sizes[path]; ok || m.pending[path] {
		return
	}
	d := m.dirs[path]
	if d == nil || !d.loaded {
		return
	}
	var st fsx.Stats
	for _, e := range d.entries {
		if !e.sized {
			return
		}
		st.Bytes += e.size
		st.Files += e.files
		st.Errors += e.errs
		if e.newest.After(st.Newest) {
			st.Newest = e.newest
		}
	}
	st.Dirs = 1
	m.setSize(path, st)
}

// total returns the current directory's size, whether every child is
// measured, and how many are.
func (m *analyzeModel) total(d *anDir) (sum, files int64, complete bool, sized int) {
	complete = true
	for _, e := range d.entries {
		if e.sized {
			sum += e.size
			files += e.files
			sized++
		} else {
			complete = false
		}
	}
	if !complete {
		if st, ok := m.sizes[d.path]; ok && st.Bytes > sum {
			return st.Bytes, st.Files, false, sized
		}
	}
	return sum, files, complete, sized
}

// ------------------------------------------------------------------ view cache

func (m *analyzeModel) refreshView(d *anDir) {
	if d == nil || !d.dirty {
		return
	}
	d.dirty = false
	curName := d.curName
	view := d.view[:0]
	for _, e := range d.entries {
		if !m.showHidden && strings.HasPrefix(e.name, ".") {
			continue
		}
		view = append(view, e)
	}
	switch m.sort {
	case anByName:
		sort.SliceStable(view, func(i, j int) bool { return strings.ToLower(view[i].name) < strings.ToLower(view[j].name) })
	case anByAge:
		sort.SliceStable(view, func(i, j int) bool {
			a, b := view[i].newest, view[j].newest
			if a.IsZero() != b.IsZero() {
				return !a.IsZero()
			}
			return a.Before(b)
		})
	default:
		sort.SliceStable(view, func(i, j int) bool {
			a, b := view[i], view[j]
			if a.sized != b.sized {
				return a.sized
			}
			if a.size != b.size {
				return a.size > b.size
			}
			return a.name < b.name
		})
	}
	d.view = view
	d.cursor = clamp(d.cursor, 0, len(view)-1)
	if curName != "" {
		for i, e := range view {
			if e.name == curName {
				d.cursor = i
				break
			}
		}
	}
	if len(view) > 0 {
		d.curName = view[d.cursor].name
	}
}

func (m *analyzeModel) dirtyAll() {
	for _, d := range m.dirs {
		d.dirty = true
	}
}

// ------------------------------------------------------------------ update

func (m *analyzeModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case dirListedMsg:
		m.applyListing(msg)
	case sizeBatchMsg:
		m.applySizes(msg.results)
		cmds = append(cmds, waitSizes(m.ctx, m.pool.out))
	case spinner.TickMsg:
		if m.needsSpin() {
			var c tea.Cmd
			m.spin, c = m.spin.Update(msg)
			cmds = append(cmds, c)
		} else {
			m.spinning = false
		}
	case deleteDoneMsg:
		m.applyDelete(msg.sum)
	case tea.KeyMsg:
		cmds = append(cmds, m.handleKey(msg))
	}
	if m.pendingCmd != nil {
		cmds = append(cmds, m.pendingCmd)
		m.pendingCmd = nil
	}
	if !m.spinning && m.needsSpin() {
		m.spinning = true
		cmds = append(cmds, m.spin.Tick)
	}
	d := m.cur()
	m.refreshView(d)
	m.fixScroll()
	return m, tea.Batch(cmds...)
}

func (m *analyzeModel) applyListing(msg dirListedMsg) {
	d := m.dirs[msg.path]
	if d == nil {
		d = &anDir{path: msg.path}
		m.dirs[msg.path] = d
	}
	d.loaded = true
	d.err = msg.err
	d.entries = msg.entries
	d.byName = make(map[string]*anEntry, len(msg.entries))
	for _, e := range msg.entries {
		d.byName[e.name] = e
		if mk := m.marked[e.path]; mk != nil {
			m.marked[e.path] = e
		}
	}
	d.dirty = true
	m.queueSizes(d)
	m.aggregate(d.path)
}

func (m *analyzeModel) listRows() int { return max(1, m.h-6) }

func (m *analyzeModel) fixScroll() {
	d := m.cur()
	if d == nil {
		return
	}
	d.offset = scrollTo(d.cursor, d.offset, m.listRows(), len(d.view))
}

func (m *analyzeModel) quit() tea.Cmd {
	m.quitting = true
	m.cancel()
	return tea.Quit
}

func (m *analyzeModel) handleKey(k tea.KeyMsg) tea.Cmd {
	if cmds, ok := splitRunes(k, m.handleKey); ok {
		return cmds
	}
	key := k.String()
	switch m.mode {
	case anHelp:
		if key == "ctrl+c" {
			return m.quit()
		}
		m.mode = anBrowse
		return nil
	case anConfirm:
		return m.confirmKey(k)
	case anDeleting:
		if key == "ctrl+c" {
			return m.quit() // RunAnalyze waits for the deletion to stop
		}
		return nil
	case anResult:
		if key == "ctrl+c" {
			return m.quit()
		}
		m.mode = anBrowse
		return nil
	}
	m.status = ""
	d := m.cur()
	m.refreshView(d)
	switch key {
	case "q", "ctrl+c":
		return m.quit()
	case "?":
		m.mode = anHelp
	case "up", "k":
		m.move(-1)
	case "down", "j":
		m.move(1)
	case "pgup", "ctrl+b":
		m.move(-max(1, m.listRows()-1))
	case "pgdown", "ctrl+f", "ctrl+d":
		m.move(max(1, m.listRows()-1))
	case "home", "g":
		m.move(-1 << 30)
	case "end", "G":
		m.move(1 << 30)
	case "right", "l", "enter":
		e := m.curEntry()
		switch {
		case e == nil:
		case e.isLink:
			m.setStatus(stInfo, e.name+" is a symlink — not followed")
		case !e.isDir:
			m.setStatus(stInfo, e.name+" is a file")
		default:
			return m.enter(e.path, "")
		}
	case "left", "h", "backspace":
		if m.cwd == "/" {
			m.setStatus(stInfo, "Already at /")
			return nil
		}
		return m.enter(filepath.Dir(m.cwd), filepath.Base(m.cwd))
	case "esc":
		if len(m.marked) > 0 {
			m.marked = map[string]*anEntry{}
			m.setStatus(stInfo, "Marks cleared")
		}
	case "s":
		m.sort = (m.sort + 1) % 3
		m.dirtyAll()
		m.setStatus(stInfo, "Sorted by "+m.sort.String())
	case ".":
		m.showHidden = !m.showHidden
		m.dirtyAll()
		if m.showHidden {
			m.setStatus(stInfo, "Showing hidden files")
		} else {
			m.setStatus(stInfo, "Hiding hidden files")
		}
	case " ":
		if e := m.curEntry(); e != nil {
			if m.marked[e.path] != nil {
				delete(m.marked, e.path)
			} else {
				m.marked[e.path] = e
			}
			m.move(1)
		}
	case "o":
		if e := m.curEntry(); e != nil {
			if err := m.revealFn(e.path); err != nil {
				m.setStatus(stErr, "open -R failed: "+err.Error())
			} else {
				m.setStatus(stInfo, "Revealed "+m.env.Pretty(e.path)+" in Finder")
			}
		}
	case "r":
		return m.rescan()
	case "d", "x", "delete":
		m.openConfirm()
	}
	return nil
}

func (m *analyzeModel) move(delta int) {
	d := m.cur()
	if d == nil || len(d.view) == 0 {
		return
	}
	d.cursor = clamp(d.cursor+delta, 0, len(d.view)-1)
	d.curName = d.view[d.cursor].name
}

func (m *analyzeModel) curEntry() *anEntry {
	d := m.cur()
	if d == nil || d.cursor < 0 || d.cursor >= len(d.view) {
		return nil
	}
	return d.view[d.cursor]
}

// forget drops cached listings and sizes of path and everything below it.
func (m *analyzeModel) forget(path string) {
	for p := range m.sizes {
		if fsx.Within(p, path) {
			delete(m.sizes, p)
		}
	}
	for p := range m.dirs {
		if fsx.Within(p, path) && p != m.cwd {
			delete(m.dirs, p)
		}
	}
}

func (m *analyzeModel) rescan() tea.Cmd {
	path := m.cwd
	focus := ""
	if d := m.cur(); d != nil {
		focus = d.curName
	}
	m.forget(path)
	delete(m.dirs, path)
	m.setStatus(stInfo, "Rescanning "+m.env.Pretty(path)+"…")
	m.refreshDisk()
	return m.enter(path, focus)
}

// ------------------------------------------------------------------ delete

func (m *analyzeModel) openConfirm() {
	var targets []*anEntry
	if len(m.marked) > 0 {
		for _, e := range m.marked {
			targets = append(targets, e)
		}
		sort.Slice(targets, func(i, j int) bool { return targets[i].path < targets[j].path })
	} else if e := m.curEntry(); e != nil {
		targets = []*anEntry{e}
	}
	if len(targets) == 0 {
		return
	}
	var total int64
	items := m.itemsFor(targets)
	total = core.Total(items)
	m.confirm = targets
	m.confTotal = total
	m.input = lineInput{active: true}
	m.hint = ""
	m.mode = anConfirm
}

// itemsFor builds the synthetic items handed to clean.Run: everything goes
// through the safety guard like any other deletion.
func (m *analyzeModel) itemsFor(es []*anEntry) []*core.Item {
	items := make([]*core.Item, 0, len(es))
	for _, e := range es {
		it := &core.Item{
			ID:         "analyze:" + e.path,
			Provider:   "analyze",
			Kind:       "analyze",
			Name:       e.name,
			Path:       e.path,
			Size:       e.size,
			Files:      e.files,
			LastUsed:   e.newest,
			Risk:       core.RiskCaution,
			Method:     core.MethodDelete,
			Selectable: true,
		}
		if e.worktree != "" && e.worktree != "?" {
			// a linked worktree: let git remove it (refuses dirty/unpushed ones)
			it.Method = core.MethodWorktree
			it.Project = e.worktree
			it.Kind = "worktree"
		}
		items = append(items, it)
	}
	return items
}

func (m *analyzeModel) confirmKey(k tea.KeyMsg) tea.Cmd {
	switch k.String() {
	case "ctrl+c":
		return m.quit()
	case "esc":
		m.mode = anBrowse
		m.confirm = nil
		m.setStatus(stInfo, "Cancelled — nothing was touched")
	case "enter":
		if strings.EqualFold(strings.TrimSpace(m.input.String()), "yes") {
			return m.startDelete()
		}
		m.hint = "type yes (then enter) to confirm, esc to cancel"
	default:
		m.input.handle(k)
		m.hint = ""
	}
	return nil
}

func (m *analyzeModel) startDelete() tea.Cmd {
	items := m.itemsFor(m.confirm)
	m.mode = anDeleting
	fn, opts, ctx := m.cleanFn, m.opt.Clean, m.ctx
	run := &deleteRun{done: make(chan struct{})}
	m.deleting = run
	go func() {
		defer close(run.done)
		run.sum = safeClean(fn, ctx, items, opts, nil)
	}()
	return func() tea.Msg {
		<-run.done
		return deleteDoneMsg{sum: run.sum}
	}
}

// deleteRun is a deletion running in the background.
type deleteRun struct {
	done chan struct{}
	sum  *clean.Summary
}

// waitDelete blocks until a running deletion ends (the program is exiting).
func (m *analyzeModel) waitDelete() {
	if m.deleting == nil {
		return
	}
	m.cancel()
	<-m.deleting.done
}

func (m *analyzeModel) applyDelete(sum *clean.Summary) {
	m.deleting = nil
	m.result = sum
	m.mode = anResult
	m.confirm = nil
	if sum == nil {
		return
	}
	for _, r := range sum.Results {
		if r.Status != clean.StatusDone || r.Item == nil {
			continue
		}
		p := r.Item.Path
		delete(m.marked, p)
		// drop the entry from its parent listing
		if d := m.dirs[filepath.Dir(p)]; d != nil {
			keep := d.entries[:0]
			for _, e := range d.entries {
				if e.path != p {
					keep = append(keep, e)
				}
			}
			d.entries = keep
			delete(d.byName, filepath.Base(p))
			d.dirty = true
		}
		// ancestors shrink by what was freed
		freed, files := r.Item.Size, r.Item.Files
		m.forget(p)
		for a := filepath.Dir(p); ; a = filepath.Dir(a) {
			if st, ok := m.sizes[a]; ok {
				st.Bytes = max(0, st.Bytes-freed)
				st.Files = max(0, st.Files-files)
				m.setSize(a, st)
			}
			if a == "/" || a == filepath.Dir(a) {
				break
			}
		}
	}
	// the current directory may have been inside a deleted path
	if !fsx.IsDir(m.cwd) {
		p := m.cwd
		for p != "/" && !fsx.IsDir(p) {
			p = filepath.Dir(p)
		}
		m.pendingCmd = m.enter(p, "")
	}
	m.refreshDisk()
}
