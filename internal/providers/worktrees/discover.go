package worktrees

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
	"golang.org/x/sys/unix"
)

// worktree is one linked worktree found on disk.
type worktree struct {
	path   string // real path of the checkout
	key    fileKey
	gitdir string // admin dir: <common>/worktrees/<name>
	common string // common git dir (<main>/.git, or the bare repo)
	main   string // main working tree, or the bare repository
	bare   bool
	repo   *repo // nil when the main repository is gone

	// Classification (filled by resolve).
	orphan  string // why git no longer tracks it ("" = tracked)
	copyOf  string // the admin entry belongs to this other (existing) checkout
	movedAt string // git still records the checkout at this (missing) path
	offline string // main repository on this unmounted volume: state unknown
	// external: the checkout itself lives on another volume than the home.
	external bool
	outside  bool // outside the areas the safety guard allows (e.g. /private/tmp)

	// Git state (filled by inspect).
	gitOK      bool   // git status succeeded
	gitErr     string // why git state is unknown
	branch     string // short branch name, "" when detached
	detached   bool
	commit     string // abbreviated HEAD
	headDate   time.Time
	upstream   string
	gone       bool // upstream configured but deleted on the remote
	dirty      int
	dirtyPaths []string
	unpushed   int
	unpushedOK bool
	merged     bool
	mergedOK   bool
	locked     bool
	lockReason string
	envFiles   []string // ignored .env files that would be lost
	activity   time.Time

	// Environment (filled by inspect from the tool state).
	tool      string
	inUse     []string // commands whose cwd is inside
	editors   []string // editors with a window on it
	session   string   // active tool session ("active Codex thread"...)
	toolNotes []string // extra facts from the tool state (Meta)
	toolMeta  map[string]string
}

// repo is a main repository (working tree with a .git directory, or bare).
type repo struct {
	path   string // main working tree (or bare repo dir)
	common string // common git dir
	bare   bool

	listed   bool
	prunable []listEntry

	info       sync.Once // guards the fields below (resolved by repoInfo)
	hasRemotes bool
	defRef     string // full ref of the default branch (refs/remotes/origin/main...)
	defName    string // short name (origin/main)
	defRemote  bool   // defRef is a remote-tracking ref
}

// listEntry is one record of `git worktree list --porcelain`.
type listEntry struct {
	path     string
	head     string
	branch   string
	detached bool
	bare     bool
	locked   bool
	prunable bool
	reason   string
}

// fileKey identifies a directory (device + inode) for deduplication.
type fileKey struct{ dev, ino uint64 }

func keyOf(p string) (fileKey, bool) {
	var st unix.Stat_t
	if unix.Stat(p, &st) != nil {
		return fileKey{}, false
	}
	return fileKey{uint64(st.Dev), st.Ino}, true
}

// devOf returns the device of p (overridable in tests).
var devOf = func(p string) (uint64, bool) {
	var st unix.Stat_t
	if unix.Stat(p, &st) != nil {
		return 0, false
	}
	return uint64(st.Dev), true
}

// volumesRoot is where macOS mounts external volumes.
var volumesRoot = "/Volumes"

// realPath resolves symlinks when possible, else returns the cleaned path.
func realPath(p string) string {
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// offlineVolume returns the volume name when p lives under /Volumes/<name>
// and that volume is not mounted (missing, or an empty mount point).
func offlineVolume(p string) string {
	prefix := volumesRoot + "/"
	if !strings.HasPrefix(p, prefix) {
		return ""
	}
	name := strings.SplitN(p[len(prefix):], "/", 2)[0]
	if name == "" {
		return ""
	}
	mp := filepath.Join(volumesRoot, name)
	fi, err := os.Lstat(mp)
	if err != nil {
		return name
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return "" // e.g. "Macintosh HD" -> /
	}
	d1, ok1 := devOf(mp)
	d2, ok2 := devOf(volumesRoot)
	if ok1 && ok2 && d1 == d2 {
		return name // stale mount point directory
	}
	return ""
}

// gitKind tells whether dir holds a .git file (linked worktree / submodule)
// or a .git directory (main repository).
const (
	gitNone = iota
	gitFile
	gitDir
)

func gitKind(dir string) int {
	fi, err := os.Lstat(filepath.Join(dir, ".git"))
	switch {
	case err != nil:
		return gitNone
	case fi.IsDir():
		return gitDir
	case fi.Mode().IsRegular():
		return gitFile
	}
	return gitNone
}

// readGitFile parses "<dir>/.git" ("gitdir: <path>") and returns the
// absolute, cleaned admin dir.
func readGitFile(dir string) (string, bool) {
	f, err := os.Open(filepath.Join(dir, ".git"))
	if err != nil {
		return "", false
	}
	defer f.Close()
	line, err := bufio.NewReader(io.LimitReader(f, 4096)).ReadString('\n')
	if err != nil && line == "" {
		return "", false
	}
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "gitdir:") {
		return "", false
	}
	g := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
	if g == "" {
		return "", false
	}
	if !filepath.IsAbs(g) {
		g = filepath.Join(dir, g)
	}
	return filepath.Clean(g), true
}

// commonDir returns the common git dir of the admin dir gitdir. It reads
// <gitdir>/commondir when present, else derives it from the
// ".../worktrees/<name>" layout. ok is false for submodules and anything
// that is not a linked worktree.
func commonDir(gitdir string) (string, bool) {
	if b, err := os.ReadFile(filepath.Join(gitdir, "commondir")); err == nil {
		c := strings.TrimSpace(string(b))
		if c != "" {
			if !filepath.IsAbs(c) {
				c = filepath.Join(gitdir, c)
			}
			c = filepath.Clean(c)
			if filepath.Base(filepath.Dir(gitdir)) == "worktrees" && !strings.Contains(c, "/modules/") {
				return c, true
			}
			return "", false
		}
	}
	i := strings.LastIndex(gitdir, "/worktrees/")
	if i <= 0 || strings.Contains(gitdir[i+len("/worktrees/"):], "/") {
		return "", false
	}
	c := gitdir[:i]
	if strings.Contains(c, "/modules/") {
		return "", false // worktree of a submodule: out of scope
	}
	return c, true
}

// mainOf returns the main repository of a common git dir.
func mainOf(common string) (main string, bare bool) {
	if filepath.Base(common) == ".git" {
		return filepath.Dir(common), false
	}
	return common, true
}

// ---------------------------------------------------------------- walking

// pruneDirs are never descended into while looking for repositories.
var pruneDirs = map[string]bool{
	"node_modules": true, ".git": true, "Pods": true, "build": true, "DerivedData": true,
	".gradle": true, ".expo": true, ".next": true, "dist": true, ".turbo": true, ".cxx": true,
	"target": true, ".cache": true, ".yarn": true, ".Trash": true, "Library": true,
	"__pycache__": true, ".venv": true, "venv": true,
}

// maxWalkDirs bounds one walk (a pathological root must not stall the scan).
const maxWalkDirs = 200_000

// discover walks the worktree roots and the project roots.
func (s *scan) discover() {
	var mains, wts []string
	seenRoot := map[string]bool{}
	visit := func(dir string, kind int) {
		switch kind {
		case gitDir:
			mains = append(mains, dir)
		case gitFile:
			wts = append(wts, dir)
		}
	}
	for _, r := range s.env.WorktreeRoots {
		if rr := realPath(r); !seenRoot[rr] {
			seenRoot[rr] = true
			s.walk(r, wtRootDepth, false, visit)
		}
	}
	depth := s.env.MaxDepth
	if depth <= 0 {
		depth = 8
	}
	for _, r := range s.env.Roots {
		s.walk(r, depth, true, visit)
	}
	for _, m := range mains {
		s.addMain(m)
	}
	for _, w := range wts {
		s.addCandidate(w)
	}
}

// walk visits root and its subdirectories up to maxDepth, calling visit for
// directories holding a .git file or directory (never descending into them).
// Symlinks are never followed.
func (s *scan) walk(root string, maxDepth int, skipHidden bool, visit func(string, int)) {
	type node struct {
		path  string
		depth int
	}
	root = filepath.Clean(root)
	if !fsx.IsDir(root) && !isDirFollow(root) {
		return
	}
	stack := []node{{root, 0}}
	visited := 0
	for len(stack) > 0 {
		if s.ctx.Err() != nil || visited > maxWalkDirs {
			return
		}
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		visited++
		if s.env.Excluded(n.path) {
			continue
		}
		if k := gitKind(n.path); k != gitNone {
			visit(n.path, k)
			continue
		}
		if n.depth >= maxDepth {
			continue
		}
		entries, err := os.ReadDir(n.path)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() { // symlinks report ModeSymlink, never IsDir
				continue
			}
			name := e.Name()
			if pruneDirs[name] || (skipHidden && strings.HasPrefix(name, ".")) {
				continue
			}
			stack = append(stack, node{filepath.Join(n.path, name), n.depth + 1})
		}
	}
}

func isDirFollow(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// nestedWorktreeDirs are conventional places for worktrees inside (or next
// to) a repository.
func nestedWorktreeDirs(dir string) []string {
	return []string{
		filepath.Join(dir, ".claude", "worktrees"),
		filepath.Join(dir, ".worktrees"),
		filepath.Join(dir, "worktrees"),
		dir + "-worktrees",
	}
}

// scanNested adds the linked worktrees found one level below the
// conventional worktree folders of dir.
func (s *scan) scanNested(dir string) {
	for _, d := range nestedWorktreeDirs(dir) {
		if !fsx.IsDir(d) {
			continue
		}
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			p := filepath.Join(d, e.Name())
			if gitKind(p) == gitFile {
				s.addCandidate(p)
			}
		}
	}
}

// addMain registers a main repository (a directory with a .git directory, or
// a bare repository when bare is set via addRepo).
func (s *scan) addMain(dir string) *repo {
	dir = realPath(dir)
	return s.addRepo(dir, filepath.Join(dir, ".git"), false)
}

func (s *scan) addRepo(main, common string, bare bool) *repo {
	s.mu.Lock()
	if r, ok := s.repos[main]; ok {
		s.mu.Unlock()
		return r
	}
	r := &repo{path: main, common: common, bare: bare}
	s.repos[main] = r
	s.mu.Unlock()
	if !bare {
		s.scanNested(main)
	}
	return r
}

// addCandidate registers the linked worktree at dir (deduplicated).
func (s *scan) addCandidate(dir string) {
	if s.ctx.Err() != nil {
		return
	}
	path := realPath(dir)
	if s.env.Excluded(dir) || s.env.Excluded(path) || s.env.IsProtected(path) || s.env.IsProtected(dir) {
		return
	}
	if gitKind(path) != gitFile {
		return
	}
	gitdir, ok := readGitFile(path)
	if !ok {
		return
	}
	common, ok := commonDir(gitdir)
	if !ok {
		return // submodule or separate git dir: not a linked worktree
	}
	key, ok := keyOf(path)
	if !ok {
		return
	}
	s.mu.Lock()
	if _, dup := s.byKey[key]; dup {
		s.mu.Unlock()
		return
	}
	w := &worktree{path: path, key: key, gitdir: gitdir, common: common}
	s.byKey[key] = w
	s.ordered = append(s.ordered, w)
	s.mu.Unlock()

	s.resolve(w)
	// Worktrees may hold their own nested worktrees (.claude/worktrees...).
	s.scanNested(path)
}

// resolve finds the main repository and classifies orphans, moved and
// offline worktrees.
func (s *scan) resolve(w *worktree) {
	w.main, w.bare = mainOf(w.common)
	if fsx.Exists(w.main) {
		w.main = realPath(w.main)
		w.common = realPath(w.common)
	}

	if d, ok := devOf(w.path); ok {
		if hd, ok := devOf(s.home); ok && d != hd {
			w.external = true
		}
	}
	w.outside = !s.allowedPath(w.path)

	gitdirOK := fsx.IsDir(w.gitdir)
	mainOK := isDirFollow(w.common)
	switch {
	case !gitdirOK:
		if v := offlineVolume(w.gitdir); v != "" {
			w.offline = v
		} else if !mainOK {
			w.orphan = "main repository deleted"
		} else {
			w.orphan = "worktree entry pruned from the main repository"
		}
	default:
		back := readTrim(filepath.Join(w.gitdir, "gitdir"))
		if back != "" {
			if !filepath.IsAbs(back) {
				back = filepath.Join(w.gitdir, back)
			}
			tracked := filepath.Dir(filepath.Clean(back))
			if realPath(tracked) != w.path {
				if k, ok := keyOf(tracked); ok && k != w.key && gitKind(tracked) == gitFile {
					w.copyOf = tracked
					w.orphan = "copy of another worktree"
				} else if !fsx.Exists(tracked) {
					w.movedAt = tracked
				}
			}
		}
	}
	if w.offline == "" && mainOK {
		if w.bare {
			w.repo = s.addRepo(w.main, w.common, true)
		} else {
			w.repo = s.addRepo(w.main, w.common, false)
		}
	}
}

// allowedPath mirrors the safety guard: deletions only below the home or the
// per-user temp area.
func (s *scan) allowedPath(p string) bool {
	for _, a := range s.allowed {
		if a != "" && p != a && fsx.Within(p, a) {
			return true
		}
	}
	return false
}

// readTrim reads a small text file and trims it ("" when missing).
func readTrim(p string) string {
	f, err := os.Open(p)
	if err != nil {
		return ""
	}
	defer f.Close()
	b, _ := io.ReadAll(io.LimitReader(f, 8192))
	return strings.TrimSpace(string(b))
}
