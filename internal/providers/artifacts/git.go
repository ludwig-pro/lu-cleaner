package artifacts

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
)

const (
	gitListTimeout  = 20 * time.Second // ls-files --others --ignored per work tree
	gitCheckTimeout = 10 * time.Second // check-ignore / ls-files <paths> batches
	gitBatch        = 200              // paths per command line
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
}

// addGitRoot registers dir (which holds a .git entry) as a work tree.
func (s *scan) addGitRoot(dir string, wc walkCtx) *gitRoot {
	s.mu.Lock()
	if g, ok := s.gits[dir]; ok {
		s.mu.Unlock()
		return g
	}
	s.mu.Unlock()
	g := &gitRoot{path: dir, root: wc.root, depth: wc.depth}
	dotgit := filepath.Join(dir, ".git")
	fi, err := os.Lstat(dotgit)
	if err != nil {
		return nil
	}
	switch {
	case fi.IsDir():
		g.gitdir = dotgit
	case fi.Mode().IsRegular():
		gd := readGitFile(dotgit)
		if gd == "" {
			return nil
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
	default:
		return nil
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
		if _, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
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
	ctx, cancel := context.WithTimeout(s.ctx, gitListTimeout)
	defer cancel()
	out, err := s.env.Output(ctx, g.path, "git", "-c", "core.fsmonitor=false", "-c", "core.quotePath=false",
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
		ctx, cancel := context.WithTimeout(s.ctx, gitCheckTimeout)
		args := append([]string{"-c", "core.fsmonitor=false", "-c", "core.quotePath=false", "check-ignore", "--"}, batch...)
		out, err := s.env.Output(ctx, g.path, "git", args...)
		cancel()
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
		ctx, cancel := context.WithTimeout(s.ctx, gitCheckTimeout)
		args := append([]string{"--literal-pathspecs", "-c", "core.fsmonitor=false", "ls-files", "-z", "--"}, batch...)
		out, err := s.env.Output(ctx, g.path, "git", args...)
		cancel()
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

func readSmall(p string, max int64) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, max))
}
