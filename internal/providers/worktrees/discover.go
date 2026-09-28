package worktrees

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
	"golang.org/x/sys/unix"
	"golang.org/x/text/unicode/norm"
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

	// Classification (filled by resolve, then reconcile).
	orphan  string // why git no longer tracks it ("" = tracked)
	copyOf  string // the admin entry belongs to this other (existing) checkout
	movedAt string // git still records the checkout at this (missing) path
	offline string // main repository on this unmounted volume: state unknown
	// unreadable: the git metadata or the main repository cannot be inspected
	// (EACCES, EPERM from macOS privacy protection, I/O error...). Its state is
	// unknown: report only, never an orphan.
	unreadable string
	// repairAt: the .git file points to a missing place but this existing main
	// repository still lists the checkout (the main repository was renamed or
	// moved). Git still tracks it (maybe locked): `git worktree repair` fixes it.
	repairAt string
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
	envFiles   []string // ignored secret / local files that would be lost
	envErr     string   // why the ignored files could not be listed
	nested     []string // other repositories or worktrees inside the checkout
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
	entries  []listEntry // linked worktrees listed by `git worktree list`
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

// commonDir returns the common git dir of the admin dir gitdir
// (<common>/worktrees/<name>). It reads <gitdir>/commondir, which git always
// writes for a linked worktree: an existing admin dir without it is something
// else (e.g. the gitdir of a submodule checked out under "worktrees/"). Only
// when the admin dir is gone or unreadable (orphan detection) is the common
// dir derived from the path, and it must then look like a git dir. ok is false
// for submodules (and their worktrees) and anything that is not a linked
// worktree.
func commonDir(gitdir string) (string, bool) {
	if strings.Contains(gitdir+"/", "/.git/modules/") {
		return "", false // submodule, or a worktree of a submodule: out of scope
	}
	if filepath.Base(filepath.Dir(gitdir)) != "worktrees" {
		return "", false
	}
	b, err := os.ReadFile(filepath.Join(gitdir, "commondir"))
	if err == nil {
		c := strings.TrimSpace(string(b))
		if c == "" {
			return "", false
		}
		if !filepath.IsAbs(c) {
			c = filepath.Join(gitdir, c)
		}
		c = filepath.Clean(c)
		if strings.Contains(c+"/", "/.git/modules/") {
			return "", false
		}
		return c, true
	}
	if fi, lerr := os.Lstat(gitdir); lerr == nil && fi.IsDir() && errors.Is(err, fs.ErrNotExist) {
		return "", false // live admin dir without commondir: not a linked worktree
	}
	// The admin dir is gone (or unreadable): derive the common dir from the
	// ".../worktrees/<name>" layout.
	c := filepath.Dir(filepath.Dir(gitdir))
	if base := filepath.Base(c); base == ".git" || strings.HasSuffix(base, ".git") || looksBare(c) {
		return c, true
	}
	return "", false
}

// looksBare reports a bare repository layout (HEAD file, objects/ and refs/).
func looksBare(dir string) bool {
	if fi, err := os.Lstat(filepath.Join(dir, "HEAD")); err != nil || !fi.Mode().IsRegular() {
		return false
	}
	for _, sub := range []string{"objects", "refs"} {
		if fi, err := os.Lstat(filepath.Join(dir, sub)); err != nil || !fi.IsDir() {
			return false
		}
	}
	return true
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

// Orphan reasons (Meta "orphan").
const (
	orphanMainGone = "main repository deleted or moved"
	orphanPruned   = "worktree entry pruned from the main repository"
	orphanCopy     = "copy of another worktree"
)

// resolve finds the main repository and classifies orphans, moved, offline
// and unreadable worktrees. A worktree is an orphan only when its admin dir
// (and, for "main repository deleted", its common dir) verifiably does not
// exist: see confirmedMissing.
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

	gone, err := confirmedMissing(w.gitdir)
	if gone || err != nil {
		if v := offlineVolume(w.gitdir); v != "" {
			w.offline = v
			return
		}
	}
	switch {
	case err != nil:
		w.unreadable = "git metadata unreadable (" + errText(err) + ")"
	case gone:
		mainGone, err := confirmedMissing(w.common)
		switch {
		case err != nil:
			w.unreadable = "main repository unreadable (" + errText(err) + ")"
		case mainGone:
			w.orphan = orphanMainGone
		default:
			w.orphan = orphanPruned
		}
	case !isDirFollow(w.gitdir):
		w.unreadable = "git metadata " + w.gitdir + " is not a directory"
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
					w.orphan = orphanCopy
				} else if !fsx.Exists(tracked) {
					w.movedAt = tracked
				}
			}
		}
	}
	if w.unreadable == "" && isDirFollow(w.common) {
		w.repo = s.addRepo(w.main, w.common, w.bare)
	}
}

// reconcile re-attaches orphan-looking checkouts that an existing main
// repository still lists: that main repository was renamed or moved, so the
// checkout's .git file points to the old place. Git still tracks them (maybe
// locked, maybe with uncommitted work): they are not orphans but need
// `git worktree repair`, and are reported only. Runs once every main
// repository has been listed.
func (s *scan) reconcile() {
	s.mu.Lock()
	defer s.mu.Unlock()
	repos := make([]*repo, 0, len(s.repos))
	for _, r := range s.repos {
		repos = append(repos, r)
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].path < repos[j].path })
	for _, r := range repos {
		var admins map[string]string
		for _, e := range r.entries {
			k, ok := keyOf(e.path)
			if !ok {
				continue
			}
			w := s.byKey[k]
			if w == nil || w.orphan == "" {
				continue
			}
			if admins == nil {
				admins = adminDirs(r.common)
			}
			w.repairAt = r.path
			w.orphan, w.copyOf = "", ""
			w.repo, w.main, w.common, w.bare = r, r.path, r.common, r.bare
			if a := admins[e.path]; a != "" {
				w.gitdir = a // its lock file is read from here by inspect
			}
			if e.locked {
				w.locked = true
				if w.lockReason == "" {
					w.lockReason = firstLine(e.reason)
				}
			}
		}
	}
}

// confirmedMissing tells whether p verifiably does not exist: lstat fails
// with ENOENT and the nearest existing ancestor is a directory this process
// can list (symlinks in the ancestor chain must resolve, and not to an
// unmounted volume). Any other outcome — EACCES, EPERM (macOS privacy
// protection, e.g. ~/Documents for a process without access), an unreadable
// ancestor, a dangling symlink to an unplugged disk, an I/O error — returns
// an error: the state of p is unknown and nothing may be removed on the
// assumption that it is gone. (false, nil) means p exists.
func confirmedMissing(p string) (bool, error) {
	p = filepath.Clean(p)
	_, err := os.Lstat(p)
	if err == nil {
		return false, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	for dir := filepath.Dir(p); ; dir = filepath.Dir(dir) {
		fi, err := os.Lstat(dir)
		if errors.Is(err, fs.ErrNotExist) {
			if dir == filepath.Dir(dir) {
				return false, err
			}
			continue
		}
		if err != nil {
			return false, err
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			if fi, err = os.Stat(dir); err != nil {
				return false, fmt.Errorf("%s: symlink target unavailable: %w", dir, err)
			}
		}
		if !fi.IsDir() {
			return false, fmt.Errorf("%s is not a directory", dir)
		}
		if v := offlineVolume(realPath(dir)); v != "" {
			return false, fmt.Errorf("volume %s is not mounted", v)
		}
		f, err := os.Open(dir)
		if err != nil {
			return false, err
		}
		_, err = f.Readdirnames(1)
		f.Close()
		if err != nil && err != io.EOF {
			return false, err
		}
		return true, nil
	}
}

// errText is a short description of a filesystem error.
func errText(err error) string {
	var pe *fs.PathError
	switch {
	case errors.Is(err, fs.ErrPermission):
		return "permission denied — grant the terminal Full Disk Access or fix the permissions"
	case errors.As(err, &pe):
		return pe.Err.Error()
	}
	return err.Error()
}

// pathKey is the comparison form of a path on macOS: APFS is case- and
// Unicode-normalization-insensitive, and tools store paths in either form.
func pathKey(p string) string {
	return norm.NFC.String(strings.ToLower(norm.NFC.String(filepath.Clean(p))))
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
