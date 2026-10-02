package artifacts

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
	"golang.org/x/text/unicode/norm"
)

const (
	gitListTimeout  = 20 * time.Second // ls-files --others --ignored per work tree
	gitCheckTimeout = 10 * time.Second // check-ignore / ls-files <paths> / diff-files / untracked
	gitBatch        = 200              // paths per command line
	maxDirtyStat    = 300              // modified / untracked entries stat'ed for the activity
	// untrackedDirBudget bounds the walk of one untracked folder.
	untrackedDirBudget = 300
)

// gitRoot is a git work tree found while walking (main repo, linked
// worktree or submodule).
type gitRoot struct {
	path   string // work tree root
	gitdir string // its git dir (<repo>/.git, <common>/worktrees/<name>, <common>/modules/<x>)
	linked bool   // linked worktree (.git file pointing into .../worktrees/)
	tool   string // tool that created the linked worktree ("codex", "claude", "worktree"...)
	root   *scanRoot
	depth  int // depth of the work tree below its scan root

	mu    sync.Mutex
	cands []*cand

	processed bool
	listed    bool            // ls-files --others --ignored succeeded
	ignored   map[string]bool // ignored directories (rel, no trailing slash)
	outer     []string        // outermost ignored directories (rel)
	checked   bool            // ignore state known for every candidate (listing or check-ignore)
	trackedOK bool            // tracked-files check succeeded
	dirtyOK   bool            // uncommitted files listed: dirty is their activity
	dirty     time.Time       // newest mtime of the modified / untracked files
}

// addGitRoot registers dir (which holds a .git entry) as a work tree. A
// .git entry that cannot be used (garbage or unreadable .git file, dangling
// symlink) still makes dir a checkout: it is registered with an unknown git
// dir, git then fails there (or answers for an enclosing repository), and
// its generic candidates are never judged "outside git".
func (s *scan) addGitRoot(dir string, wc walkCtx) *gitRoot {
	s.mu.Lock()
	if g, ok := s.gits[dir]; ok {
		s.mu.Unlock()
		return g
	}
	s.mu.Unlock()
	g := &gitRoot{path: dir, root: wc.root, depth: wc.depth}
	dotgit := filepath.Join(dir, ".git")
	fi, err := fsx.Lstat(s.ctx, dotgit)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	switch {
	case err != nil:
		// Unreadable: a checkout all the same.
	case fi.IsDir():
		g.gitdir = dotgit
	case fi.Mode()&os.ModeSymlink != 0:
		// git follows a .git symlink to a git dir (repo tool, dotfiles).
		if rd, err := filepath.EvalSymlinks(dotgit); err == nil && realDir(rd) {
			g.gitdir = rd
		}
	case fi.Mode().IsRegular():
		gd := readGitFile(dotgit)
		if gd == "" {
			break
		}
		if !filepath.IsAbs(gd) {
			gd = filepath.Join(dir, gd)
		}
		g.gitdir = filepath.Clean(gd)
		if strings.Contains(filepath.ToSlash(g.gitdir), "/worktrees/") {
			g.linked = true
			g.depth = 0 // the walker gives each checkout the full depth budget
			g.tool = wc.tool
			if g.tool == "" {
				g.tool = wc.root.tool
			}
			if g.tool == "" {
				g.tool = "worktree"
			}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.gits[dir]; ok {
		return old
	}
	s.gits[dir] = g
	return g
}

// readGitFile returns the "gitdir: X" target of a .git file.
func readGitFile(p string) string {
	b, err := readSmall(p, 4096)
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), "gitdir:"); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// findGitAbove looks for a work tree enclosing root (roots may be folders
// inside a repository). The home itself (dotfile repos) never counts.
func (s *scan) findGitAbove(r *scanRoot) *gitRoot {
	for d := filepath.Dir(r.path); d != "/" && d != "." && fsx.Within(d, s.home) && d != s.home; d = filepath.Dir(d) {
		if _, err := fsx.Lstat(s.ctx, filepath.Join(d, ".git")); err == nil {
			return s.addGitRoot(d, walkCtx{root: r, tool: r.tool})
		}
	}
	return nil
}

// rel returns p relative to the work tree ("" when outside).
func (g *gitRoot) rel(p string) string {
	if p == g.path || !fsx.Within(p, g.path) {
		return ""
	}
	return p[len(g.path)+1:]
}

// isIgnored reports the ignore state of rel (-1 unknown, 0 no, 1 yes).
func (g *gitRoot) isIgnored(rel string) int8 {
	if !g.checked {
		return -1
	}
	for p := rel; p != "." && p != "" && p != "/"; p = filepath.Dir(p) {
		if g.ignored[p] {
			return 1
		}
	}
	return 0
}

// listIgnored runs `git ls-files --others --ignored --directory` once: the
// ignored directories decide the ignore state of every candidate and feed
// the "heavy ignored directories" discovery.
func (s *scan) listIgnored(g *gitRoot) {
	out, err := s.env.OutputTimeout(s.ctx, gitListTimeout, g.path, "git", "-c", "core.fsmonitor=false", "-c", "core.quotePath=false",
		"ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--directory")
	if err != nil {
		s.logf("artifacts: git ls-files --ignored in %s: %v", g.path, err)
		return
	}
	g.ignored = map[string]bool{}
	var dirs []string
	for _, e := range bytes.Split(out, []byte{0}) {
		if len(e) < 2 || e[len(e)-1] != '/' {
			continue
		}
		d := filepath.Clean(string(e[:len(e)-1]))
		if d == "." || strings.HasPrefix(d, "../") || d == ".." {
			continue
		}
		g.ignored[d] = true
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	var last string
	for _, d := range dirs {
		if last != "" && (d == last || strings.HasPrefix(d, last+"/")) {
			continue
		}
		g.outer = append(g.outer, d)
		last = d
	}
	g.listed, g.checked = true, true
}

// checkIgnore is the fallback when the listing failed: `git check-ignore`
// on the candidates (batched, paths as arguments).
func (s *scan) checkIgnore(g *gitRoot, rels []string) {
	g.ignored = map[string]bool{}
	for i := 0; i < len(rels); i += gitBatch {
		batch := rels[i:min(i+gitBatch, len(rels))]
		args := append([]string{"-c", "core.fsmonitor=false", "-c", "core.quotePath=false", "check-ignore", "--"}, batch...)
		out, err := s.env.OutputTimeout(s.ctx, gitCheckTimeout, g.path, "git", args...)
		if err != nil && !exitCode(err, 1) { // 1 = nothing ignored
			s.logf("artifacts: git check-ignore in %s: %v", g.path, err)
			return
		}
		for _, l := range strings.Split(string(out), "\n") {
			if l = strings.TrimRight(l, "\r"); l == "" {
				continue
			}
			g.ignored[filepath.Clean(unquoteGit(l))] = true
		}
	}
	g.checked = true
}

// trackedCands marks the candidates holding files tracked by git (authored
// content: committed Pods, zero-install .yarn/cache, a source folder named
// build...). One `git ls-files` per batch of candidates.
func (s *scan) trackedCands(g *gitRoot, cs []*cand) {
	byRel := map[string]*cand{}
	var rels []string
	for _, c := range cs {
		if r := g.rel(c.path); r != "" {
			byRel[r] = c
			rels = append(rels, r)
		}
	}
	sort.Strings(rels)
	for i := 0; i < len(rels); i += gitBatch {
		batch := rels[i:min(i+gitBatch, len(rels))]
		args := append([]string{"--literal-pathspecs", "-c", "core.fsmonitor=false", "ls-files", "-z", "--"}, batch...)
		out, err := s.env.OutputTimeout(s.ctx, gitCheckTimeout, g.path, "git", args...)
		if err != nil {
			s.logf("artifacts: git ls-files in %s: %v", g.path, err)
			return
		}
		for _, e := range bytes.Split(out, []byte{0}) {
			if len(e) == 0 {
				continue
			}
			for p := filepath.Dir(string(e)); p != "." && p != "/" && p != ""; p = filepath.Dir(p) {
				if c, ok := byRel[p]; ok {
					c.tracked = 1
					if c.trackedSample == "" {
						c.trackedSample = string(e)
					}
					break
				}
			}
		}
	}
	for _, c := range byRel {
		if c.tracked != 1 {
			c.tracked = 0
		}
	}
	g.trackedOK = true
}

func exitCode(err error, code int) bool {
	var ee *exec.ExitError
	return errors.As(err, &ee) && ee.ExitCode() == code
}

// unquoteGit decodes a path quoted by git (C-style, with octal escapes).
func unquoteGit(s string) string {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return s
	}
	body := s[1 : len(s)-1]
	var b strings.Builder
	for i := 0; i < len(body); i++ {
		c := body[i]
		if c != '\\' || i+1 >= len(body) {
			b.WriteByte(c)
			continue
		}
		i++
		switch e := body[i]; e {
		case 'a':
			b.WriteByte('\a')
		case 'b':
			b.WriteByte('\b')
		case 't':
			b.WriteByte('\t')
		case 'n':
			b.WriteByte('\n')
		case 'v':
			b.WriteByte('\v')
		case 'f':
			b.WriteByte('\f')
		case 'r':
			b.WriteByte('\r')
		case '"', '\\':
			b.WriteByte(e)
		default:
			if e >= '0' && e <= '7' && i+2 < len(body) {
				if v, err := strconv.ParseUint(body[i:i+3], 8, 8); err == nil {
					b.WriteByte(byte(v))
					i += 2
					continue
				}
			}
			b.WriteByte('\\')
			b.WriteByte(e)
		}
	}
	return b.String()
}

// indexTime returns the newest of the work tree's git index, HEAD and HEAD
// reflog: commits, checkouts, staging and editors refreshing the index.
func (g *gitRoot) indexTime() time.Time {
	if g == nil || g.gitdir == "" {
		return time.Time{}
	}
	return fsx.NewestOf(
		filepath.Join(g.gitdir, "index"),
		filepath.Join(g.gitdir, "HEAD"),
		filepath.Join(g.gitdir, "logs", "HEAD"),
	)
}

// commonDir is the repository's common git dir: where linked worktrees are
// registered (<common>/worktrees/<name>/gitdir).
func (g *gitRoot) commonDir() string {
	if g.gitdir == "" {
		return ""
	}
	if b, err := readSmall(filepath.Join(g.gitdir, "commondir"), 4096); err == nil {
		if cd := strings.TrimSpace(string(b)); cd != "" {
			if !filepath.IsAbs(cd) {
				cd = filepath.Join(g.gitdir, cd)
			}
			return filepath.Clean(cd)
		}
	}
	return g.gitdir
}

// linkedWorktrees returns the folded paths of every linked worktree
// registered in the repositories found. The walk never enters artifacts, so
// a worktree checked out below a build folder (build/site) is only known
// from there.
func (s *scan) linkedWorktrees() []string {
	s.mu.Lock()
	gits := make([]*gitRoot, 0, len(s.gits))
	for _, g := range s.gits {
		gits = append(gits, g)
	}
	s.mu.Unlock()
	commons := map[string]bool{}
	for _, g := range gits {
		if s.ctx.Err() != nil {
			return nil
		}
		if cd := g.commonDir(); cd != "" {
			commons[cd] = true
		}
	}
	var out []string
	for cd := range commons {
		admin := filepath.Join(cd, "worktrees")
		ents, _ := fsx.ReadDir(s.ctx, admin)
		for _, e := range ents {
			b, err := readSmall(filepath.Join(admin, e.Name(), "gitdir"), 4096)
			gd := strings.TrimSpace(string(b))
			if err != nil || gd == "" {
				continue
			}
			if !filepath.IsAbs(gd) { // worktree.useRelativePaths
				gd = filepath.Join(admin, e.Name(), gd)
			}
			wt := filepath.Dir(filepath.Clean(gd))
			out = append(out, foldPath(wt), foldPath(realPath(wt)))
		}
	}
	sort.Strings(out)
	return out
}

// worktreeIn returns the registered linked worktree at or below p (relative
// to p's parent for messages), "" when none.
func (s *scan) worktreeIn(p string) string {
	fp := foldPath(p)
	for _, w := range s.worktrees {
		if fsx.Within(w, fp) {
			if rel, err := filepath.Rel(foldPath(filepath.Dir(p)), w); err == nil {
				return rel
			}
			return w
		}
	}
	return ""
}

// isWorktree reports whether p is a registered linked worktree.
func (s *scan) isWorktree(p string) bool {
	fp := foldPath(p)
	for _, w := range s.worktrees {
		if w == fp {
			return true
		}
	}
	return false
}

// foldPath normalizes a path for comparisons on the (case-insensitive,
// normalization-insensitive) APFS: NFC, lower case.
func foldPath(p string) string {
	return strings.ToLower(norm.NFC.String(filepath.Clean(p)))
}

func readSmall(p string, max int64) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, max))
}

// dirtyActivity records the newest mtime of the work tree's uncommitted
// files, wherever they are: editing a file leaves the index, HEAD and the
// top-level entries untouched. Two cheap listings instead of `git status`
// (which refreshes the index and blocks on lazy fetches in partial clones):
// `git diff-files` compares stat data with the index (hashing only racily
// clean files, never needing blob objects) for modified tracked files, and
// `ls-files --others --exclude-standard --directory` lists untracked ones
// (untracked folders collapsed, walked with a small budget).
func (s *scan) dirtyActivity(g *gitRoot) {
	run := func(args ...string) ([]byte, bool) {
		out, err := s.env.OutputTimeout(s.ctx, gitCheckTimeout, g.path, "git", append([]string{"-c", "core.fsmonitor=false", "-c", "core.quotePath=false"}, args...)...)
		if err != nil {
			s.logf("artifacts: git %s in %s: %v", args[0], g.path, err)
			return nil, false
		}
		return out, true
	}
	modified, ok1 := run("diff-files", "--name-only", "-z", "--ignore-submodules=all")
	if !ok1 {
		return
	}
	untracked, ok2 := run("ls-files", "-z", "--others", "--exclude-standard", "--directory")
	if !ok2 {
		return
	}
	var newest time.Time
	n := 0
	// Modified files are tracked, hence sources wherever they are (a
	// tracked file is never inside an artifact: decide rejects those), even
	// in folders named like one (bin/, scripts/build/, internal/out/).
	// Untracked files are listed one by one only in folders git knows, so
	// they count too; an untracked FOLDER named like an artifact
	// (packages/x/node_modules/ not ignored) is the artifact's own writes,
	// and too big to walk.
	for _, e := range bytes.Split(append(append(modified, 0), untracked...), []byte{0}) {
		rel := string(e)
		dir := strings.HasSuffix(rel, "/")
		if rel == "" || dir && s.inArtifact(rel) {
			continue
		}
		if n++; n > maxDirtyStat {
			break
		}
		p := filepath.Join(g.path, rel)
		var t time.Time
		if dir {
			t = s.sourceActivity(p, untrackedDirBudget) // files only: the folder's mtime moves when an artifact inside is deleted
		} else {
			t = fsx.ModTime(p) // a deleted file has none (zero)
		}
		if t.After(newest) {
			newest = t
		}
	}
	g.dirty, g.dirtyOK = newest, true
}

// inArtifact reports whether a work-tree relative folder ("a/b/") is, or
// lies inside, a folder named like an artifact.
func (s *scan) inArtifact(rel string) bool {
	for _, part := range strings.Split(strings.TrimSuffix(rel, "/"), "/") {
		if s.rs.names[part] {
			return true
		}
	}
	return false
}
