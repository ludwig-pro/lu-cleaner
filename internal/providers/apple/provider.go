// Package apple scans Xcode and the iOS simulators: DerivedData per project,
// archives, DeviceSupport symbols, simulator devices / runtimes / device sets,
// XCTest recordings left inside simulators, CoreSimulator caches, stale dyld
// caches, extra Xcode versions and the Metal toolchain.
//
// Fixed, well-known paths (Xcode caches, CocoaPods, SwiftPM, Carthage,
// CoreSimulator logs...) live in the catalog (catalog/data_apple.go).
package apple

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
	"github.com/ludwig-pro/lu-cleaner/internal/safety"
	"github.com/ludwig-pro/lu-cleaner/internal/sysx"
	"golang.org/x/sys/unix"
)

// Provider implements core.Provider.
type Provider struct {
	// running reports which process names are alive (sysx.Running; stubbed in tests).
	running func(names ...string) []string
	// systemRoot prefixes system locations (/Applications, /Library, /System) — tests only.
	systemRoot string
}

// New returns the provider.
func New() *Provider { return &Provider{running: sysx.Running} }

func (p *Provider) ID() string    { return "apple" }
func (p *Provider) Title() string { return "Xcode & iOS simulators" }
func (p *Provider) Categories() []core.Category {
	return []core.Category{core.CatSimulators, core.CatXcode}
}

// Scan emits items. Every part runs concurrently; missing tools or folders
// simply produce no items.
func (p *Provider) Scan(ctx context.Context, env *core.Env, emit core.Emit) error {
	s := newScan(ctx, p, env, emit)
	parts := []func(){
		s.derivedData,
		s.archives,
		s.deviceSupport,
		s.simulators, // devices, runtimes, recordings, logs, caches, orphans, dyld caches
		s.deviceSets,
		s.coreSimulatorCaches,
		s.xcodeApps,
		s.metalToolchain,
	}
	for _, f := range parts {
		s.spawn(f)
	}
	s.wg.Wait()
	if s.panicErr != nil {
		return s.panicErr
	}
	return ctx.Err()
}

// ---------------------------------------------------------------- scan state

type scan struct {
	ctx  context.Context
	p    *Provider
	env  *core.Env
	emit core.Emit

	homeDev  int64
	realHome string
	// tmpAreas are the temporary areas the safety guard lets us delete in
	// (safety.TempAreas: given and resolved spellings).
	tmpAreas []string

	sizeSem chan struct{} // bounds concurrent fsx.Size walks
	wg      sync.WaitGroup

	// running is the set of guarded processes alive when the scan started
	// (taken before we spawn xcodebuild/xcrun ourselves).
	running map[string]bool

	mu       sync.Mutex
	panicErr error // first panic of a background part (the engine only recovers Scan's goroutine)
}

// guardedProcesses are every ProcessGuard name used by this provider.
var guardedProcesses = []string{"Xcode", "xcodebuild", "Simulator"}

func newScan(ctx context.Context, p *Provider, env *core.Env, emit core.Emit) *scan {
	running := p.running
	if running == nil {
		running = sysx.Running
	}
	s := &scan{ctx: ctx, p: p, env: env, emit: emit, homeDev: -1, sizeSem: make(chan struct{}, 4), running: map[string]bool{}}
	for _, n := range running(guardedProcesses...) {
		s.running[n] = true
	}
	s.realHome = filepath.Clean(env.Home)
	if r, err := filepath.EvalSymlinks(env.Home); err == nil {
		s.realHome = r
	}
	var st unix.Stat_t
	if unix.Stat(s.realHome, &st) == nil {
		s.homeDev = int64(st.Dev)
	}
	// Mirror the guard: never the parent of an unset or shared TMPDIR (/tmp
	// would open / and /private).
	s.tmpAreas = safety.TempAreas(env.TmpDir)
	return s
}

// spawn runs f in a goroutine tracked by the scan. A panic is turned into
// the scan error instead of crashing the program.
func (s *scan) spawn(f func()) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				s.mu.Lock()
				if s.panicErr == nil {
					s.panicErr = fmt.Errorf("apple provider: panic: %v\n%s", r, debug.Stack())
				}
				s.mu.Unlock()
			}
		}()
		if s.ctx.Err() == nil {
			f()
		}
	}()
}

// lib returns ~/Library/<rel...>.
func (s *scan) lib(rel ...string) string {
	return filepath.Join(append([]string{s.env.Home, "Library"}, rel...)...)
}

// sys returns a system path (prefixed by systemRoot in tests).
func (s *scan) sys(p string) string {
	if s.p.systemRoot == "" {
		return p
	}
	return filepath.Join(s.p.systemRoot, p)
}

func (s *scan) item(kind string, cat core.Category, key string) *core.Item {
	return &core.Item{
		ID:         "apple:" + kind + ":" + key,
		Provider:   "apple",
		Category:   cat,
		Kind:       kind,
		Selectable: true,
	}
}

// skipPath reports paths that must not even be proposed.
func (s *scan) skipPath(p string) bool {
	return s.env.Excluded(p) || s.env.IsProtected(p)
}

// output runs an external command with a timeout.
func (s *scan) output(timeout time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(s.ctx, timeout)
	defer cancel()
	return s.env.Output(ctx, "", name, args...)
}

func addWarn(it *core.Item, w string) {
	if w == "" {
		return
	}
	if it.Warn == "" {
		it.Warn = w
	} else if !strings.Contains(it.Warn, w) {
		it.Warn += "; " + w
	}
}

func setMeta(it *core.Item, k, v string) {
	if v == "" {
		return
	}
	if it.Meta == nil {
		it.Meta = map[string]string{}
	}
	it.Meta[k] = v
}

// guardWarn flags items whose ProcessGuard processes are running right now.
func (s *scan) guardWarn(it *core.Item) {
	if len(it.ProcessGuard) == 0 {
		return
	}
	var r []string
	for _, n := range it.ProcessGuard {
		if s.running[n] {
			r = append(r, n)
		}
	}
	if len(r) > 0 {
		addWarn(it, strings.Join(r, ", ")+" is running — quit it before cleaning")
	}
}

// ---------------------------------------------------------------- locations

type placement int

const (
	placeInternal placement = iota // on the home volume, inside home or the per-user temp dir
	placeOutside                   // on the home volume but outside home (/Applications, /Library...)
	placeExternal                  // on another volume
	placeMissing                   // dangling symlink, unmounted volume, vanished
)

// place says where p really lives (symlinks resolved).
func (s *scan) place(p string) placement {
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return placeMissing
	}
	var st unix.Stat_t
	if unix.Stat(real, &st) != nil {
		return placeMissing
	}
	if s.homeDev >= 0 && int64(st.Dev) != s.homeDev {
		return placeExternal
	}
	if fsx.Within(real, s.realHome) {
		return placeInternal
	}
	for _, a := range s.tmpAreas {
		if fsx.Within(real, a) {
			return placeInternal
		}
	}
	return placeOutside
}

// applyPlace turns items stored on another volume (or outside the areas the
// safety guard lets us delete) into reports. It returns false when the item
// must be dropped (dangling symlink / missing mount).
func (s *scan) applyPlace(it *core.Item, p string) bool {
	switch s.place(p) {
	case placeMissing:
		return false
	case placeExternal:
		toReport(it, "on external volume — no internal gain")
	case placeOutside:
		if it.Method == core.MethodDelete || it.Method == core.MethodTrash {
			toReport(it, "outside your home folder — lu-cleaner does not delete it")
		}
	}
	return true
}

func toReport(it *core.Item, warn string) {
	it.Method = core.MethodReport
	it.Command = nil
	it.Selectable = false
	it.Recommended = false
	addWarn(it, warn)
}

// ---------------------------------------------------------------- sizing

type measured struct {
	bytes, reclaim, files int64
	newest                time.Time
}

// measure sizes paths (bounded concurrency across the whole scan).
func (s *scan) measure(paths ...string) measured {
	s.sizeSem <- struct{}{}
	defer func() { <-s.sizeSem }()
	var m measured
	for _, p := range paths {
		if s.ctx.Err() != nil {
			break
		}
		st, _ := fsx.Size(s.ctx, p, nil)
		m.bytes += st.Bytes
		m.reclaim += st.Reclaim
		m.files += st.Files
		if st.Newest.After(m.newest) {
			m.newest = st.Newest
		}
	}
	return m
}

func (m measured) apply(it *core.Item) {
	it.Sizing = false
	it.Size, it.Files = m.bytes, m.files
	// A tree fully shared with other files (hardlinks or APFS clones) frees
	// ~nothing: SetReclaim stores it as 1 byte, since 0 means "same as Size".
	it.SetReclaim(m.reclaim)
}

// sizeLater emits it as a placeholder now and measures paths in the
// background; finish (optional) adjusts the item once measured.
func (s *scan) sizeLater(it *core.Item, paths []string, finish func(*core.Item, measured)) {
	it.Sizing = true
	s.emit(it.Clone())
	s.spawn(func() {
		m := s.measure(paths...)
		if s.ctx.Err() != nil {
			return
		}
		m.apply(it)
		if finish != nil {
			finish(it, m)
		}
		s.emit(it)
	})
}

// setTargets sets Path (one target) or Paths + Location (group item).
func setTargets(it *core.Item, paths []string, location string) {
	if len(paths) == 1 {
		it.Path = paths[0]
		return
	}
	it.Paths = paths
	it.Location = location
}

// ---------------------------------------------------------------- misc helpers

// newestMTime returns the newest mtime among paths (missing ones ignored).
func newestMTime(paths ...string) time.Time { return fsx.NewestOf(paths...) }

// childrenNewest returns the newest mtime of dir and its direct children.
func childrenNewest(dir string) time.Time {
	t := fsx.ModTime(dir)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return t
	}
	for _, e := range ents {
		if fi, err := e.Info(); err == nil && fi.ModTime().After(t) {
			t = fi.ModTime()
		}
	}
	return t
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

// days returns d in whole days.
func days(d time.Duration) int { return int(d.Hours() / 24) }

const day = 24 * time.Hour

// logf is a nil-safe env.Logf.
func (s *scan) logf(format string, args ...any) {
	if s.env.Logf != nil {
		s.env.Logf(format, args...)
	}
}
