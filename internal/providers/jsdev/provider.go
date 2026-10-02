// Package jsdev provides the "js" scanner: everything of the JavaScript /
// React Native toolchain that needs logic rather than a fixed path (fixed
// paths live in catalog/data_jsdev.go — not data_js.go, which Go would
// only build for GOOS=js):
//
//   - Node versions of nvm, fnm, mise, asdf and volta, keeping the default,
//     the `node` on PATH, running ones and versions pinned by projects;
//   - fnm_multishells symlinks left by dead shells;
//   - npm -g leftovers (.name-XXXXXXXX), npx installs named by package;
//   - pnpm stores per major (+ `pnpm store prune`), Yarn Berry global folder;
//   - Expo Go simulator builds, Playwright browser revisions (.links aware);
//   - $TMPDIR React Native codegen and Vitest dirs (content signatures);
//   - stale watchman watches.
package jsdev

import (
	"context"
	"os"
	"sync"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/scanctl"
	"golang.org/x/sys/unix"
)

// Provider implements core.Provider.
type Provider struct {
	// Test hooks (all defaulted by New).
	getenv      func(string) string
	alive       func(pid int) bool
	bootTime    func() time.Time
	devOf       func(path string) (uint64, bool) // nil = real stat
	sysPrefixes []string                         // global npm prefixes outside $HOME (read-only)
	// maxProjectDirs overrides the project walk budgets (0 = defaults).
	maxProjectDirs int
}

// New returns the provider.
func New() *Provider {
	return &Provider{
		getenv:      os.Getenv,
		alive:       pidAlive,
		bootTime:    bootTime,
		sysPrefixes: []string{"/opt/homebrew/lib/node_modules", "/usr/local/lib/node_modules"},
	}
}

func (p *Provider) ID() string    { return "js" }
func (p *Provider) Title() string { return "Node versions & JS tooling" }
func (p *Provider) Categories() []core.Category {
	return []core.Category{core.CatJS}
}

// Scan emits items. Nothing is ever modified: only directory listings,
// symlink reads, small config files and read-only commands (ps, lsof,
// `pnpm store path`, `watchman --no-spawn watch-list`).
func (p *Provider) Scan(ctx context.Context, env *core.Env, emit core.Emit) error {
	ctx = scanctl.Ensure(ctx)
	s := &scanner{ctx: ctx, env: env, emit: emit, p: p}
	if dev, ok := s.devOf(env.Home); ok {
		s.homeDev, s.homeDevOK = dev, true
	}

	// Phase 1: shared knowledge. Processes are read first (every scanner
	// checks them); the project walk (pins, Plug'n'Play projects, Expo SDKs:
	// up to 190,000 folders) and the fnm multishell entries (tens of
	// thousands of symlinks, 7 s to lstat on a cold disk) are read in the
	// background: only the scanners that need them wait.
	projects, shells := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(projects)
		s.projects = loadProjects(ctx, env, p.maxProjectDirs)
	}()
	go func() {
		defer close(shells)
		s.loadMultishells()
	}()
	defer func() { <-projects; <-shells }()
	s.procs = loadProcs(ctx, env)

	// Phase 2: independent scanners, bounded. Those that wait for the
	// project walk first measure what they will show (warm).
	tasks := []struct {
		run   func()
		warm  func()
		needs []chan struct{}
	}{
		{s.nodeVersions, s.warmNodeVersions, []chan struct{}{projects, shells}},
		{s.multishells, nil, []chan struct{}{shells}},
		{s.npx, nil, nil},
		{s.pnpm, nil, nil},
		{s.yarnBerry, s.warmYarnBerry, []chan struct{}{projects}},
		{s.expoGo, s.warmExpoGo, []chan struct{}{projects}},
		{s.playwright, nil, nil},
		{s.tmpSignatures, nil, nil},
		{s.watchman, nil, nil},
	}
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for _, t := range tasks {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if t.warm != nil {
				t.warm()
			}
			for _, c := range t.needs {
				select {
				case <-c:
				case <-ctx.Done():
					return
				}
			}
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			t.run()
		}()
	}
	wg.Wait()
	return ctx.Err()
}

// pidAlive reports whether a process exists (EPERM = exists, not ours).
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := unix.Kill(pid, 0)
	return err == nil || err == unix.EPERM
}

// bootTime returns the last boot time (zero if unknown).
func bootTime() time.Time {
	tv, err := unix.SysctlTimeval("kern.boottime")
	if err != nil || tv == nil || tv.Sec <= 0 {
		return time.Time{}
	}
	return time.Unix(tv.Sec, int64(tv.Usec)*1000)
}
