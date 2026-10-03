package artifacts

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/ludwig-pro/lu-cleaner/internal/diagnostics"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
	"github.com/ludwig-pro/lu-cleaner/internal/providers/internal/scanio"
	"github.com/ludwig-pro/lu-cleaner/internal/scanctl"
	"golang.org/x/sys/unix"
)

// scanRoot is one directory tree walked for artifacts.
type scanRoot struct {
	path     string // real, clean path
	dev      uint64
	tool     string // AI tool owning the worktrees below ("codex", "cursor"...), "" for project roots
	label    string // name prefix of extra roots ("codex-chat"...)
	extra    bool   // extra root: dependency/build rules only, no ignored-dir discovery
	external bool   // on another volume than the home: report only
	icloud   bool   // inside iCloud-synced Desktop/Documents
}

// walkCtx is what a directory inherits from its ancestors.
type walkCtx struct {
	root  *scanRoot
	git   *gitRoot // nearest git work tree at or above the directory
	tool  string   // tool of the enclosing worktree ("" = none)
	depth int      // depth below the root (reset at linked worktrees)
}

// maxWalkDirs bounds a whole scan (safety net against pathological trees).
const maxWalkDirs = 400_000

// pruneNames are never descended into when they did not match a rule: huge
// or opaque trees that never hold projects.
var pruneNames = map[string]bool{
	"node_modules": true, "Pods": true, "site-packages": true, "bower_components": true,
	"DerivedData": true, "__pycache__": true, "xcuserdata": true,
}

// pruneSuffixes are bundle-like directories (apps, frameworks, archives...).
var pruneSuffixes = []string{
	".app", ".appex", ".framework", ".xcframework", ".xcarchive", ".dSYM", ".bundle",
	".photoslibrary", ".noindex", ".xcassets", ".lproj", ".xcodeproj", ".xcworkspace",
	".kext", ".plugin", ".docc", ".imageset", ".appiconset", ".icon", ".sdk", ".pkg",
}

// walker walks the scan roots concurrently (bounded) with os.ReadDir-like
// reads: never follows symlinks, never leaves the root's device, never
// descends into an artifact, .git, hidden folders (except in-repo
// .claude/worktrees and .worktrees), bundles and other roots.
type walker struct {
	s   *scan
	sem chan struct{}
}

func newWalker(s *scan) *walker {
	return &walker{s: s, sem: make(chan struct{}, min(walkWorkers, scanctl.From(s.ctx).Limits().IO))}
}

// runRoots shares the same worker pool across every root. The sentinel keeps
// the task count positive until all initial tasks have been submitted.
func (w *walker) runRoots(roots []*scanRoot) {
	var wg sync.WaitGroup
	wg.Add(1)
	func() {
		defer wg.Done()
		defer diagnostics.Recover(w.s.ctx, "artifacts")
		for _, r := range roots {
			if w.s.ctx.Err() != nil {
				break
			}
			wc := walkCtx{root: r, tool: r.tool, git: w.s.findGitAbove(r)}
			if wc.git != nil && wc.git.linked {
				wc.tool = wc.git.tool
			}
			w.spawn(r.path, wc, &wg)
		}
	}()
	wg.Wait()
}

// run walks an extra tree with the scan's pool. Each invocation owns its task
// count, so concurrent ignored-directory walks never reuse a WaitGroup.
func (w *walker) run(dir string, wc walkCtx) {
	var wg sync.WaitGroup
	wg.Add(1)
	func() { defer wg.Done(); defer diagnostics.Recover(w.s.ctx, "artifacts"); w.spawn(dir, wc, &wg) }()
	wg.Wait()
}

func (w *walker) spawn(dir string, wc walkCtx, wg *sync.WaitGroup) {
	if w.s.ctx.Err() != nil {
		return
	}
	select {
	case w.sem <- struct{}{}:
		wg.Add(1)
		go func() {
			defer func() { <-w.sem; wg.Done() }()
			defer diagnostics.Recover(w.s.ctx, "artifacts")
			w.walk(dir, wc, wg)
		}()
	default:
		w.walk(dir, wc, wg)
	}
}

func (w *walker) walk(dir string, wc walkCtx, wg *sync.WaitGroup) {
	s := w.s
	if s.ctx.Err() != nil || !s.markVisited(dir) {
		return
	}
	entries, err := fsx.ReadDir(s.ctx, dir)
	if err != nil {
		return
	}
	names := make(map[string]bool, len(entries))
	for _, e := range entries {
		names[e.Name()] = true
	}

	// A .git entry makes this directory a git work tree (main repo, linked
	// worktree or submodule).
	if names[".git"] {
		if g := s.addGitRoot(dir, wc); g != nil {
			wc.git = g
			if g.linked {
				wc.tool = g.tool
				wc.depth = 0 // each checkout gets the full depth budget
			}
		}
	}
	// Content-identified artifacts the parent's rules did not catch: a
	// virtualenv of any name, a folder tagged CACHEDIR.TAG. Never walk them.
	// A directory holding a .git entry is a checkout even when addGitRoot
	// could not register it (unreadable or dangling .git file).
	if dir != wc.root.path && (wc.git == nil || dir != wc.git.path) && !names[".git"] {
		var r *rule
		switch {
		case names["pyvenv.cfg"]:
			r = looseVenv
		case names["CACHEDIR.TAG"] && validCacheDirTag(dir):
			r = cacheDirTag
		}
		if r != nil {
			if !wc.root.extra || r.ExtraRoots {
				if key, ok := keyOf(dir); ok && key.dev == wc.root.dev {
					s.addCand(&cand{
						path: dir, key: key, rule: r, kind: r.Kind, parent: filepath.Dir(dir),
						root: wc.root, git: wc.git, tool: wc.tool, depth: wc.depth,
						contentOK: true, content: names,
					})
				}
			}
			return
		}
	}

	var children []string
	for _, e := range entries {
		child := filepath.Join(dir, e.Name())
		if e.IsDir() && e.Name() != ".git" && !s.env.Excluded(child) && !s.isOtherRoot(child, wc.root) {
			children = append(children, child)
		}
	}
	infos, err := scanio.Lstats(s.ctx, children)
	if err != nil {
		return
	}
	metadata := make(map[string]scanio.Info, len(children))
	for i, child := range children {
		metadata[filepath.Base(child)] = infos[i]
	}
	for _, e := range entries {
		if s.ctx.Err() != nil {
			return
		}
		if e.Type()&fs.ModeSymlink != 0 || !e.IsDir() {
			continue
		}
		name := e.Name()
		if name == ".git" {
			continue
		}
		child := filepath.Join(dir, name)
		if s.env.Excluded(child) || s.isOtherRoot(child, wc.root) {
			continue
		}
		info := metadata[name]
		fi, err := info.File, info.Err
		if err != nil || fi == nil {
			continue
		}
		stp, ok := fi.Sys().(*syscall.Stat_t)
		if !ok || !fi.IsDir() {
			continue
		}
		st := stp
		if uint64(st.Dev) != wc.root.dev {
			continue // unreadable, or a mount point: stay on the root's device
		}
		if r := s.rootKeys[fileKey{uint64(st.Dev), st.Ino}]; r != nil && r != wc.root {
			continue // another root spelled differently (~/code vs ~/Code, NFD vs NFC)
		}
		if wc.depth+1 <= s.maxDepth {
			if s.match(dir, names, name, child, fileKey{uint64(st.Dev), st.Ino}, wc) {
				continue // never descend into an artifact
			}
		}
		if wc.depth+1 >= s.maxDepth {
			continue
		}
		next := wc
		next.depth++
		switch {
		case name == ".claude" || name == ".worktrees":
			// In-repo AI / manual worktrees: <repo>/.claude/worktrees/*, <repo>/.worktrees/*.
			sub := child
			tool := "worktree"
			if name == ".claude" {
				sub = filepath.Join(child, "worktrees")
				tool = "claude"
				next.depth++
				if !realDir(sub) {
					continue
				}
			}
			if wc.root.tool == "" && wc.tool == "" {
				next.tool = tool
			}
			w.spawn(sub, next, wg)
			continue
		case strings.HasPrefix(name, "."):
			continue // hidden folders: only known artifact names (matched above)
		case pruneNames[name] || pruned(name):
			continue
		case child == s.homeLibrary || name == ".Trash":
			continue
		}
		if s.dirCount.Add(1) > maxWalkDirs {
			s.logf("artifacts: walk capped at %d directories", maxWalkDirs)
			return
		}
		w.spawn(child, next, wg)
	}
}

func pruned(name string) bool {
	for _, suf := range pruneSuffixes {
		if strings.HasSuffix(name, suf) {
			return true
		}
	}
	return false
}

// match tests the rules against the directory entry name of parent. It
// registers the resulting candidates and reports whether the entry must not
// be descended into.
func (s *scan) match(parent string, parentNames map[string]bool, name, child string, key fileKey, wc walkCtx) bool {
	rs := s.rs.candidates(name)
	if len(rs) == 0 {
		return false
	}
	matched := false
	for _, r := range rs {
		if wc.root.extra && !r.ExtraRoots {
			continue
		}
		if !r.markersOK(parentNames) {
			continue
		}
		target, tkey := child, key
		if r.Sub != "" {
			target = filepath.Join(child, r.Sub)
			var st unix.Stat_t
			if scanctl.DoIO(s.ctx, func() error { return unix.Lstat(target, &st) }) != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR || uint64(st.Dev) != wc.root.dev {
				continue
			}
			tkey = fileKey{uint64(st.Dev), st.Ino}
		}
		// A checkout (a gh-pages branch checked out as dist/ with `git
		// worktree add`, a nested repository, a submodule) is never an
		// artifact: the walk enters it instead (addGitRoot registers it) and
		// only git may remove it (worktrees provider, with its dirty checks).
		if hasGitEntry(target) {
			continue
		}
		var content map[string]bool
		contentOK, strongOK := false, false
		if len(r.Content) > 0 || r.Generic {
			content = dirNames(s.ctx, target)
			_, contentOK = matchAny(r.Content, content)
		}
		if r.NeedContent && !contentOK {
			continue
		}
		if contentOK && len(r.Strong) > 0 && wc.git == nil {
			strongOK = r.strongOutput(s.ctx, target, content) // only decisive outside git
		}
		s.addCand(&cand{
			path: target, key: tkey, rule: r, kind: r.kindFor(target), parent: parent,
			root: wc.root, git: wc.git, tool: wc.tool, depth: wc.depth + 1,
			contentOK: contentOK, strongOK: strongOK, content: content,
		})
		matched = true
		if r.Sub == "" {
			break
		}
	}
	return matched
}

// markVisited records dir as walked; false when it already was.
func (s *scan) markVisited(dir string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.visited[dir] {
		return false
	}
	s.visited[dir] = true
	return true
}

func (s *scan) wasVisited(dir string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.visited[dir]
}

// isOtherRoot reports whether p is another scan root (walked on its own).
func (s *scan) isOtherRoot(p string, cur *scanRoot) bool {
	r, ok := s.rootSet[p]
	return ok && r != cur
}

func keyOf(p string) (fileKey, bool) {
	var st unix.Stat_t
	if unix.Lstat(p, &st) != nil {
		return fileKey{}, false
	}
	return fileKey{uint64(st.Dev), st.Ino}, true
}

func realDir(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.IsDir()
}
