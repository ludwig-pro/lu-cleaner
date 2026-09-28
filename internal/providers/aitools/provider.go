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
	day                = 24 * time.Hour
	claudeSessionAge   = 30 * day // Claude Code prunes at 30 days by default (cleanupPeriodDays)
	codexSessionAge    = 30 * day
	liveGrace          = time.Hour // anything written this recently may belong to a live session
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
}

// New returns the provider.
func New() *Provider {
	return &Provider{
		running: sysx.Running,
		appDirs: []string{"/Applications", "~/Applications", "/Applications/Setapp"},
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

	procOnce sync.Once
	procs    []string // executable paths of running processes

	pinnedOnce sync.Once
	pinned     map[string]bool // rollout paths of pinned Codex threads

	claudeRes *resolver
	cursorRes *resolver
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

// findApp returns the path of the first installed <name>.app, or "".
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
	return ""
}

// processPaths returns the executable paths of the running processes
// (`ps -axo comm=`), fetched once per scan.
func (s *scanner) processPaths() []string {
	s.procOnce.Do(func() {
		ctx, cancel := context.WithTimeout(s.ctx, 3*time.Second)
		defer cancel()
		out, err := s.env.Output(ctx, "", "ps", "-axo", "comm=")
		if err != nil {
			return
		}
		for _, l := range strings.Split(string(out), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				s.procs = append(s.procs, l)
			}
		}
	})
	return s.procs
}

// runningWithin reports whether a running process executable lives in dir.
func (s *scanner) runningWithin(dir string) bool {
	for _, p := range s.processPaths() {
		if filepath.IsAbs(p) && fsx.Within(filepath.Clean(p), dir) {
			return true
		}
	}
	return false
}

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

// missingVolume reports whether p is below /Volumes/<name> and that volume is absent.
func missingVolume(p string) bool {
	if !strings.HasPrefix(p, "/Volumes/") {
		return false
	}
	name := strings.SplitN(strings.TrimPrefix(p, "/Volumes/"), "/", 2)[0]
	if name == "" {
		return false
	}
	_, err := os.Stat(filepath.Join("/Volumes", name))
	return err != nil
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
	if reclaim < size {
		it.Reclaim = reclaim
	}
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
