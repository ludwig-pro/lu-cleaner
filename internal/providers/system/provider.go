// Package system scans what lives outside projects and outside the mobile /
// AI toolchains: the Trash, containers (Docker, colima, Lima, OrbStack,
// Apple's container CLI), Homebrew, Go / Rust / Ruby toolchains, VS Code,
// Zed and JetBrains IDE state, old installers in ~/Downloads, app updater
// leftovers, iOS device backups and macOS-level facts that explain "why is my
// disk still full" (Time Machine local snapshots, swap, staged updates).
//
// Fixed, well-known cache locations of this domain are catalog entries
// (internal/providers/catalog/data_system.go); this provider handles
// everything that needs logic: parsing tool output (docker, brew, go,
// tmutil), deciding which version is current (VS Code builds and extension
// versions, JetBrains IDE versions, rustup / rbenv), workspace state of
// deleted folders, ages of downloads...
//
// Nothing here ever deletes anything: items are only emitted.
package system

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
	"github.com/ludwig-pro/lu-cleaner/internal/providers/internal/scanio"
	"github.com/ludwig-pro/lu-cleaner/internal/providers/internal/scanmemo"
	"github.com/ludwig-pro/lu-cleaner/internal/scanctl"
	"github.com/ludwig-pro/lu-cleaner/internal/sysx"
	"golang.org/x/sys/unix"
)

// Provider implements core.Provider.
//
// The unexported fields are seams for tests; New leaves them nil and Scan
// fills sane defaults.
type Provider struct {
	appDirs       []string                                     // where .app bundles are looked up
	running       func(names ...string) []string               // sysx.Running
	devOf         func(path string) (uint64, error)            // st_dev of a path (following symlinks)
	getenv        func(string) string                          // os.Getenv
	swapUsage     func() (total, used int64, ok bool)          // sysctl vm.swapusage
	updatesDir    string                                       // /Library/Updates ("-" disables)
	brewSystemEnv string                                       // /etc/homebrew/brew.env ("-" disables)
	mdfind        bool                                         // ask Spotlight for app bundles
	only          map[string]bool                              // test seam: run only these parts (nil = all)
	lastUsed      func(path string) (time.Time, bool)          // Finder "last opened" date of a file
	added         func(path string, st *unix.Stat_t) time.Time // when a file arrived in its folder
}

// New returns the provider.
func New() *Provider { return &Provider{mdfind: true} }

func (p *Provider) ID() string    { return "system" }
func (p *Provider) Title() string { return "System, containers & toolchains" }
func (p *Provider) Categories() []core.Category {
	return []core.Category{core.CatSystem, core.CatContainers, core.CatLangs, core.CatIDE}
}

func (p *Provider) defaults(env *core.Env) {
	if p.appDirs == nil {
		p.appDirs = []string{"/Applications", filepath.Join(env.Home, "Applications"), "/Applications/Setapp"}
	}
	if p.devOf == nil {
		p.devOf = statDev
	}
	if p.getenv == nil {
		p.getenv = os.Getenv
	}
	if p.swapUsage == nil {
		p.swapUsage = swapUsage
	}
	if p.updatesDir == "" {
		p.updatesDir = "/Library/Updates"
	}
	if p.brewSystemEnv == "" {
		p.brewSystemEnv = "/etc/homebrew/brew.env"
	}
	if p.lastUsed == nil {
		p.lastUsed = finderLastUsed
	}
	if p.added == nil {
		p.added = fileAdded
	}
}

func statDev(p string) (uint64, error) {
	var st unix.Stat_t
	if err := unix.Stat(p, &st); err != nil {
		return 0, err
	}
	return uint64(st.Dev), nil
}

// Scan emits items. It never deletes anything.
func (p *Provider) Scan(ctx context.Context, env *core.Env, emit core.Emit) error {
	ctx = scanctl.Ensure(ctx)
	p.defaults(env)
	s := &scan{
		p: p, ctx: ctx, env: env, emit: emit,
		now:     env.Now,
		sizeSem: make(chan struct{}, 6),
		runOnce: map[string]*runResult{},
	}
	if s.now.IsZero() {
		s.now = time.Now()
	}
	s.homeReal = env.Home
	if r, err := filepath.EvalSymlinks(env.Home); err == nil {
		s.homeReal = r
	}
	s.homeDev, _ = p.devOf(s.homeReal)

	parts := []struct {
		name string
		fn   func()
	}{
		{"trash", s.trash},
		{"snapshots", s.snapshots},
		{"downloads", s.downloads},
		{"updaters", s.updaters},
		{"backups", s.deviceBackups},
		{"macos", s.macosInfo},
		{"docker", s.docker},
		{"vms", s.vms},
		{"homebrew", s.homebrew},
		{"go", s.golang},
		{"rust", s.rustup},
		{"ruby", s.rbenv},
		{"vscode", s.vscode},
		{"zed", s.zed},
		{"jetbrains", s.jetbrains},
	}
	partSem := make(chan struct{}, 6)
	var partsWG sync.WaitGroup
	for _, part := range parts {
		if p.only != nil && !p.only[part.name] {
			continue
		}
		if ctx.Err() != nil {
			break
		}
		partsWG.Add(1)
		go func(fn func()) {
			defer partsWG.Done()
			select {
			case partSem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-partSem }()
			fn()
		}(part.fn)
	}
	partsWG.Wait()
	s.wg.Wait()
	return ctx.Err()
}

// scan holds the state of one Scan call.
type scan struct {
	p    *Provider
	ctx  context.Context
	env  *core.Env
	emit core.Emit
	now  time.Time

	homeReal string
	homeDev  uint64

	wg      sync.WaitGroup
	sizeSem chan struct{}

	runMu   sync.Mutex
	runOnce map[string]*runResult
}

type runResult struct {
	once scanmemo.Once
	out  []byte
	err  error
}

func (s *scan) logf(format string, a ...any) {
	if s.env.Logf != nil {
		s.env.Logf("system: "+format, a...)
	}
}

// home joins rel (slash separated) to the home directory.
func (s *scan) home(rel string) string { return filepath.Join(s.env.Home, filepath.FromSlash(rel)) }

// appSupport returns ~/Library/Application Support/<rel>.
func (s *scan) appSupport(rel string) string {
	return filepath.Join(s.env.Home, "Library", "Application Support", filepath.FromSlash(rel))
}

// run executes an external command with a timeout. Results are memoized per
// scan (several parts may ask the same thing).
func (s *scan) run(timeout time.Duration, name string, args ...string) ([]byte, error) {
	return s.runIn("", timeout, name, args...)
}

// runIn is run with a working directory ("" = lu-cleaner's current one).
func (s *scan) runIn(dir string, timeout time.Duration, name string, args ...string) ([]byte, error) {
	key := dir + "\x00" + name + "\x00" + strings.Join(args, "\x00")
	s.runMu.Lock()
	r := s.runOnce[key]
	if r == nil {
		r = &runResult{}
		s.runOnce[key] = r
	}
	s.runMu.Unlock()
	if err := r.once.Do(s.ctx, func() {
		r.out, r.err = s.env.OutputTimeout(s.ctx, timeout, dir, name, args...)
		if r.err != nil {
			s.logf("%s %s: %v", name, strings.Join(args, " "), r.err)
		}
	}); err != nil {
		return nil, err
	}
	return r.out, r.err
}

// has reports whether a binary is on PATH.
func (s *scan) has(name string) bool { return s.env.Has(name) }

// ------------------------------------------------------------------ apps

// findApp returns the path of the first "<name>.app" bundle found in the
// application folders ("" when absent).
func (s *scan) findApp(names ...string) string {
	for _, d := range s.p.appDirs {
		for _, n := range names {
			p := filepath.Join(d, n+".app")
			if fi, err := os.Stat(p); err == nil && fi.IsDir() {
				return p
			}
		}
	}
	return ""
}

// appInstalled reports whether an app is installed: bundle in the usual
// folders, else (when enabled) a Spotlight lookup of its bundle id.
func (s *scan) appInstalled(bundleID string, names ...string) bool {
	if s.findApp(names...) != "" {
		return true
	}
	if !s.p.mdfind || bundleID == "" || !s.has("mdfind") {
		return false
	}
	out, err := s.run(4*time.Second, "mdfind", "kMDItemCFBundleIdentifier == '"+bundleID+"'")
	if err != nil {
		// Unknown: be conservative and answer "installed".
		return true
	}
	return strings.TrimSpace(string(out)) != ""
}

// runningOf returns the running processes among patterns (comma separated,
// "/Visual Studio Code.app/Contents/MacOS/" shown as "Visual Studio Code").
func (s *scan) runningOf(patterns ...string) (string, error) {
	if len(patterns) == 0 {
		return "", nil
	}
	if err := s.ctx.Err(); err != nil {
		return "", err
	}
	var running []string
	if s.p.running != nil {
		running = s.p.running(patterns...)
	} else {
		var err error
		running, err = sysx.RunningContext(s.ctx, patterns...)
		if err != nil {
			return "", err
		}
	}
	var names []string
	for _, r := range running {
		names = append(names, prettyProc(r))
	}
	return strings.Join(names, ", "), nil
}

// prettyProc turns a path pattern into an app name.
func prettyProc(p string) string {
	if i := strings.Index(p, ".app/"); i >= 0 {
		return filepath.Base(p[:i])
	}
	return p
}

// ------------------------------------------------------------------ places

// place tells where a path really lives relative to the home volume.
type place struct {
	Real     string // resolved path
	Exists   bool
	Dangling bool   // behind a broken symlink or an unmounted volume
	External bool   // exists on another volume than the home folder
	Link     bool   // the path itself is a symlink
	Volume   string // "/Volumes/<name>" when the resolved path lives there
}

// locate resolves p (symlinks included) and compares its device with the
// home's. Nothing is followed beyond reading links.
func (s *scan) locate(p string) place {
	pl := place{Real: p}
	fi, err := fsx.Lstat(s.ctx, p)
	if err != nil {
		return pl
	}
	pl.Link = fi.Mode()&os.ModeSymlink != 0
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		if pl.Link {
			pl.Dangling = true
			if t, err := os.Readlink(p); err == nil {
				if !filepath.IsAbs(t) {
					t = filepath.Join(filepath.Dir(p), t)
				}
				pl.Real = filepath.Clean(t)
				pl.Volume = volumeOf(pl.Real)
			}
		}
		return pl
	}
	pl.Real = real
	pl.Volume = volumeOf(real)
	dev, err := s.p.devOf(real)
	if err != nil {
		pl.Dangling = true
		return pl
	}
	pl.Exists = true
	pl.External = dev != s.homeDev
	return pl
}

// volumeOf returns "/Volumes/<name>" for a path below /Volumes.
func volumeOf(p string) string {
	for _, prefix := range []string{"/Volumes/", "/System/Volumes/Data/Volumes/"} {
		if strings.HasPrefix(p, prefix) {
			rest := p[len(prefix):]
			if i := strings.IndexByte(rest, '/'); i >= 0 {
				rest = rest[:i]
			}
			if rest != "" {
				return "/Volumes/" + rest
			}
		}
	}
	return ""
}

const warnExternal = "on external volume — no internal gain"

// ------------------------------------------------------------------ emission

func itemID(kind, where string) string { return "system:" + kind + ":" + where }

// newItem returns an item pre-filled with the provider defaults.
func (s *scan) newItem(kind string, cat core.Category, name string, risk core.Risk) *core.Item {
	return &core.Item{
		Provider:   "system",
		Category:   cat,
		Kind:       kind,
		Name:       name,
		Risk:       risk,
		Method:     core.MethodDelete,
		Selectable: true,
	}
}

type pubOpts struct {
	measure     []string // paths to measure (default: the item targets)
	placeholder bool     // emit a Sizing=true placeholder first (slow measurements)
	newest      bool     // LastUsed = newest mtime found while measuring, when more recent
	keepZero    bool     // keep the item selectable even when it measures 0 bytes
}

// allowedTargets filters the filesystem targets of a cleanable item: paths
// that are excluded by the user or protected by the safety guard are dropped.
// It returns false when nothing is left.
func (s *scan) allowedTargets(it *core.Item) bool {
	if !it.Method.Cleanable() || it.Method == core.MethodCommand {
		return true
	}
	ok := func(p string) bool { return !s.env.Excluded(p) && !s.env.IsProtectedContext(s.ctx, p) }
	if len(it.Paths) > 0 {
		var keep []string
		for _, p := range it.Paths {
			if ok(p) {
				keep = append(keep, p)
			}
		}
		it.Paths = keep
		return len(keep) > 0
	}
	if it.Path != "" {
		return ok(it.Path)
	}
	return false
}

// publish emits it (optionally with a Sizing placeholder first), measures it
// in the background and emits the measured version under the same ID.
func (s *scan) publish(it *core.Item, o pubOpts) {
	if !s.allowedTargets(it) {
		return
	}
	if it.ID == "" {
		it.ID = itemID(it.Kind, it.Where())
	}
	if it.Warn == "" && len(it.ProcessGuard) > 0 {
		r, err := s.runningOf(it.ProcessGuard...)
		if err != nil {
			it.Warn = "process state unavailable — rescan before cleaning"
			it.Recommended = false
		} else if r != "" {
			it.Warn = r + " is running — quit it before cleaning"
		}
	}
	if len(it.Meta) == 0 {
		it.Meta = nil
	}
	measure := o.measure
	if measure == nil {
		measure = it.Targets()
	}
	if o.placeholder {
		ph := it.Clone()
		ph.Sizing = true
		s.emit(ph)
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		select {
		case s.sizeSem <- struct{}{}:
		case <-s.ctx.Done():
			return
		}
		st := s.measure(measure)
		<-s.sizeSem
		if s.ctx.Err() != nil {
			return
		}
		it.Size, it.Files = st.Bytes, st.Files
		it.SetReclaim(st.Reclaim) // 0 would mean "same as Size"

		if o.newest && st.Newest.After(it.LastUsed) {
			it.LastUsed = st.Newest
		}
		if it.Size == 0 && !o.keepZero {
			if !o.placeholder {
				return // nothing to show
			}
			it.Selectable = false
			it.Recommended = false
		}
		s.emit(it)
	}()
}

// report emits an informational item (never cleanable).
func (s *scan) report(it *core.Item, o pubOpts) {
	it.Method = core.MethodReport
	it.Selectable = false
	it.Recommended = false
	o.keepZero = true
	if o.measure == nil {
		o.measure = it.Targets()
		if len(o.measure) == 0 && it.Location != "" && !strings.HasSuffix(it.Location, "…") {
			o.measure = []string{it.Location}
		}
	}
	s.publish(it, o)
}

// emitNow emits an item that needs no measurement.
func (s *scan) emitNow(it *core.Item) {
	if !s.allowedTargets(it) {
		return
	}
	if it.ID == "" {
		it.ID = itemID(it.Kind, it.Where())
	}
	if len(it.Meta) == 0 {
		it.Meta = nil
	}
	s.emit(it)
}

// measure sums fsx.Size over paths (missing ones count 0).
func (s *scan) measure(paths []string) fsx.Stats {
	var total fsx.Stats
	for _, m := range paths {
		if s.ctx.Err() != nil {
			break
		}
		st, _ := fsx.Size(s.ctx, m, nil)
		total.Bytes += st.Bytes
		total.Apparent += st.Apparent
		total.Reclaim += st.Reclaim
		total.Files += st.Files
		total.Dirs += st.Dirs
		total.Errors += st.Errors
		if st.Newest.After(total.Newest) {
			total.Newest = st.Newest
		}
	}
	return total
}

// ------------------------------------------------------------------ fs helpers

type entry struct {
	name  string
	path  string
	dir   bool // real directory (symlinks are not)
	link  bool
	mtime time.Time
}

// list returns the entries of dir (hidden ones only when hidden is true),
// sorted by name.
func list(ctx context.Context, dir string, hidden bool) []entry {
	ents, err := fsx.ReadDir(ctx, dir)
	if err != nil {
		return nil
	}
	paths := make([]string, len(ents))
	for i, e := range ents {
		paths[i] = filepath.Join(dir, e.Name())
	}
	infos, err := scanio.Lstats(ctx, paths)
	if err != nil {
		return nil
	}
	out := make([]entry, 0, len(ents))
	for i, e := range ents {
		if !hidden && strings.HasPrefix(e.Name(), ".") {
			continue
		}
		fi, err := infos[i].File, infos[i].Err
		if err != nil {
			continue
		}
		out = append(out, entry{
			name:  e.Name(),
			path:  filepath.Join(dir, e.Name()),
			dir:   fi.IsDir(),
			link:  fi.Mode()&os.ModeSymlink != 0,
			mtime: fi.ModTime(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// isDir reports whether p is a real directory (not a symlink).
func isDir(p string) bool { return fsx.IsDir(p) }

// exists reports whether p exists (following symlinks).
func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// holdsGitRepo reports whether dir is a git repository or worktree: it
// holds a .git entry (directory, file or symlink), or is a bare repository
// (HEAD + objects + refs). The safety guard refuses those.
func holdsGitRepo(dir string) bool {
	if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
		return true
	}
	for _, n := range []string{"HEAD", "objects", "refs"} {
		if _, err := os.Lstat(filepath.Join(dir, n)); err != nil {
			return false
		}
	}
	return true
}

// permissionDenied reports TCC / permission errors.
func permissionDenied(err error) bool {
	return errors.Is(err, fs.ErrPermission) || errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES)
}

func maxTime(ts ...time.Time) time.Time {
	var t time.Time
	for _, x := range ts {
		if x.After(t) {
			t = x
		}
	}
	return t
}

// newestShallow returns the newest mtime of dir and its direct children.
func newestShallow(ctx context.Context, dir string) time.Time {
	t := fsx.ModTime(dir)
	for _, e := range list(ctx, dir, true) {
		if e.mtime.After(t) {
			t = e.mtime
		}
	}
	return t
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return itoa(n) + " " + many
}

func itoa(n int) string { return strconv.Itoa(n) }
