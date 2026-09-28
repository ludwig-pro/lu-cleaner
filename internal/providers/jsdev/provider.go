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
	s := &scanner{ctx: ctx, env: env, emit: emit, p: p}
	if dev, ok := s.devOf(env.Home); ok {
		s.homeDev, s.homeDevOK = dev, true
	}

	// Phase 1: shared knowledge (processes, project pins, fnm shell links).
	var wg sync.WaitGroup
	for _, f := range []func(){
		func() { s.procs = loadProcs(ctx, env) },
		func() { s.projects = loadProjects(ctx, env, p.maxProjectDirs) },
		s.loadMultishells,
	} {
		wg.Add(1)
		go func(f func()) { defer wg.Done(); f() }(f)
	}
	wg.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}

	// Phase 2: independent scanners, bounded.
	tasks := []func(){
		s.nodeVersions, s.multishells, s.npx, s.pnpm, s.yarnBerry,
		s.expoGo, s.playwright, s.tmpSignatures, s.watchman,
	}
	sem := make(chan struct{}, 4)
	for _, t := range tasks {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(t func()) {
			defer func() { <-sem; wg.Done() }()
			t()
		}(t)
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
