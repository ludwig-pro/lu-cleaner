// Package aitools provides the "ai" scanner: data left behind by AI coding
// tools (Claude Code, Claude desktop, Codex, Cursor, ChatGPT, Conductor,
// Multica, Antigravity, local models...).
//
// Fixed, well-known cache locations live in the catalog
// (internal/providers/catalog/data_ai.go). This provider handles everything
// that needs logic: transcripts grouped by project / month and age, projects
// whose folder no longer exists (deleted worktrees), versioned binaries where
// only the symlinked one is live, Cursor workspace state of deleted folders,
// superseded extension versions, local model stores...
package aitools

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
	"github.com/ludwig-pro/lu-cleaner/internal/sysx"
	"golang.org/x/sys/unix"
)

const providerID = "ai"

// Age thresholds.
const (
	day              = 24 * time.Hour
	claudeSessionAge = 30 * day // Claude Code prunes at 30 days by default (cleanupPeriodDays)
	codexSessionAge  = 30 * day
	liveGrace        = time.Hour // anything written this recently may belong to a live session
	// orphanRecommendAge: data of a folder that no longer exists is only
	// preselected once untouched for this long. A missing folder may come
	// back (rename, Conductor unarchive, re-created worktree) and the data
	// (transcripts, chats) is never regenerated.
	orphanRecommendAge = 30 * day
	backupMinAge       = 7 * day
	archiveContextAge  = 30 * day
	multicaTaskMinAge  = 7 * day
	transcriptAge      = 30 * day
	recordingAge       = 90 * day
	visualizationAge   = 30 * day
	tempLeftoverMinAge = day
)

// ProcessGuard patterns for each application (see sysx.Running: a plain name
// matches an executable basename exactly and case-sensitively, a pattern with
// "/" matches a substring of the executable path).
var (
	procClaudeDesktop = []string{"Claude"}
	// ChatGPT.app embeds Codex (Codex Framework, codex app-server, codex-code-mode-host).
	procCodex     = []string{"codex", "Codex", "ChatGPT", "codex-code-mode-host"}
	procCursor    = []string{"Cursor"}
	procChatGPT   = []string{"ChatGPT", "ChatGPT Classic", "/ChatGPT Atlas.app/"}
	procConductor = []string{"conductor", "Conductor"}
	procMultica   = []string{"Multica", "multica"}
	procOllama    = []string{"ollama", "Ollama"}
	procLMStudio  = []string{"LM Studio", "/LM Studio.app/"}
	procVoiceInk  = []string{"VoiceInk"}
	procAntigrav  = []string{"Antigravity", "/Antigravity.app/"}
)

// Provider implements core.Provider.
type Provider struct {
	// running reports which of the given process names are running (sysx.Running).
	running func(names ...string) []string
	// appDirs are searched for installed .app bundles ("~/" allowed).
	appDirs []string
	// resolverRoot is where encoded project names are resolved from ("/"; tests override it).
	resolverRoot string
	// execInside reports whether a running process executes a binary located
	// in dir. The real probe is sysx.ExecInside: proc_info(2) gives the main
	// executable of every inspectable process (PROC_PIDPATHINFO, the vnode
	// path, i.e. symlinks resolved) and the files mapped in its address space
	// (loaded libraries, native .node addons); `lsof -d txt` is the fallback
	// when the process table cannot be read natively.
	execInside func(dir string) bool
}

// New returns the provider.
func New() *Provider {
	return &Provider{
		running:    sysx.Running,
		appDirs:    []string{"/Applications", "~/Applications", "/Applications/Setapp"},
		execInside: sysExecInside,
	}
}

func (p *Provider) ID() string    { return providerID }
func (p *Provider) Title() string { return "AI tools data" }
func (p *Provider) Categories() []core.Category {
	return []core.Category{core.CatAI, core.CatIDE}
}

// Scan emits items. Every sub-scanner is independent and silent when its
// tool is absent.
func (p *Provider) Scan(ctx context.Context, env *core.Env, emit core.Emit) error {
	s := newScanner(ctx, p, env, emit)
	jobs := []struct {
		name string
		fn   func()
	}{
		// slowest first (they emit Sizing placeholders early)
		{"multica", s.multicaTaskHomes},
		{"cursor-projects", s.cursorProjects},
		{"cursor-workspaces", s.cursorWorkspaceStorage},
		{"conductor-archives", s.conductorArchivedContexts},
		{"antigravity", s.antigravity},
		{"versions", s.versionedBinaries},
		{"claude-projects", s.claudeProjects},
		{"claude-backups", s.claudeConfigBackups},
		{"codex-sessions", s.codexSessions},
		{"codex-archived", s.codexArchived},
		{"codex-dbs", s.codexDatabases},
		{"codex-visualizations", s.codexVisualizations},
		{"cursor-cached-data", s.cursorCachedData},
		{"cursor-extensions", s.cursorExtensions},
		{"atlas", s.chatgptAtlas},
		{"models", s.localModels},
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 6)
	for _, j := range jobs {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(name string, fn func()) {
			start := time.Now()
			defer func() {
				if r := recover(); r != nil {
					env.Logf("ai: %s panicked: %v\n%s", name, r, debug.Stack())
				}
				env.Logf("ai: %s took %s", name, time.Since(start).Round(time.Millisecond))
				<-sem
				wg.Done()
			}()
			fn()
		}(j.name, j.fn)
	}
	wg.Wait()
	return ctx.Err()
}

// scanner holds the state shared by the sub-scanners of one Scan call.
type scanner struct {
	ctx     context.Context
	p       *Provider
	env     *core.Env
	emit    core.Emit
	now     time.Time
	homeDev int64
	running func(names ...string) []string
	// execInside: see Provider.execInside (never nil).
	execInside func(dir string) bool

	procOnce sync.Once
	procs    []string // executable paths of running processes

	pinnedOnce sync.Once
	pinned     map[string]bool // rollout paths of pinned Codex threads

	claudeRes *resolver
	cursorRes *resolver

	appMu sync.Mutex
	apps  map[string]string // Spotlight lookups of <name>.app ("" = not found)
}

func newScanner(ctx context.Context, p *Provider, env *core.Env, emit core.Emit) *scanner {
	s := &scanner{ctx: ctx, p: p, env: env, emit: emit, now: env.Now}
	if s.now.IsZero() {
		s.now = time.Now()
	}
	s.running = p.running
	if s.running == nil {
		s.running = sysx.Running
	}
	s.execInside = p.execInside
	if s.execInside == nil {
		s.execInside = sysExecInside
	}
	var st unix.Stat_t
	if unix.Stat(env.Home, &st) == nil {
		s.homeDev = int64(st.Dev)
	}
	s.claudeRes = newResolver(claudeEncode)
	s.cursorRes = newResolver(cursorEncode)
	if p.resolverRoot != "" {
		s.claudeRes.root, s.cursorRes.root = p.resolverRoot, p.resolverRoot
	}
	return s
}

// ---------------------------------------------------------------- paths

// home joins rel under the home directory.
func (s *scanner) home(rel string) string { return filepath.Join(s.env.Home, rel) }

// appSupport joins rel under ~/Library/Application Support.
func (s *scanner) appSupport(rel string) string {
	return filepath.Join(s.env.Home, "Library", "Application Support", rel)
}

// usable reports whether p may be scanned / proposed at all.
func (s *scanner) usable(p string) bool {
	return !s.env.Excluded(p) && !s.env.IsProtected(p)
}

// findApp returns the path of the first installed <name>.app, or "". The
// usual application folders are searched first, then LaunchServices'
// Spotlight index (apps installed anywhere else). Results are cached for the
// scan.
func (s *scanner) findApp(names ...string) string {
	for _, d := range s.p.appDirs {
		d = s.env.Expand(d)
		for _, n := range names {
			p := filepath.Join(d, n+".app")
			if fi, err := os.Stat(p); err == nil && fi.IsDir() {
				return p
			}
		}
	}
	for _, n := range names {
		if p := s.spotlightApp(n); p != "" {
			return p
		}
	}
	return ""
}

// spotlightApp asks Spotlight for an application bundle named <name>.app
// outside the Trash ("" when none, or when mdfind is unavailable).
func (s *scanner) spotlightApp(name string) string {
	s.appMu.Lock()
	defer s.appMu.Unlock()
	if p, ok := s.apps[name]; ok {
		return p
	}
	if s.apps == nil {
		s.apps = map[string]string{}
	}
	s.apps[name] = ""
	if strings.ContainsAny(name, `'"\*`) {
		return ""
	}
	ctx, cancel := context.WithTimeout(s.ctx, 3*time.Second)
	defer cancel()
	q := "kMDItemContentType == 'com.apple.application-bundle' && kMDItemFSName == '" + name + ".app'"
	out, err := s.env.Output(ctx, "", "mdfind", q)
	if err != nil {
		return ""
	}
	trash := s.home(".Trash")
	for _, l := range strings.Split(string(out), "\n") {
		l = filepath.Clean(strings.TrimSpace(l))
		if !filepath.IsAbs(l) || fsx.Within(l, trash) || strings.Contains(l, "/.Trashes/") || !isDir(l) {
			continue
		}
		s.apps[name] = l
		return l
	}
	return ""
}

// processPaths returns the absolute executable paths of the running
// processes (`ps -axo comm=`), plus their symlink-resolved form, fetched
// once per scan.
func (s *scanner) processPaths() []string {
	s.procOnce.Do(func() {
		ctx, cancel := context.WithTimeout(s.ctx, 3*time.Second)
		defer cancel()
		out, err := s.env.Output(ctx, "", "ps", "-axo", "comm=")
		if err != nil {
			return
		}
		for _, l := range strings.Split(string(out), "\n") {
			if l = strings.TrimSpace(l); !filepath.IsAbs(l) {
				continue
			}
			l = filepath.Clean(l)
			s.procs = append(s.procs, l)
			if r, err := filepath.EvalSymlinks(l); err == nil && r != l {
				s.procs = append(s.procs, r)
			}
		}
	})
	return s.procs
}

// runningWithin reports whether a running process executable lives in dir.
//
// `ps -o comm` shows the path the binary was exec'd through: for a CLI
// launched via ~/.local/bin/claude (a symlink to versions/<v>) that is the
// symlink, which points to another version after an auto-update. The kernel's
// view (proc_pidpath: the resolved executable vnode) is therefore the
// authority; ps paths are kept as a cheap extra signal.
func (s *scanner) runningWithin(dir string) bool {
	if s.execInside(dir) {
		return true
	}
	for _, p := range s.processPaths() {
		if fsx.Within(p, dir) {
			return true
		}
	}
	return false
}

// sysExecInside is the real execInside.
func sysExecInside(dir string) bool { return sysx.ExecInside(dir) != "" }

// ---------------------------------------------------------------- volumes

type volState int

const (
	volOK       volState = iota // on the home volume
	volExternal                 // real location on another volume or under a missing mount
	volGone                     // missing or dangling symlink: never proposed
)

// where tells on which volume p really lives. Parent symlinks are followed
// by lstat; a symlink p is resolved explicitly.
func (s *scanner) where(p string) volState {
	fi, err := os.Lstat(p)
	if err != nil {
		return volGone
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		real, err := filepath.EvalSymlinks(p)
		if err != nil {
			if underMissingMount(p) {
				return volExternal
			}
			return volGone
		}
		var st unix.Stat_t
		if unix.Stat(real, &st) != nil {
			return volGone
		}
		return s.devState(int64(st.Dev))
	}
	if st, ok := fi.Sys().(*sysStat); ok {
		return s.devState(int64(st.Dev))
	}
	return volOK
}

func (s *scanner) devState(dev int64) volState {
	if s.homeDev != 0 && dev != s.homeDev {
		return volExternal
	}
	return volOK
}

// underMissingMount reports whether the symlink p points below /Volumes/<name>
// while that volume is not mounted.
func underMissingMount(p string) bool {
	t, err := os.Readlink(p)
	if err != nil {
		return false
	}
	if !filepath.IsAbs(t) {
		t = filepath.Join(filepath.Dir(p), t)
	}
	return missingVolume(filepath.Clean(t))
}

// missingVolume reports whether p lives on storage that is not reachable
// right now, so that "p does not exist" proves nothing: below
// /Volumes/<name> (or /System/Volumes/Data/Volumes/<name>) while that volume
// is not mounted, below ~/Library/CloudStorage/<provider> while that
// provider folder is absent (logged out, File Provider disabled), or below
// ~/Library/Mobile Documents/<container> (iCloud Drive) while that container
// is absent (signed out of iCloud).
func missingVolume(p string) bool {
	for _, prefix := range []string{"/Volumes/", "/System/Volumes/Data/Volumes/"} {
		if !strings.HasPrefix(p, prefix) {
			continue
		}
		name := strings.SplitN(strings.TrimPrefix(p, prefix), "/", 2)[0]
		if name == "" {
			return false
		}
		_, err := os.Stat(filepath.Join(prefix, name))
		return err != nil
	}
	for _, cloud := range []string{"/Library/CloudStorage/", "/Library/Mobile Documents/"} {
		i := strings.Index(p, cloud)
		if i <= 0 {
			continue
		}
		name := strings.SplitN(p[i+len(cloud):], "/", 2)[0]
		if name == "" {
			return false
		}
		_, err := os.Stat(p[:i+len(cloud)] + name)
		return err != nil
	}
	return false
}

// ---------------------------------------------------------------- items

func itemID(kind, key string) string { return providerID + ":" + kind + ":" + key }

func (s *scanner) newItem(kind string, cat core.Category, name string, risk core.Risk) *core.Item {
	return &core.Item{
		Provider:   providerID,
		Category:   cat,
		Kind:       kind,
		Name:       name,
		Risk:       risk,
		Method:     core.MethodDelete,
		Selectable: true,
	}
}

type pubOpts struct {
	// placeholder emits the item with Sizing=true before measuring it.
	placeholder bool
	// newest raises LastUsed to the newest mtime found while measuring.
	newest bool
	// minBytes drops the item when it is smaller (only without placeholder).
	minBytes int64
}

// publish validates the targets of it (protection, exclusion, volume), sets
// the running-app warning, measures it and emits it. Items whose targets all
// vanished are dropped; items living on another volume become report-only.
func (s *scanner) publish(it *core.Item, o pubOpts) {
	if s.ctx.Err() != nil {
		return
	}
	raw := it.Targets()
	if len(raw) == 0 {
		return
	}
	var kept []string
	external := false
	for _, t := range raw {
		if !s.usable(t) {
			continue
		}
		switch s.where(t) {
		case volOK:
			kept = append(kept, t)
		case volExternal:
			kept = append(kept, t)
			external = true
		}
	}
	if len(kept) == 0 {
		return
	}
	if len(it.Paths) > 0 {
		it.Paths = kept
		if it.Location == "" {
			it.Location = commonLocation(kept)
		}
	} else {
		it.Path = kept[0]
	}
	if external {
		it.Method = core.MethodReport
		it.Recommended = false
		it.Warn = "on external volume — no internal gain"
	} else if it.Warn == "" && len(it.ProcessGuard) > 0 && it.Method.Cleanable() {
		if r := s.running(it.ProcessGuard...); len(r) > 0 {
			it.Warn = strings.Join(r, ", ") + " is running — quit it before cleaning"
		}
	}
	if o.placeholder {
		c := it.Clone()
		c.Sizing = true
		s.emit(c)
	}
	stats := s.measure(kept, external)
	if s.ctx.Err() != nil {
		return
	}
	var size, reclaim, files int64
	var newest time.Time
	for _, st := range stats {
		size += st.Bytes
		reclaim += st.Reclaim
		files += st.Files
		if st.Newest.After(newest) {
			newest = st.Newest
		}
	}
	it.Sizing = false
	it.Size, it.Files = size, files
	// SetReclaim: a tree fully shared with other files (hardlinks or APFS
	// clones, reclaim 0) must not read as "frees its whole size".
	it.SetReclaim(reclaim)
	if o.newest && newest.After(it.LastUsed) {
		it.LastUsed = newest
	}
	if !o.placeholder && !external && (size == 0 || size < o.minBytes) {
		return
	}
	if size == 0 {
		it.Selectable = false
	}
	s.emit(it)
}

// measure sizes targets concurrently (bounded). External targets are
// resolved and measured across devices so the report shows their real size.
func (s *scanner) measure(targets []string, external bool) []fsx.Stats {
	out := make([]fsx.Stats, len(targets))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, t := range targets {
		if s.ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, t string) {
			defer func() { <-sem; wg.Done() }()
			var opt *fsx.Options
			if external {
				if r, err := filepath.EvalSymlinks(t); err == nil {
					t = r
				}
				opt = &fsx.Options{CrossDevice: true}
			}
			out[i], _ = fsx.Size(s.ctx, t, opt)
		}(i, t)
	}
	wg.Wait()
	return out
}

// commonLocation returns the deepest common directory of paths + "/…".
func commonLocation(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	if len(paths) == 1 {
		return paths[0]
	}
	common := filepath.Dir(paths[0])
	for _, p := range paths[1:] {
		for !fsx.Within(p, common) && common != "/" {
			common = filepath.Dir(common)
		}
	}
	return strings.TrimSuffix(common, "/") + "/…"
}

// ---------------------------------------------------------------- small fs helpers

type entry struct {
	name  string
	path  string
	dir   bool
	mtime time.Time
}

// list returns the entries of dir (symlinks are reported as non-dirs and
// never followed). Hidden entries are skipped unless hidden is true.
func list(dir string, hidden bool) []entry {
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make([]entry, 0, len(des))
	for _, d := range des {
		if !hidden && strings.HasPrefix(d.Name(), ".") {
			continue
		}
		fi, err := d.Info()
		if err != nil {
			continue
		}
		out = append(out, entry{
			name:  d.Name(),
			path:  filepath.Join(dir, d.Name()),
			dir:   d.IsDir(),
			mtime: fi.ModTime(),
		})
	}
	return out
}

// newestShallow returns the newest mtime of p and its direct children.
func newestShallow(p string) time.Time {
	t := fsx.ModTime(p)
	for _, e := range list(p, true) {
		if e.mtime.After(t) {
			t = e.mtime
		}
	}
	return t
}

// newestDeep returns the newest mtime of p and of everything below it
// (symlinks are not followed). ok is false when the tree could not be walked
// completely (unreadable entry, more than maxNewestEntries entries, ctx
// cancelled): callers must then treat the tree as recently used.
func newestDeep(ctx context.Context, p string) (newest time.Time, ok bool) {
	n := 0
	ok = true
	err := filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			ok = false
			return filepath.SkipAll
		}
		if n++; n > maxNewestEntries || ctx.Err() != nil {
			ok = false
			return filepath.SkipAll
		}
		fi, err := d.Info()
		if err != nil {
			ok = false
			return filepath.SkipAll
		}
		if fi.ModTime().After(newest) {
			newest = fi.ModTime()
		}
		return nil
	})
	return newest, ok && err == nil
}

// maxNewestEntries bounds newestDeep (per-project agent data is small).
const maxNewestEntries = 50_000

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// fileExists reports whether p exists (lstat).
func fileExists(p string) bool { return fsx.Exists(p) }

// isDir reports whether p is a directory, following symlinks.
func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
