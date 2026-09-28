// Package android scans the Android toolchain of a React Native / Expo
// developer: emulators (AVDs) and their Quick Boot snapshots, SDK packages
// (system images, NDKs, build-tools, platforms, CMake...), Gradle user home
// (wrapper distributions, per-version caches, daemon logs, temp files),
// Android Studio caches/logs/old settings and installed JDKs.
//
// The SDK and the AVDs often live on an external SSD behind symlinks
// (~/Library/Android/sdk, ~/.android/avd) that may be unmounted: anything
// whose real location is not on the home volume is reported, never deleted,
// and dangling symlinks are never touched.
package android

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
	"github.com/ludwig-pro/lu-cleaner/internal/sysx"
	"golang.org/x/sys/unix"
)

// keepLatest is the number of newest versions always kept for versioned SDK
// packages (build-tools, platforms, cmake, NDK). Mirrors config.KeepLatest
// (default 1), which is not passed to providers yet.
const keepLatest = 1

// Provider implements core.Provider.
//
// The unexported fields are seams for tests; New leaves them nil and Scan
// fills sane defaults.
type Provider struct {
	getenv       func(string) string            // os.Getenv
	arm64        func() bool                    // Apple Silicon host?
	running      func(names ...string) []string // sysx.Running
	devOf        func(path string) (uint64, error)
	appDirs      []string // where "Android Studio*.app" bundles are looked up
	systemJVMDir string   // /Library/Java/JavaVirtualMachines
	extraSDKs    []string // SDK locations outside home (Homebrew android-commandlinetools)
	mdfind       bool     // ask Spotlight for Android Studio bundles
}

// New returns the provider.
func New() *Provider { return &Provider{mdfind: true} }

func (p *Provider) ID() string    { return "android" }
func (p *Provider) Title() string { return "Android emulators, SDK & Gradle" }
func (p *Provider) Categories() []core.Category {
	return []core.Category{core.CatAndroid}
}

func (p *Provider) defaults(env *core.Env) {
	if p.getenv == nil {
		p.getenv = os.Getenv
	}
	if p.arm64 == nil {
		p.arm64 = isAppleSilicon
	}
	if p.running == nil {
		p.running = sysx.Running
	}
	if p.devOf == nil {
		p.devOf = statDev
	}
	if p.appDirs == nil {
		p.appDirs = []string{"/Applications", filepath.Join(env.Home, "Applications")}
	}
	if p.systemJVMDir == "" {
		p.systemJVMDir = "/Library/Java/JavaVirtualMachines"
	}
	if p.extraSDKs == nil {
		p.extraSDKs = []string{
			"/opt/homebrew/share/android-commandlinetools",
			"/usr/local/share/android-commandlinetools",
		}
	}
}

func isAppleSilicon() bool {
	if v, err := unix.SysctlUint32("hw.optional.arm64"); err == nil {
		return v == 1
	}
	return runtime.GOARCH == "arm64"
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
	p.defaults(env)
	s := newScan(ctx, p, env, emit)

	// Independent of project attribution: run while projects are walked.
	var early sync.WaitGroup
	for _, f := range []func(){s.studio, s.userCache, s.jdks} {
		early.Add(1)
		go func(f func()) { defer early.Done(); f() }(f)
	}

	pi := s.scanProjects()
	if ctx.Err() == nil {
		sdks := s.findSDKs(pi)
		avds := s.scanAVDs()
		s.emitAVDs(avds, sdks)
		for _, sdk := range sdks {
			if ctx.Err() != nil {
				break
			}
			s.sdk(sdk, pi, avds)
		}
		s.gradle(pi)
	}
	early.Wait()
	s.wg.Wait()
	return ctx.Err()
}

// scan holds the state of one Scan call.
type scan struct {
	p     *Provider
	ctx   context.Context
	env   *core.Env
	emit  core.Emit
	wg    sync.WaitGroup
	sem   chan struct{}
	arm64 bool

	homeReal string
	homeDev  uint64

	shellOnce sync.Once
	shellVars map[string][]string

	studioOnce    sync.Once
	studioRunning bool
}

func newScan(ctx context.Context, p *Provider, env *core.Env, emit core.Emit) *scan {
	s := &scan{p: p, ctx: ctx, env: env, emit: emit, sem: make(chan struct{}, 6), arm64: p.arm64()}
	s.homeReal = env.Home
	if r, err := filepath.EvalSymlinks(env.Home); err == nil {
		s.homeReal = r
	}
	s.homeDev, _ = p.devOf(s.homeReal)
	return s
}

func (s *scan) logf(format string, a ...any) {
	if s.env.Logf != nil {
		s.env.Logf("android: "+format, a...)
	}
}

// now is the reference time of the scan.
func (s *scan) now() time.Time {
	if s.env.Now.IsZero() {
		return time.Now()
	}
	return s.env.Now
}

// isStudioRunning reports (once per scan) whether Android Studio runs.
func (s *scan) isStudioRunning() bool {
	s.studioOnce.Do(func() { s.studioRunning = len(s.p.running(studioProcess)) > 0 })
	return s.studioRunning
}

// ------------------------------------------------------------------ emission

func itemID(kind, path string) string { return "android:" + kind + ":" + path }

// base returns an item pre-filled with the provider defaults.
func (s *scan) base(kind, name string, risk core.Risk) *core.Item {
	return &core.Item{
		Provider:   "android",
		Category:   core.CatAndroid,
		Kind:       kind,
		Name:       name,
		Risk:       risk,
		Method:     core.MethodDelete,
		Selectable: true,
		Meta:       map[string]string{},
	}
}

// allowed reports whether every filesystem target of it may be proposed.
func (s *scan) allowed(it *core.Item) bool {
	for _, t := range it.Targets() {
		if s.env.Excluded(t) {
			return false
		}
		if it.Method.Cleanable() && s.env.IsProtected(t) {
			return false
		}
	}
	return true
}

type sizeOpt struct {
	// lastUsedFromNewest sets LastUsed to the newest mtime found while sizing
	// when it is more recent (pure caches whose writes are the usage signal).
	lastUsedFromNewest bool
}

// add emits it immediately with Sizing=true, then measures `measure` (or the
// item targets) in the background and emits the measured version (same ID).
func (s *scan) add(it *core.Item, measure []string, opt sizeOpt) {
	if it.ID == "" {
		it.ID = itemID(it.Kind, it.Where())
	}
	if !s.allowed(it) {
		return
	}
	if measure == nil {
		measure = it.Targets()
	}
	if len(it.Meta) == 0 {
		it.Meta = nil
	}
	it.Sizing = true
	s.emit(it.Clone())
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		select {
		case s.sem <- struct{}{}:
		case <-s.ctx.Done():
			return
		}
		defer func() { <-s.sem }()
		var size, reclaim, files int64
		var newest time.Time
		for _, m := range measure {
			st, err := fsx.Size(s.ctx, m, nil)
			if err != nil && s.ctx.Err() != nil {
				return
			}
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
		if opt.lastUsedFromNewest && newest.After(it.LastUsed) {
			it.LastUsed = newest
		}
		if it.Size == 0 {
			it.Selectable = false
			it.Recommended = false
		}
		s.emit(it)
	}()
}

// report emits an informational item without measuring it.
func (s *scan) report(it *core.Item) {
	if it.ID == "" {
		it.ID = itemID(it.Kind, it.Where())
	}
	it.Method = core.MethodReport
	it.Selectable = false
	it.Recommended = false
	if len(it.Meta) == 0 {
		it.Meta = nil
	}
	if !s.allowed(it) {
		return
	}
	s.emit(it)
}

// ------------------------------------------------------------------ helpers

// dirNames lists the sub-directory names of dir (symlinks excluded), sorted.
func dirNames(dir string) []string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}

// readSmall reads at most 512 KB of a text file.
func readSmall(p string) string {
	f, err := os.Open(p)
	if err != nil {
		return ""
	}
	defer f.Close()
	b, _ := io.ReadAll(io.LimitReader(f, 512<<10))
	return string(b)
}

func uniqSorted(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sortStrings(out)
	return out
}
