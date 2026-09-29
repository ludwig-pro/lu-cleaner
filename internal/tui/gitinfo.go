package tui

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
	"github.com/ludwig-pro/lu-cleaner/internal/safety"
)

// gitKind classifies a directory listed by the analyzer.
type gitKind int

const (
	gitNone     gitKind = iota // not a git checkout
	gitRepo                    // a repository: never deleted by the analyzer
	gitWorktree                // a verified linked worktree: removed through git
	gitOther                   // a .git entry that is not a verified linked worktree: never deleted
)

// gitInfo is what the analyzer knows about a directory's relation to git.
type gitInfo struct {
	kind gitKind
	// common is the common git dir of a linked worktree's repository
	// (<main>/.git, the bare repository, or a --separate-git-dir): git runs
	// there to remove the worktree, whatever the repository layout.
	common string
	main   string // linked worktree: main working tree, or the bare repository (display only)
	locked bool   // linked worktree locked with `git worktree lock`
	label  string // short tag shown in the list
	why    string // gitRepo / gitOther: why the analyzer refuses to delete it
}

// classifyGit tells whether dir is a git repository, a linked worktree that
// git can remove, or another kind of checkout (submodule, orphan, copy,
// unreadable .git...). Only a linked worktree whose admin dir points back to
// dir, inside a repository that still exists, is deletable (through
// `git worktree remove`); everything else holding git data is refused, since
// an rm -rf would lose uncommitted work or the history itself.
func classifyGit(dir string) gitInfo {
	dotgit := filepath.Join(dir, ".git")
	fi, err := os.Lstat(dotgit)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// A directory named .git holds a repository's history even when it
		// does not look complete (APFS: .GIT is the same name).
		if strings.EqualFold(filepath.Base(dir), ".git") {
			return refuseRepo("git data", "the git data of a repository")
		}
		if isBareRepo(dir) {
			return refuseRepo("git repo (bare)", "a bare git repository")
		}
		return gitInfo{}
	case err != nil:
		// Only a clean ENOENT proves there is no git data here. Most often
		// the directory itself is unreadable (permissions, macOS privacy
		// protection): say so rather than calling it a git checkout.
		return refuseOther("🔒 unreadable", "unreadable ("+errText(err)+"): whether it holds git data cannot be checked")
	case fi.IsDir():
		return refuseRepo("git repo", "a git repository (source code)")
	case !fi.Mode().IsRegular():
		return refuseOther("git checkout (.git symlink)", "its .git is a symlink: a checkout that git cannot remove safely")
	}

	gitdir, ok := readGitdirFile(dotgit)
	if !ok {
		return refuseOther("git checkout (.git file)", "its .git file cannot be resolved")
	}
	if inside(gitdir, dir) {
		// e.g. `git clone --bare url .bare && echo "gitdir: ./.bare" > .git`:
		// the whole history and every branch live in this directory.
		return refuseRepo("git repo (bare layout)", "a git repository whose history lives inside it")
	}
	gfi, err := os.Stat(gitdir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return refuseOther("git checkout (orphaned)", "its git dir is gone (orphaned worktree?) — clean it from the Worktrees view (lu-cleaner worktrees)")
	case err != nil:
		return refuseOther("git checkout (git dir unreadable)", "its git dir cannot be read ("+errText(err)+")")
	case !gfi.IsDir():
		return refuseOther("git checkout (.git file)", "its .git file does not point to a git dir")
	}

	cb, err := os.ReadFile(filepath.Join(gitdir, "commondir"))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// A full git dir outside the checkout: a submodule, or the main
		// working tree of a --separate-git-dir repository.
		if underModules(gitdir) {
			return refuseOther("git submodule", "a git submodule checkout (uncommitted work would be lost)")
		}
		return refuseRepo("git repo (separate git dir)", "the working tree of a git repository (separate git dir)")
	case err != nil:
		return refuseOther("git checkout (unreadable)", "its git dir cannot be read ("+errText(err)+")")
	}
	common := strings.TrimSpace(string(cb))
	if common == "" {
		return refuseOther("git checkout (.git file)", "its git dir has an empty commondir")
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(gitdir, common)
	}
	common = filepath.Clean(common)

	// A linked worktree's admin dir is <common>/worktrees/<name>.
	if filepath.Base(filepath.Dir(gitdir)) != "worktrees" || !samePath(filepath.Dir(filepath.Dir(gitdir)), common) {
		return refuseOther("git checkout (.git file)", "unrecognised git layout (not a linked worktree)")
	}
	if underModules(common) {
		return refuseOther("git submodule worktree", "a worktree of a git submodule")
	}
	if inside(common, dir) {
		return refuseRepo("git repo (bare layout)", "a git repository whose history lives inside it")
	}
	if !isGitDir(common) {
		return refuseOther("git checkout (orphaned)", "its repository is missing or unreadable — clean it from the Worktrees view (lu-cleaner worktrees)")
	}
	// The admin dir must point back here: a copied or moved worktree is not
	// the one git knows, and `git worktree remove` would act on another path.
	back, err := os.ReadFile(filepath.Join(gitdir, "gitdir"))
	if err != nil {
		return refuseOther("git checkout (unregistered)", "git has no record of this worktree")
	}
	b := strings.TrimSpace(string(back))
	if b != "" && !filepath.IsAbs(b) {
		b = filepath.Join(gitdir, b) // worktree.useRelativePaths
	}
	if b == "" || !samePath(filepath.Clean(b), dotgit) {
		return refuseOther("git checkout (copy)", "git registers this worktree elsewhere: a copy or a moved worktree")
	}
	info := gitInfo{kind: gitWorktree, common: common, main: common, label: "🌳 linked worktree"}
	if filepath.Base(common) == ".git" {
		info.main = filepath.Dir(common)
	}
	if _, err := os.Lstat(filepath.Join(gitdir, "locked")); err == nil {
		info.locked = true
		info.label += " · locked"
	}
	return info
}

func refuseRepo(label, why string) gitInfo  { return gitInfo{kind: gitRepo, label: label, why: why} }
func refuseOther(label, why string) gitInfo { return gitInfo{kind: gitOther, label: label, why: why} }

// refusal returns why the analyzer never deletes e ("" when it may).
func (e *anEntry) refusal() string {
	if e.git.kind == gitRepo || e.git.kind == gitOther {
		return e.git.why
	}
	return e.inGit
}

// gitDataRefusal returns why nothing at or below dir may be deleted by the
// analyzer: dir or one of its ancestors is a git dir, i.e. a directory named
// .git (any case: APFS) or a bare repository / separate git dir (HEAD,
// objects/, refs/), whose content is a repository's history and state. It
// returns "" otherwise.
func gitDataRefusal(dir string) string {
	for a := filepath.Clean(dir); ; a = filepath.Dir(a) {
		if strings.EqualFold(filepath.Base(a), ".git") || isBareRepo(a) {
			return "part of the git data of the repository " + a
		}
		if a == filepath.Dir(a) {
			return ""
		}
	}
}

// dotGitFileRefusal returns why p, when it is a checkout's .git file or
// symlink (not a directory), must not be deleted: git would lose track of the
// checkout (a linked worktree, a submodule...). "" otherwise.
func dotGitFileRefusal(p string) string {
	if strings.EqualFold(filepath.Base(p), ".git") {
		return "the .git entry of a git checkout (git would lose track of it)"
	}
	return ""
}

// gitDataEntryRefusal is the refusal of an entry about to be deleted: a .git
// file or symlink, anything inside a git dir, and (isDir) a git dir itself.
func gitDataEntryRefusal(path string, isDir bool) string {
	if !isDir {
		if why := dotGitFileRefusal(path); why != "" {
			return why
		}
		return gitDataRefusal(filepath.Dir(path))
	}
	return gitDataRefusal(path)
}

// nestedRepoMaxDepth bounds the search for repositories inside a folder the
// analyzer deletes; the search also stops (and fails closed) beyond
// safety.FindNestedRepo's entry budget.
const nestedRepoMaxDepth = 32

// nestedSkip are regenerable dependency / build folders the search does not
// enter: they never hold anyone's repository and can be huge (the executor
// skips the same ones when it checks a worktree).
var nestedSkip = map[string]bool{"node_modules": true, "Pods": true, ".gradle": true, "DerivedData": true, ".build": true}

// nestedRepoIn returns a git repository or worktree strictly inside dir (""
// if none). It fails closed: an unreadable directory, a tree too large to
// inspect or another app's container (it cannot be read without Full Disk
// Access, and reading it would raise a macOS prompt) is an error.
func nestedRepoIn(dir string) (string, error) {
	var blocked string
	found, err := safety.FindNestedRepo(dir, nestedRepoMaxDepth, 0, func(p, name string) bool {
		if fsx.AppDataProtected(p) {
			if blocked == "" {
				blocked = p
			}
			return true
		}
		return nestedSkip[name]
	})
	if found != "" || err != nil {
		return found, err
	}
	if blocked != "" {
		return "", fmt.Errorf("%s is another app's data: %w", blocked, fsx.ErrNeedsFullDiskAccess)
	}
	return "", nil
}

// recheckPlain re-validates, right before the executor deletes it, an entry
// the analyzer removes with a plain delete (not through git): it must not be
// git data, and a folder (dir) must not hold a git repository or worktree —
// when it cannot be fully inspected it is refused unless force. nested is
// the search (nestedRepoIn when nil).
func recheckPlain(path string, dir, force bool, nested func(string) (string, error)) error {
	if why := gitDataEntryRefusal(path, dir); why != "" {
		return fmt.Errorf("%s — the analyzer never deletes git data", why)
	}
	if !dir {
		return nil
	}
	switch g := classifyGit(path); g.kind {
	case gitRepo, gitOther:
		return fmt.Errorf("%s — the analyzer never deletes it", g.why)
	case gitWorktree:
		return errors.New("became a linked worktree since it was listed — rescan (worktrees are removed through git)")
	}
	if nested == nil {
		nested = nestedRepoIn
	}
	found, err := nested(path)
	switch {
	case found != "":
		return fmt.Errorf("contains the git repository or worktree %s — the analyzer never deletes git repositories; move or delete it first", found)
	case err != nil && !force:
		return fmt.Errorf("cannot check it for git repositories inside (%v) — refusing (--force skips this check)", err)
	}
	return nil
}

func (e *anEntry) isWorktree() bool { return e.git.kind == gitWorktree }

// readGitdirFile parses a .git file ("gitdir: <path>") and returns the
// absolute, cleaned git dir it points to.
func readGitdirFile(p string) (string, bool) {
	f, err := os.Open(p)
	if err != nil {
		return "", false
	}
	defer f.Close()
	line, err := bufio.NewReader(io.LimitReader(f, 4096)).ReadString('\n')
	if err != nil && line == "" {
		return "", false
	}
	g, ok := strings.CutPrefix(strings.TrimSpace(line), "gitdir:")
	if !ok {
		return "", false
	}
	g = strings.TrimSpace(g)
	if g == "" {
		return "", false
	}
	if !filepath.IsAbs(g) {
		g = filepath.Join(filepath.Dir(p), g)
	}
	return filepath.Clean(g), true
}

// isBareRepo reports a bare repository layout: HEAD file, objects/ and refs/
// (the same test as the safety guard).
func isBareRepo(dir string) bool {
	h, err := os.Lstat(filepath.Join(dir, "HEAD"))
	if err != nil || !h.Mode().IsRegular() {
		return false
	}
	for _, sub := range []string{"objects", "refs"} {
		if fi, err := os.Lstat(filepath.Join(dir, sub)); err != nil || !fi.IsDir() {
			return false
		}
	}
	return true
}

// isGitDir reports whether dir looks like a usable git dir (HEAD, objects/, refs/).
func isGitDir(dir string) bool {
	if fi, err := os.Stat(filepath.Join(dir, "HEAD")); err != nil || !fi.Mode().IsRegular() {
		return false
	}
	for _, sub := range []string{"objects", "refs"} {
		if fi, err := os.Stat(filepath.Join(dir, sub)); err != nil || !fi.IsDir() {
			return false
		}
	}
	return true
}

// underModules reports a submodule git dir: <gitdir>/modules/<name> (the
// name may contain slashes, submodules nest), where <gitdir> is a git dir
// (<repo>/.git, a bare repository) or a linked worktree's admin dir (a
// submodule checked out in a worktree). A plain folder named "modules" (e.g.
// ~/code/modules/api) does not count: its worktrees stay deletable.
func underModules(p string) bool {
	for a := filepath.Clean(p); ; {
		parent := filepath.Dir(a)
		if parent == a {
			return false
		}
		if strings.EqualFold(filepath.Base(a), "modules") && gitAdminDir(parent) {
			return true
		}
		a = parent
	}
}

// gitAdminDir reports a git dir (HEAD, objects/, refs/) or a linked
// worktree's admin dir (HEAD and a commondir file).
func gitAdminDir(dir string) bool {
	if isGitDir(dir) {
		return true
	}
	h, err := os.Stat(filepath.Join(dir, "HEAD"))
	if err != nil || !h.Mode().IsRegular() {
		return false
	}
	c, err := os.Stat(filepath.Join(dir, "commondir"))
	return err == nil && c.Mode().IsRegular()
}

// samePath compares two paths the way APFS does (case and Unicode
// normalization insensitive), also after resolving symlinks.
func samePath(a, b string) bool {
	if safety.Key(a) == safety.Key(b) {
		return true
	}
	return safety.Key(resolved(a)) == safety.Key(resolved(b))
}

// inside reports whether p is dir or below it, as given or with symlinks
// resolved (so a git dir reached through a symlink still counts).
func inside(p, dir string) bool {
	return safety.Within(p, dir) || safety.Within(resolved(p), resolved(dir))
}

func resolved(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

func errText(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}
