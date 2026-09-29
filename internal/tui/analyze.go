package tui

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
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
	name   string
	path   string
	isDir  bool
	isLink bool
	size   int64
	files  int64
	newest time.Time
	sized  bool
	errs   int64
	tag    string
	git    gitInfo // repository / linked worktree / other checkout (directories only)
	// inGit says why the entry is git data the analyzer never deletes: it
	// lies inside a .git directory or a bare repository, or it is a
	// checkout's .git file ("" otherwise).
	inGit string
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

// nestedCheckMsg carries the result of the search for git repositories
// inside the plain folders of the delete dialog (see openConfirm).
type nestedCheckMsg struct {
	seq   int
	found map[string]string // entry path → a repository or worktree found inside it
	errs  map[string]error  // entry path → why it could not be fully inspected
}

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

	sort        anSort
	showHidden  bool
	marked      map[string]*anEntry
	mode        anMode
	confirm     []*anEntry        // entries the confirmation dialog deletes
	confRefused []*anEntry        // marked entries left out: git repositories, checkouts, git data, folders holding them
	confWhy     map[string]string // path → why a confRefused entry is left out
	confNote    map[string]string // path → note shown next to a confirm entry
	confTotal   int64
	// The folders of the dialog are searched for git repositories in the
	// background: confChecking while it runs, confSeq identifies the current
	// search, confYes records a "yes" typed before it ended (the deletion
	// then starts when it ends, unless an entry was left out).
	confChecking bool
	confSeq      int
	confYes      bool
	input        lineInput
	hint         string
	result       *clean.Summary
	status       string
	statusKind   statusKind
	w, h         int
	spin         spinner.Model
	disk         sysx.Disk
	diskErr      error
	quitting     bool
	spinning     bool
	deleting     *deleteRun
	pendingCmd   tea.Cmd

	diskFn   func(string) (sysx.Disk, error)
	revealFn func(string) error
	cleanFn  func(context.Context, []*core.Item, clean.Options, func(clean.Result)) *clean.Summary
	listFn   func(string) tea.Msg
	nestedFn func(string) (string, error) // nestedRepoIn, replaced in tests
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
		nestedFn:   nestedRepoIn,
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
	if len(m.pending) > 0 || m.mode == anDeleting || (m.mode == anConfirm && m.confChecking) {
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
	if fsx.AppDataProtected(path) {
		// another app's container: opening it would block on a macOS permission prompt
		return dirListedMsg{path: path, err: fsx.ErrNeedsFullDiskAccess}
	}
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
	// everything inside a .git directory or a bare repository is git data
	inGit := gitDataRefusal(path)
	for _, name := range names {
		p := filepath.Join(path, name)
		if fsx.AppDataProtected(p) {
			// never stat another app's container without Full Disk Access
			e := &anEntry{name: name, path: p, isDir: true, sized: true, errs: 1, tag: "🔒 app container — needs Full Disk Access", inGit: inGit}
			msg.entries = append(msg.entries, e)
			continue
		}
		fi, err := os.Lstat(p)
		if err != nil {
			continue
		}
		e := &anEntry{name: name, path: p, newest: fi.ModTime(), inGit: inGit}
		if inGit == "" && !fi.IsDir() {
			e.inGit = dotGitFileRefusal(p)
		}
		switch {
		case fi.Mode()&os.ModeSymlink != 0:
			e.isLink = true
			e.sized = true
			e.size = allocated(fi)
		case fi.IsDir():
			e.isDir = true
			e.git = classifyGit(p)
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
	if e.git.kind == gitNone && e.inGit != "" {
		return "git data"
	}
	if !e.isDir {
		return ""
	}
	if e.git.kind != gitNone {
		// git first: a repository named "build" is not a build output
		return e.git.label
	}
	if t, ok := knownDirs[e.name]; ok {
		return t
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
	case nestedCheckMsg:
		cmds = append(cmds, m.applyNestedCheck(msg))
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
			switch {
			case m.marked[e.path] != nil:
				delete(m.marked, e.path)
			case e.refusal() != "":
				m.setStatus(stWarn, fmt.Sprintf("%s is %s — the analyzer never deletes it", e.name, e.refusal()))
				return nil
			default:
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
		return m.openConfirm()
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

func (m *analyzeModel) openConfirm() tea.Cmd {
	var targets []*anEntry
	if len(m.marked) > 0 {
		for p, e := range m.marked {
			// a mark inside a directory deleted since then is stale
			if _, err := os.Lstat(p); errors.Is(err, fs.ErrNotExist) {
				delete(m.marked, p)
				continue
			}
			targets = append(targets, e)
		}
		sort.Slice(targets, func(i, j int) bool { return targets[i].path < targets[j].path })
	}
	if len(targets) == 0 {
		if e := m.curEntry(); e != nil {
			targets = []*anEntry{e}
		}
	}
	if len(targets) == 0 {
		return nil
	}
	// Repositories, submodules, unverified checkouts and git data are never
	// deleted: they do not even reach the executor. The listing may be old:
	// classify again now (a clone or `git worktree add` may have happened
	// since).
	var ok, refused []*anEntry
	for _, e := range targets {
		if e.isDir && !e.isLink {
			e.git = classifyGit(e.path)
		}
		e.inGit = gitDataEntryRefusal(e.path, e.isDir && !e.isLink)
		e.tag = annotate(e)
		if e.refusal() != "" {
			refused = append(refused, e)
			delete(m.marked, e.path) // it can never be deleted here: do not keep it marked
		} else {
			ok = append(ok, e)
		}
	}
	if len(ok) == 0 {
		e := refused[0]
		msg := fmt.Sprintf("%s is %s — the analyzer never deletes it", e.name, e.refusal())
		if len(refused) > 1 {
			msg = fmt.Sprintf("%s: git repositories, checkouts or git data — the analyzer never deletes them", plural(len(refused), "marked entry"))
		}
		m.setStatus(stWarn, msg)
		return nil
	}
	m.confirm = ok
	m.confRefused = refused
	m.confWhy = map[string]string{}
	m.confNote = map[string]string{}
	for _, e := range refused {
		m.confWhy[e.path] = e.refusal()
	}
	m.updateConfTotal()
	m.input = lineInput{active: true}
	m.hint = ""
	m.mode = anConfirm
	m.confYes = false
	m.confChecking = false
	m.confSeq++

	// A plain folder holding a repository or a worktree would take it along:
	// search them in the background (the walk can be long), the dialog waits.
	var dirs []string
	for _, e := range ok {
		if e.isDir && !e.isLink && !e.isWorktree() {
			dirs = append(dirs, e.path)
		}
	}
	if len(dirs) == 0 || m.nestedFn == nil {
		return nil
	}
	m.confChecking = true
	seq, fn := m.confSeq, m.nestedFn
	return func() tea.Msg {
		msg := nestedCheckMsg{seq: seq, found: map[string]string{}, errs: map[string]error{}}
		for _, d := range dirs {
			found, err := fn(d)
			switch {
			case found != "":
				msg.found[d] = found
			case err != nil:
				msg.errs[d] = err
			}
		}
		return msg
	}
}

func (m *analyzeModel) updateConfTotal() {
	keep, _ := splitTrash(m.itemsFor(m.confirm), m.opt.Clean.Trash)
	m.confTotal = core.Total(keep)
}

// applyNestedCheck leaves out of the dialog the folders holding a git
// repository or worktree, and those that could not be fully inspected
// (unless --force). A "yes" typed during the search starts the deletion
// only when nothing was left out.
func (m *analyzeModel) applyNestedCheck(msg nestedCheckMsg) tea.Cmd {
	if msg.seq != m.confSeq || m.mode != anConfirm || !m.confChecking {
		return nil // stale: the dialog was cancelled or reopened
	}
	m.confChecking = false
	var keep []*anEntry
	left := 0
	for _, e := range m.confirm {
		why := ""
		if repo := msg.found[e.path]; repo != "" {
			why = "contains the git repository or worktree " + m.env.Pretty(repo) + " — move or delete it first"
		} else if err := msg.errs[e.path]; err != nil {
			if m.opt.Clean.Force {
				m.confNote[e.path] = "⚠ unchecked (--force)"
			} else {
				why = "cannot be checked for git repositories inside it (" + errText(err) + ") — --force skips this check"
			}
		}
		if why == "" {
			keep = append(keep, e)
			continue
		}
		left++
		m.confRefused = append(m.confRefused, e)
		m.confWhy[e.path] = why
		delete(m.marked, e.path)
	}
	m.confirm = keep
	if len(keep) == 0 {
		m.mode = anBrowse
		m.confirm = nil
		m.confYes = false
		e := m.confRefused[len(m.confRefused)-1]
		msg := fmt.Sprintf("%s %s — nothing was deleted", e.name, m.confWhy[e.path])
		if len(m.confRefused) > 1 {
			msg = fmt.Sprintf("%s left out (git repositories inside, or git data) — nothing was deleted", plural(len(m.confRefused), "marked entry"))
		}
		m.setStatus(stWarn, msg)
		return nil
	}
	m.updateConfTotal()
	if left > 0 {
		// the dialog changed: the "yes" was given for another list
		m.confYes = false
		m.input = lineInput{active: true}
		m.hint = plural(left, "entry") + " left out (git repository inside) — review, then type yes again"
		return nil
	}
	if m.confYes {
		m.confYes = false
		return m.startDelete()
	}
	return nil
}

// itemsFor builds the synthetic items handed to clean.Run: everything goes
// through the safety guard like any other deletion. A verified linked
// worktree is removed by git from its repository's common dir (git refuses
// dirty or locked ones); any other entry holding git data is report-only.
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
		switch {
		case e.isWorktree():
			it.Method = core.MethodWorktree
			it.Project = e.git.common
			it.Kind = "worktree"
			if e.git.locked {
				it.Meta = map[string]string{"locked": "true"}
			}
		case e.refusal() != "":
			it.Method = core.MethodReport
			it.Selectable = false
			it.Note = e.refusal()
		default:
			// re-checked right before the deletion (and in dry-run): git
			// data, or a repository that appeared inside the folder since
			// the dialog, is refused
			path, dir, force := e.path, e.isDir && !e.isLink, m.opt.Clean.Force
			fn := m.nestedFn
			it.Recheck = func(context.Context) error { return recheckPlain(path, dir, force, fn) }
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
		m.confChecking, m.confYes = false, false
		m.confSeq++ // a search still running is now stale
		m.setStatus(stInfo, "Cancelled — nothing was touched")
	case "enter":
		if strings.EqualFold(strings.TrimSpace(m.input.String()), "yes") {
			if m.confChecking {
				m.confYes = true
				m.hint = "still looking for git repositories inside — the deletion starts when the check ends"
				return nil
			}
			return m.startDelete()
		}
		m.hint = "type yes (then enter) to confirm, esc to cancel"
	default:
		if m.input.handle(k) {
			m.confYes = false // editing withdraws a "yes" given during the search
		}
		m.hint = ""
	}
	return nil
}

func (m *analyzeModel) startDelete() tea.Cmd {
	items := m.itemsFor(m.confirm)
	m.confRefused = nil
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
	trashDir := filepath.Join(m.opt.Clean.Home, ".Trash")
	trashed := false
	for _, r := range sum.Results {
		if r.Status != clean.StatusDone || r.Item == nil {
			continue
		}
		p := r.Item.Path
		// marks on the entry and on anything inside it are gone with it
		for k := range m.marked {
			if fsx.Within(k, p) {
				delete(m.marked, k)
			}
		}
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
		// ancestors shrink by what was freed; a move to the Trash frees
		// nothing, so the ancestors of the Trash keep their size
		moved := movedToTrash(r, sum.Trash)
		trashed = trashed || moved
		freed, files := r.Item.Size, r.Item.Files
		m.forget(p)
		for a := filepath.Dir(p); ; a = filepath.Dir(a) {
			// (case/normalization-insensitive: the root may be typed as /users/x)
			if st, ok := m.sizes[a]; ok && !(moved && safety.Within(trashDir, a)) {
				st.Bytes = max(0, st.Bytes-freed)
				st.Files = max(0, st.Files-files)
				m.setSize(a, st)
			}
			if a == "/" || a == filepath.Dir(a) {
				break
			}
		}
	}
	if trashed {
		m.remeasure(trashDir)
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

// remeasure drops what is cached about path (its size, its listing) and
// measures it again if it is shown in a listed directory.
func (m *analyzeModel) remeasure(path string) {
	m.forget(path)
	delete(m.pending, path)
	d := m.dirs[filepath.Dir(path)]
	if d == nil || d.byName == nil {
		return
	}
	e := d.byName[filepath.Base(path)]
	if e == nil || !e.isDir || e.isLink || e.path != path {
		return
	}
	e.sized = false
	d.dirty = true
	m.pending[path] = true
	m.pool.push(path)
}
