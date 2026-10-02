package worktrees

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
	"github.com/ludwig-pro/lu-cleaner/internal/providers/internal/scanwalk"
	"github.com/ludwig-pro/lu-cleaner/internal/scanctl"
)

const (
	gitTimeout     = 20 * time.Second // git status on a huge checkout
	cherryMaxAhead = 100              // only look for patch-equivalent merges on small branches
	maxDirtyStat   = 300              // dirty paths stat'ed for last activity
)

// git runs git with a timeout and returns stdout.
func (s *scan) git(timeout time.Duration, args ...string) (string, error) {
	out, err := s.env.OutputTimeout(s.ctx, timeout, "", "git", args...)
	if errors.Is(err, context.DeadlineExceeded) && s.ctx.Err() == nil {
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
	var linked, prunable []listEntry
	for i, e := range entries {
		if i == 0 || e.bare {
			continue // the main working tree (or the bare repository itself)
		}
		if e.prunable {
			prunable = append(prunable, e)
			continue
		}
		linked = append(linked, e)
		if fsx.Exists(e.path) {
			s.addCandidate(e.path)
		}
	}
	s.mu.Lock()
	r.entries = linked
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
func (s *scan) repoInfo(r *repo) error {
	return r.info.Do(s.ctx, func() { s.resolveRepoInfo(r) })
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
	if !w.gitUsable() {
		times = append(times, fsx.ModTime(w.path)) // no git signal left (refined by sizing)
	}
	if w.gitUsable() && !w.external {
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
		if w.gitOK {
			w.envFiles, w.envErr = s.ignoredSecrets(w)
		}
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

// gitUsable reports whether git can inspect the worktree: it is tracked by
// a reachable, readable main repository.
func (w *worktree) gitUsable() bool {
	return w.orphan == "" && w.offline == "" && w.unreadable == "" && w.repairAt == ""
}

// gitState runs git status & friends inside the worktree. Flags that user
// config could change are explicit (untracked files, submodules), like the
// checks run by the cleaner before removal.
func (s *scan) gitState(w *worktree) {
	out, err := s.git(gitTimeout, "-c", "core.fsmonitor=false", "-C", w.path,
		"status", "--porcelain=v2", "--branch", "-z", "--untracked-files=normal", "--ignore-submodules=none")
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
		if err := s.repoInfo(r); err != nil {
			w.gitOK = false
			w.gitErr = "repository state unavailable"
			return
		}
	}
	// Unpushed commits.
	switch {
	case unborn:
		w.unpushed, w.unpushedOK = 0, true
	case w.detached:
		// Same rule as the removal (clean refuses without --force): with a
		// detached HEAD, the commits contained in no branch, tag or
		// remote-tracking ref are lost. Being patch-equivalent to the default
		// branch (cherry-picked, rebased) does not save them.
		if out, err := s.git(gitTimeout, "-C", w.path, "rev-list", "--count", "HEAD", "--not", "--branches", "--tags", "--remotes"); err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(out)); err == nil {
				w.unpushed, w.unpushedOK = n, true
			}
		}
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
		// Everything is in the remote default branch: nothing can be lost
		// (a branch is kept by the removal anyway; a detached HEAD keeps the
		// stricter rule above).
		if w.merged && r.defRemote && !w.detached {
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

// ---------------------------------------------------------------- ignored secrets

// artifactDirs are ignored folders that only hold regenerable build output or
// dependencies: they are never searched for secrets.
var artifactDirs = map[string]bool{
	"node_modules": true, "pods": true, "build": true, "deriveddata": true, ".gradle": true,
	".expo": true, ".next": true, "dist": true, ".turbo": true, ".cxx": true, "target": true,
	".cache": true, ".yarn": true, "__pycache__": true, ".venv": true, "venv": true,
	".dart_tool": true, "coverage": true, ".nuxt": true, ".output": true, ".svelte-kit": true,
	".parcel-cache": true, ".pnpm-store": true, "out": true, "tmp": true, ".tox": true,
	".mypy_cache": true, ".pytest_cache": true, ".ruff_cache": true, ".angular": true,
	"xcuserdata": true, ".build": true, ".swiftpm": true, "carthage": true,
}

// regenerable tells whether an ignored folder only holds regenerable data
// (dependencies, build output, caches): it is not searched for secrets.
func regenerable(name string) bool {
	n := strings.ToLower(name)
	return artifactDirs[n] || strings.Contains(n, "cache")
}

// secretNames are file names (lowercase) holding credentials or personal
// settings that nothing regenerates.
var secretNames = map[string]bool{
	".envrc": true, ".npmrc": true, ".netrc": true, ".pypirc": true,
	".dev.vars": true, ".secrets": true, "secrets": true,
	"google-services.json": true, "googleservice-info.plist": true,
	"key.properties": true, "keystore.properties": true, "signing.properties": true,
	"credentials.json": true, "credentials": true, "id_rsa": true, "id_ed25519": true, "id_ecdsa": true,
	"claude.local.md": true, "settings.local.json": true, "auth.json": true, ".mcp.json": true,
}

// secretExts are extensions (lowercase) of keys, certificates, signing
// material and infrastructure state.
var secretExts = map[string]bool{
	".keystore": true, ".jks": true, ".p8": true, ".p12": true, ".pfx": true, ".pem": true,
	".key": true, ".mobileprovision": true, ".provisionprofile": true, ".tfstate": true,
	".tfvars": true, ".secret": true, ".secrets": true, ".env": true, ".local": true,
}

// isSecretFile tells whether the file rel (relative to the worktree) looks
// like a secret or a personal local setting: .env*, keystores, signing keys,
// Firebase configs, *.local.* overrides, Claude local settings...
func isSecretFile(rel string) bool {
	n := strings.ToLower(filepath.Base(rel))
	switch n {
	case "debug.keystore":
		return false // Android's well-known debug key (password "android"), regenerated by the build
	case ".xcode.env.local":
		return false // React Native: the node path, rewritten by every `pod install`
	}
	for _, sfx := range []string{".example", ".sample", ".template", ".dist", ".defaults", ".schema"} {
		if strings.HasSuffix(n, sfx) {
			return false // committed-style templates
		}
	}
	switch {
	case secretNames[n], secretExts[filepath.Ext(n)]:
		return true
	case n == ".env", strings.HasPrefix(n, ".env."), strings.Contains(n, ".local."),
		strings.Contains(n, ".tfstate"), strings.HasPrefix(n, "id_rsa"), strings.HasPrefix(n, "id_ed25519"),
		strings.HasPrefix(n, "service-account") && strings.HasSuffix(n, ".json"),
		strings.HasPrefix(n, "serviceaccount") && strings.HasSuffix(n, ".json"),
		strings.HasPrefix(n, "secrets.") || strings.HasPrefix(n, "secret."):
		return true
	}
	return false
}

// Bounds of the search for secrets inside a whole ignored folder.
const (
	secretWalkDepth   = 6
	secretWalkEntries = 5000
)

// ignoredSecrets lists the ignored files of w that look like secrets or
// personal settings (see isSecretFile) and exist nowhere else: `git worktree
// remove` deletes ignored files without asking. Files identical to the same
// path in the main working tree (tools copy .env from there) are not lost.
// Whole ignored folders are searched too, except build output and
// dependencies. errMsg is set when the ignored files could not be listed.
func (s *scan) ignoredSecrets(w *worktree) (lost []string, errMsg string) {
	out, err := s.git(gitTimeout, "-C", w.path, "ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--directory")
	if err != nil {
		if errors.Is(err, errTimeout) {
			return nil, "listing ignored files timed out"
		}
		return nil, "cannot list ignored files"
	}
	var entries []string
	for _, e := range strings.Split(out, "\x00") {
		if e != "" {
			entries = append(entries, e)
		}
	}
	sort.Strings(entries)
	var found []string
	for i, e := range entries {
		if !strings.HasSuffix(e, "/") {
			// Only a regular file loses data: removing a symlink leaves its
			// target (an ignored target inside the worktree is listed itself).
			// An lstat error keeps the entry (fail closed).
			if isSecretFile(e) {
				if fi, err := fsx.Lstat(s.ctx, filepath.Join(w.path, e)); err != nil || fi.Mode().IsRegular() {
					found = append(found, filepath.Clean(e))
				}
			}
			continue
		}
		// An untracked folder holding ignored files is listed before them:
		// its entries follow it (sorted). Otherwise the folder itself is
		// ignored as a whole and git does not list its content.
		if i+1 < len(entries) && strings.HasPrefix(entries[i+1], e) {
			continue
		}
		dir := strings.TrimSuffix(e, "/")
		if regenerable(filepath.Base(dir)) {
			continue
		}
		found = append(found, secretsIn(s.ctx, w.path, dir)...)
	}
	for _, rel := range found {
		if !sameAsMain(s.ctx, w, rel) {
			lost = append(lost, rel)
		}
	}
	return lost, ""
}

// secretsIn searches the ignored folder dir (relative to root) for secret
// files, without following symlinks or entering build output.
func secretsIn(ctx context.Context, root, dir string) []string {
	var found []string
	seen := 0
	base := filepath.Join(root, dir)
	_ = scanwalk.WalkDir(ctx, base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if seen++; seen > secretWalkEntries {
			return filepath.SkipAll
		}
		rel, _ := filepath.Rel(root, p)
		if d.IsDir() {
			if p != base && (regenerable(d.Name()) || d.Name() == ".git" ||
				strings.Count(rel, "/")-strings.Count(dir, "/") >= secretWalkDepth) {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() && isSecretFile(rel) {
			found = append(found, rel)
		}
		return nil
	})
	return found
}

// maxCompare bounds the files compared with the main working tree.
const maxCompare = 1 << 20

// sameAsMain reports whether the file rel of w has an identical copy at the
// same place in the main working tree.
func sameAsMain(ctx context.Context, w *worktree, rel string) bool {
	if w.bare || w.main == "" || w.main == w.path {
		return false
	}
	a, b := filepath.Join(w.path, rel), filepath.Join(w.main, rel)
	fa, err1 := fsx.Lstat(ctx, a)
	fb, err2 := fsx.Lstat(ctx, b)
	if err1 != nil || err2 != nil || !fa.Mode().IsRegular() || !fb.Mode().IsRegular() ||
		fa.Size() != fb.Size() || fa.Size() > maxCompare {
		return false
	}
	var ca, cb []byte
	err := scanctl.DoIO(ctx, func() error {
		ca, err1 = os.ReadFile(a)
		if err1 != nil {
			return err1
		}
		cb, err2 = os.ReadFile(b)
		return err2
	})
	if err != nil {
		return false
	}
	return err1 == nil && err2 == nil && bytes.Equal(ca, cb)
}
