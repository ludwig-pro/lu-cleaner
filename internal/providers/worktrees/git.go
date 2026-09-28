package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
)

const (
	gitTimeout     = 20 * time.Second // git status on a huge checkout
	cherryMaxAhead = 100              // only look for patch-equivalent merges on small branches
	maxDirtyStat   = 300              // dirty paths stat'ed for last activity
)

// git runs git with a timeout and returns stdout.
func (s *scan) git(timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(s.ctx, timeout)
	defer cancel()
	out, err := s.env.Output(ctx, "", "git", args...)
	if err != nil && ctx.Err() != nil && s.ctx.Err() == nil {
		err = errTimeout
	}
	return string(out), err
}

var errTimeout = errors.New("timed out")

// listAll runs `git worktree list` on every main repository not listed yet,
// adding the worktrees it reports. New mains may appear (worktrees of other
// repositories): repeat a few times.
func (s *scan) listAll() {
	for round := 0; round < 4 && s.ctx.Err() == nil; round++ {
		var todo []*repo
		s.mu.Lock()
		for _, r := range s.repos {
			if !r.listed {
				r.listed = true
				todo = append(todo, r)
			}
		}
		s.mu.Unlock()
		if len(todo) == 0 {
			return
		}
		parallel(s.ctx, len(todo), gitWorkers, func(i int) { s.list(todo[i]) })
	}
}

// list asks the main repository r for its worktrees.
func (s *scan) list(r *repo) {
	if !fsx.IsDir(filepath.Join(r.common, "worktrees")) {
		return // no linked worktree has ever been registered
	}
	out, err := s.git(gitTimeout, "-C", r.path, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return
	}
	entries := parseWorktreeList(out)
	var prunable []listEntry
	for i, e := range entries {
		if i == 0 || e.bare {
			continue // the main working tree (or the bare repository itself)
		}
		if e.prunable {
			prunable = append(prunable, e)
			continue
		}
		if fsx.Exists(e.path) {
			s.addCandidate(e.path)
		}
	}
	s.mu.Lock()
	r.prunable = prunable
	s.mu.Unlock()
}

// parseWorktreeList parses `git worktree list --porcelain -z`.
func parseWorktreeList(out string) []listEntry {
	var list []listEntry
	var cur *listEntry
	for _, f := range strings.Split(out, "\x00") {
		if f == "" {
			if cur != nil {
				list = append(list, *cur)
				cur = nil
			}
			continue
		}
		key, val, _ := strings.Cut(f, " ")
		if key == "worktree" {
			if cur != nil {
				list = append(list, *cur)
			}
			cur = &listEntry{path: filepath.Clean(val)}
			continue
		}
		if cur == nil {
			continue
		}
		switch key {
		case "HEAD":
			cur.head = val
		case "branch":
			cur.branch = strings.TrimPrefix(val, "refs/heads/")
		case "detached":
			cur.detached = true
		case "bare":
			cur.bare = true
		case "locked":
			cur.locked = true
			if cur.reason == "" {
				cur.reason = val
			}
		case "prunable":
			cur.prunable = true
			cur.reason = val
		}
	}
	if cur != nil {
		list = append(list, *cur)
	}
	return list
}

// repoInfo resolves (once; concurrent callers wait) whether r has remotes
// and its default branch.
func (s *scan) repoInfo(r *repo) {
	r.info.Do(func() { s.resolveRepoInfo(r) })
}

func (s *scan) resolveRepoInfo(r *repo) {
	if out, err := s.git(10*time.Second, "-C", r.path, "remote"); err == nil {
		r.hasRemotes = strings.TrimSpace(out) != ""
	}
	out, err := s.git(10*time.Second, "-C", r.path, "for-each-ref", "--format=%(refname)%00%(symref)",
		"refs/remotes/origin/HEAD", "refs/remotes/origin/main", "refs/remotes/origin/master",
		"refs/heads/main", "refs/heads/master")
	if err != nil {
		return
	}
	refs := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		name, sym, _ := strings.Cut(strings.TrimSpace(line), "\x00")
		if name != "" {
			refs[name] = sym
		}
	}
	pick := ""
	if sym := refs["refs/remotes/origin/HEAD"]; sym != "" {
		if _, ok := refs[sym]; ok || strings.HasPrefix(sym, "refs/remotes/") {
			pick = sym
		}
	}
	for _, c := range []string{"refs/remotes/origin/main", "refs/remotes/origin/master", "refs/heads/main", "refs/heads/master"} {
		if pick != "" {
			break
		}
		if _, ok := refs[c]; ok {
			pick = c
		}
	}
	if pick == "" {
		return
	}
	r.defRef = pick
	r.defRemote = strings.HasPrefix(pick, "refs/remotes/")
	r.defName = strings.TrimPrefix(strings.TrimPrefix(pick, "refs/remotes/"), "refs/heads/")
}

// inspect fills the git state, the tool state and the last activity of w.
func (s *scan) inspect(w *worktree) {
	s.tools.apply(w)
	defer func() { w.tool = s.classify(w) }() // uses the branch name

	// Lock state lives in the admin dir, readable even when git fails.
	if w.gitdir != "" && fsx.Exists(filepath.Join(w.gitdir, "locked")) {
		w.locked = true
		w.lockReason = firstLine(readTrim(filepath.Join(w.gitdir, "locked")))
	}
	// Activity signals. The checkout directory's own mtime is deliberately
	// left out: tools (Expo, Xcode, scanners) bump it without real use. The
	// .git file is written when the worktree is created.
	times := []time.Time{fsx.ModTime(filepath.Join(w.path, ".git"))}
	if w.orphan != "" || w.offline != "" {
		times = append(times, fsx.ModTime(w.path)) // no git signal left (refined by sizing)
	}
	if w.orphan == "" && w.offline == "" && !w.external {
		s.gitState(w)
		times = append(times, w.headDate,
			fsx.ModTime(filepath.Join(w.gitdir, "index")),
			fsx.ModTime(filepath.Join(w.gitdir, "HEAD")),
			fsx.ModTime(filepath.Join(w.gitdir, "logs", "HEAD")))
		for i, p := range w.dirtyPaths {
			if i >= maxDirtyStat {
				break
			}
			times = append(times, fsx.ModTime(filepath.Join(w.path, p)))
		}
		w.envFiles = s.ignoredEnvFiles(w)
	}
	for _, t := range times {
		if t.After(w.activity) && !t.After(s.now.Add(24*time.Hour)) {
			w.activity = t
		}
	}
	if t := s.tools.lastActivity(w.path); t.After(w.activity) && !t.After(s.now.Add(24*time.Hour)) {
		w.activity = t
	}
}

// gitState runs git status & friends inside the worktree.
func (s *scan) gitState(w *worktree) {
	out, err := s.git(gitTimeout, "-c", "core.fsmonitor=false", "-C", w.path,
		"status", "--porcelain=v2", "--branch", "-z", "--untracked-files=normal")
	if err != nil {
		w.gitErr = "git status failed"
		if errors.Is(err, errTimeout) {
			w.gitErr = "git status timed out"
		}
		return
	}
	st := parseStatus(out)
	w.gitOK = true
	w.dirty = len(st.paths)
	w.dirtyPaths = st.paths
	w.upstream = st.upstream
	if st.head == "(detached)" {
		w.detached = true
	} else {
		w.branch = st.head
	}
	unborn := st.oid == "" || st.oid == "(initial)"
	if !unborn && len(st.oid) >= 7 {
		w.commit = st.oid[:7]
	}
	if !unborn {
		if out, err := s.git(10*time.Second, "-C", w.path, "log", "-1", "--format=%ct", "HEAD"); err == nil {
			if sec, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64); err == nil {
				w.headDate = time.Unix(sec, 0)
			}
		}
	}

	r := w.repo
	if r != nil {
		s.repoInfo(r)
	}
	// Unpushed commits.
	switch {
	case unborn:
		w.unpushed, w.unpushedOK = 0, true
	case st.upstream != "" && st.abOK:
		w.unpushed, w.unpushedOK = st.ahead, true
	default:
		w.gone = st.upstream != ""
		// No upstream (or deleted on the remote): commits that exist nowhere
		// else. Without any remote: commits reachable from no branch (a
		// detached HEAD's work, lost once the worktree's reflog is gone).
		not := "--remotes"
		if r != nil && !r.hasRemotes {
			not = "--branches"
		}
		if out, err := s.git(gitTimeout, "-C", w.path, "rev-list", "--count", "HEAD", "--not", not); err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(out)); err == nil {
				w.unpushed, w.unpushedOK = n, true
			}
		}
	}
	// Merged into the default branch (ancestor, or every commit patch-equivalent).
	if !unborn && r != nil && r.defRef != "" {
		if out, err := s.git(gitTimeout, "-C", w.path, "rev-list", "--count", "HEAD", "--not", r.defRef); err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(out)); err == nil {
				w.mergedOK = true
				w.merged = n == 0
				if n > 0 && n <= cherryMaxAhead {
					if out, err := s.git(gitTimeout, "-C", w.path, "cherry", r.defRef, "HEAD"); err == nil {
						w.merged = !strings.Contains("\n"+out, "\n+")
					}
				}
			}
		}
		// Everything is in the remote default branch: nothing can be lost.
		if w.merged && r.defRemote {
			w.unpushed, w.unpushedOK = 0, true
		}
	}
}

// status is the parsed output of `git status --porcelain=v2 --branch -z`.
type status struct {
	oid, head, upstream string
	ahead, behind       int
	abOK                bool
	paths               []string // changed, unmerged and untracked paths
}

func parseStatus(out string) status {
	var st status
	fields := strings.Split(out, "\x00")
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if f == "" {
			continue
		}
		switch f[0] {
		case '#':
			key, val, _ := strings.Cut(strings.TrimPrefix(f, "# "), " ")
			switch key {
			case "branch.oid":
				st.oid = val
			case "branch.head":
				st.head = val
			case "branch.upstream":
				st.upstream = val
			case "branch.ab":
				var a, b int
				for _, x := range strings.Fields(val) {
					n, err := strconv.Atoi(x[1:])
					if err != nil {
						continue
					}
					if x[0] == '+' {
						a = n
					} else {
						b = n
					}
				}
				st.ahead, st.behind, st.abOK = a, b, true
			}
		case '1':
			if p := nthField(f, 8); p != "" {
				st.paths = append(st.paths, p)
			}
		case '2':
			if p := nthField(f, 9); p != "" {
				st.paths = append(st.paths, p)
			}
			i++ // the original path follows as its own field
		case 'u':
			if p := nthField(f, 10); p != "" {
				st.paths = append(st.paths, p)
			}
		case '?':
			st.paths = append(st.paths, strings.TrimPrefix(f, "? "))
		}
	}
	return st
}

// nthField returns everything after the n-th space-separated field (the
// path, which may itself contain spaces).
func nthField(s string, n int) string {
	parts := strings.SplitN(s, " ", n+1)
	if len(parts) <= n {
		return ""
	}
	return parts[n]
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	if len(s) > 80 {
		s = s[:80] + "…"
	}
	return s
}

// ignoredEnvFiles lists .env files that are neither tracked nor shown by git
// status (i.e. ignored): they would be lost with the worktree. Only the
// worktree root, its first-level folders and apps/* / packages/* are looked at.
func (s *scan) ignoredEnvFiles(w *worktree) []string {
	var cands []string
	look := func(rel string) {
		entries, err := os.ReadDir(filepath.Join(w.path, rel))
		if err != nil {
			return
		}
		for _, e := range entries {
			n := e.Name()
			if !strings.HasPrefix(n, ".env") || e.IsDir() {
				continue
			}
			switch n {
			case ".env.example", ".env.sample", ".env.template", ".env.dist", ".envrc":
				continue
			}
			cands = append(cands, filepath.Join(rel, n))
		}
	}
	look(".")
	top, _ := os.ReadDir(w.path)
	for _, d := range top {
		n := d.Name()
		if !d.IsDir() || strings.HasPrefix(n, ".") || pruneDirs[n] || n == "ios" || n == "android" {
			continue
		}
		look(n)
		if n == "apps" || n == "packages" {
			subs, _ := os.ReadDir(filepath.Join(w.path, n))
			for _, sd := range subs {
				if sd.IsDir() && !pruneDirs[sd.Name()] {
					look(filepath.Join(n, sd.Name()))
				}
			}
		}
	}
	if len(cands) == 0 {
		return nil
	}
	for i := range cands {
		cands[i] = filepath.Clean(cands[i])
	}
	known := map[string]bool{}
	for _, p := range w.dirtyPaths {
		known[filepath.Clean(p)] = true
	}
	args := append([]string{"-C", w.path, "ls-files", "-z", "--"}, cands...)
	if out, err := s.git(10*time.Second, args...); err == nil {
		for _, p := range strings.Split(out, "\x00") {
			if p != "" {
				known[filepath.Clean(p)] = true
			}
		}
	} else {
		return nil
	}
	var lost []string
	for _, c := range cands {
		if !known[c] {
			lost = append(lost, c)
		}
	}
	return lost
}
