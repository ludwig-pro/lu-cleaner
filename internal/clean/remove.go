package clean

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"github.com/ludwig-pro/lu-cleaner/internal/config"
	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/safety"
	"golang.org/x/sys/unix"
)

var errSkip = errors.New("skipped")

// blockingFlags are the BSD file flags that prevent unlinking and that the
// owner may clear. UF_COMPRESSED and the other flags must never be touched:
// clearing UF_COMPRESSED empties an APFS/HFS+ compressed file.
const blockingFlags = unix.UF_IMMUTABLE | unix.UF_APPEND

// RemoveAll deletes path permanently. A symlink is removed itself (never
// followed). When the first attempt fails, read-only directories (Go module
// cache, some SDKs) get owner rwx and the uchg/uappnd flags are cleared
// before a second attempt, but only inside the target's own filesystem and
// never on a file hard-linked elsewhere. A mount point inside the target
// (mounted volume or disk image) is never entered.
func RemoveAll(path string) error {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		err := os.Remove(path)
		if err == nil || fi.Mode()&fs.ModeSymlink != 0 {
			return err
		}
		if !clearBlockingFlags(path) {
			return err
		}
		return os.Remove(path)
	}
	err = os.RemoveAll(path)
	if err == nil {
		return nil
	}
	if errors.Is(err, syscall.EBUSY) {
		return fmt.Errorf("%w — it contains a mount point (mounted volume or disk image): eject it first", err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return err
	}
	rootDev := st.Dev
	_ = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d == nil || d.Type()&fs.ModeSymlink != 0 {
			return nil // never touch symlink targets; unreadable dirs are left as is
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return nil
		}
		if st.Dev != rootDev { // another filesystem mounted inside: not ours
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if st.Flags&blockingFlags != 0 && (d.IsDir() || st.Nlink <= 1) {
			_ = unix.Chflags(p, int(st.Flags&^blockingFlags))
		}
		if d.IsDir() && info.Mode().Perm()&0o700 != 0o700 {
			_ = os.Chmod(p, info.Mode().Perm()|0o700)
		}
		return nil
	})
	return os.RemoveAll(path)
}

// clearBlockingFlags clears uchg/uappnd on a file that is not hard-linked
// elsewhere, and reports whether it changed anything.
func clearBlockingFlags(path string) bool {
	var st unix.Stat_t
	if unix.Lstat(path, &st) != nil || st.Nlink > 1 || st.Flags&blockingFlags == 0 {
		return false
	}
	return unix.Chflags(path, int(st.Flags&^blockingFlags)) == nil
}

// trashMu serializes Trash moves when the volume cannot rename exclusively.
var trashMu sync.Mutex

// MoveToTrash moves path into ~/.Trash and returns the destination. On a
// name collision it picks "name 2", "name 3"... (before the extension of a
// file). The destination is claimed atomically (renamex_np RENAME_EXCL), so
// an existing entry of the Trash is never replaced. Only works on the same
// volume as the home directory; a path already in the Trash is refused.
func MoveToTrash(home, path string) (string, error) {
	trash := filepath.Join(home, ".Trash")
	if inside(path, trash) {
		return "", fmt.Errorf("%s is already in the Trash", path)
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(trash, 0o700); err != nil {
		return "", err
	}
	base := filepath.Base(path)
	for i := 1; i <= 1000; i++ {
		dst := filepath.Join(trash, trashName(base, i, fi.IsDir()))
		err := renameExcl(path, dst)
		switch {
		case err == nil:
			return dst, nil
		case errors.Is(err, unix.EEXIST), errors.Is(err, unix.ENOTEMPTY):
			continue
		case errors.Is(err, unix.EXDEV):
			return "", fmt.Errorf("%s is on another volume, cannot move it to the Trash", path)
		default:
			return "", err
		}
	}
	return "", fmt.Errorf("cannot find a free name for %s in the Trash", base)
}

// renameExcl renames from to to, failing with EEXIST when to exists.
func renameExcl(from, to string) error {
	err := unix.RenamexNp(from, to, unix.RENAME_EXCL)
	if !errors.Is(err, unix.ENOTSUP) && !errors.Is(err, unix.EINVAL) {
		return err
	}
	// The volume cannot rename exclusively: check-then-rename under a lock
	// (only other programs could still race us).
	trashMu.Lock()
	defer trashMu.Unlock()
	if _, err := os.Lstat(to); err == nil {
		return unix.EEXIST
	}
	return os.Rename(from, to)
}

// trashName returns the i-th candidate name for base in the Trash.
func trashName(base string, i int, dir bool) string {
	if i == 1 {
		return base
	}
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	if dir || stem == "" {
		stem, ext = base, ""
	}
	return stem + " " + strconv.Itoa(i) + ext
}

// orphanedCommits returns how many commits removing the worktree would make
// unreachable: 0 when HEAD is on a branch (the branch survives the removal),
// otherwise the commits of the detached HEAD contained in no branch, tag or
// remote-tracking ref.
func orphanedCommits(ctx context.Context, r core.Runner, wt string) (int, error) {
	if _, err := r.Output(ctx, wt, "git", "symbolic-ref", "-q", "HEAD"); err == nil {
		return 0, nil
	}
	out, err := r.Output(ctx, wt, "git", "rev-list", "--count", "HEAD", "--not", "--branches", "--tags", "--remotes")
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, fmt.Errorf("unexpected rev-list output %q", strings.TrimSpace(string(out)))
	}
	return n, nil
}

// statusArgs count uncommitted changes whatever the user's configuration:
// status.showUntrackedFiles=no must not hide untracked files, and submodule
// changes count too.
var statusArgs = []string{
	"-c", "core.fsmonitor=false", "-c", "status.showUntrackedFiles=all",
	"status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=none",
}

// countStatus counts the entries of `git status --porcelain=v1 -z`.
func countStatus(out []byte) int {
	fields := bytes.Split(out, []byte{0})
	n := 0
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if len(f) < 4 { // "XY path"
			continue
		}
		n++
		if f[0] == 'R' || f[0] == 'C' || f[1] == 'R' || f[1] == 'C' {
			i++ // the source path of a rename/copy follows in its own field
		}
	}
	return n
}

// wtPlan is what the pre-check learnt about a linked worktree.
type wtPlan struct {
	gitdir string // its admin dir, <common dir>/worktrees/<name>
	main   string // main repository (git runs from there)
	mainOK bool   // false: the main repository is confirmed gone (orphan removal, --force only)
}

// nestedDepth bounds the walk looking for repositories nested in a worktree.
const nestedDepth = 10

// heavyDirs are regenerable dependency/build folders the nested-repository
// walk does not enter (they never hold anyone's repository, and can be huge).
// .build is SwiftPM's: its checkouts/ and repositories/ are clones it
// manages itself (a built Swift package would otherwise always be refused).
var heavyDirs = map[string]bool{"node_modules": true, "Pods": true, ".gradle": true, "DerivedData": true, ".build": true}

// precheckWorktree validates a linked worktree before its removal. It runs
// in dry-run as well, so a dry run reports exactly what the real run would
// refuse (git's own refusals excepted). Unless opt.Force it refuses a
// worktree that is locked, has uncommitted or untracked changes (whatever
// the user's git config), has commits on no branch (detached HEAD), or holds
// another worktree or repository (registered or not, even in an ignored
// folder: git would delete it along). done reports worktrees already removed
// by this run (or that would be, in dry-run): they no longer block. It
// compares the path as given (callers try the resolved spelling too).
// It returns a message and errSkip when the worktree must be kept.
func precheckWorktree(ctx context.Context, it *core.Item, opt Options, done func(string) bool) (*wtPlan, string, error) {
	path := it.Path
	gitFile := filepath.Join(path, ".git")
	fi, err := os.Lstat(gitFile)
	if err != nil || !fi.Mode().IsRegular() {
		return nil, "not a linked worktree (no .git file) — refusing", errSkip
	}
	gitdir, err := readGitdir(gitFile)
	if err != nil {
		return nil, "cannot read its .git file (" + err.Error() + ") — refusing", errSkip
	}
	if filepath.Base(filepath.Dir(gitdir)) != "worktrees" {
		return nil, "its .git file points to " + gitdir + ", not to a linked worktree (submodule or separate git dir) — refusing", errSkip
	}
	if safety.Within(gitdir, path) || safety.Within(resolve(gitdir), resolve(path)) {
		return nil, "its git data lives inside it (" + gitdir + ") — it is a repository, not a linked worktree — refusing", errSkip
	}
	if msg := checkAdminDir(gitdir, path); msg != "" {
		return nil, msg, errSkip
	}
	plan := &wtPlan{gitdir: gitdir, main: it.Project}
	derived := mainOf(gitdir)
	if plan.main == "" {
		plan.main = derived
	}
	state, perr := probe(plan.main)
	if state == pathMissing && derived != plan.main {
		// The recorded main is gone but the admin dir may tell where it lives now.
		if s, _ := probe(derived); s == pathExists {
			plan.main, state, perr = derived, pathExists, nil
		}
	}
	switch state {
	case pathUnreadable:
		return nil, fmt.Sprintf("main repository %s is unreadable (%v) — refusing", plan.main, perr), errSkip
	case pathMissing:
		if s, err := probe(gitdir); s != pathMissing {
			return nil, fmt.Sprintf("git admin dir %s is unreadable (%v) — refusing", gitdir, err), errSkip
		}
		if !opt.Force {
			return nil, "main repository " + plan.main + " no longer exists: git cannot check it for uncommitted work — rescan (it is then listed as orphaned) or use --force", errSkip
		}
		return plan, "", nil
	}
	plan.mainOK = true
	if opt.Force {
		return plan, "", nil
	}

	if it.Meta["locked"] == "true" || exists(filepath.Join(gitdir, "locked")) {
		return nil, "worktree is locked (git worktree lock) — use --force", errSkip
	}
	if msg := nestedWorktree(ctx, opt.Runner, plan.main, path, done); msg != "" {
		return nil, msg, errSkip
	}
	// Unpushed commits on a branch are safe: `git worktree remove` keeps the
	// branch. Only commits reachable from nothing but this worktree's
	// detached HEAD would be lost.
	n, err := orphanedCommits(ctx, opt.Runner, path)
	if err != nil {
		return nil, "cannot tell whether commits would be lost (" + err.Error() + ") — use --force", errSkip
	}
	if n > 0 {
		return nil, fmt.Sprintf("detached HEAD with %d commit(s) on no branch — they would be lost; create a branch (git branch <name>) or use --force", n), errSkip
	}
	out, err := opt.Runner.Output(ctx, path, "git", statusArgs...)
	if err != nil {
		return nil, "cannot read git status (" + err.Error() + ") — use --force", errSkip
	}
	if n := countStatus(out); n > 0 {
		return nil, fmt.Sprintf("%d uncommitted change(s) — commit them or use --force", n), errSkip
	}
	// Symlinks are not followed by the walk: only the root may be spelled
	// differently from its resolved form.
	rp := resolve(path)
	found, err := safety.FindNestedRepoContext(ctx, path, nestedDepth, 0, func(p, name string) bool {
		return heavyDirs[name] || done(p) || done(rp+p[len(path):])
	})
	if err != nil {
		return nil, "cannot check it for nested repositories (" + err.Error() + ") — use --force", errSkip
	}
	if found != "" {
		return nil, "contains the git repository " + found + " (git would delete it along) — move or remove it first, or use --force", errSkip
	}
	return plan, "", nil
}

// checkAdminDir verifies, when the admin dir gitdir exists, that it is a
// linked worktree's (it holds commondir) and that git records this worktree
// at path (its gitdir file points back to path/.git): a copied or moved
// worktree folder is refused here, as git itself would refuse it, so that a
// dry run reports it too. It returns a refusal message ("" when fine). An
// admin dir that is gone is the orphan case, judged by the caller.
func checkAdminDir(gitdir, path string) string {
	if _, err := os.Lstat(gitdir); errors.Is(err, fs.ErrNotExist) {
		return ""
	}
	if _, err := os.Lstat(filepath.Join(gitdir, "commondir")); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "its .git file points to " + gitdir + ", which is not a linked worktree's admin dir (no commondir) — refusing"
		}
		return "git admin dir " + gitdir + " is unreadable (" + err.Error() + ") — refusing"
	}
	data, err := os.ReadFile(filepath.Join(gitdir, "gitdir"))
	if errors.Is(err, fs.ErrNotExist) {
		return "" // no back-pointer (very old git): left to git's own check
	}
	if err != nil {
		return "git admin dir " + gitdir + " is unreadable (" + err.Error() + ") — refusing"
	}
	back := strings.TrimSpace(string(data))
	if back == "" {
		return ""
	}
	if !filepath.IsAbs(back) { // worktree.useRelativePaths
		back = filepath.Join(gitdir, back)
	}
	recorded := filepath.Dir(filepath.Clean(back))
	// Compare identities, not spellings (case, symlinks, firmlinks).
	here, err := os.Stat(path)
	if err != nil {
		return "cannot read " + path + " (" + err.Error() + ") — refusing"
	}
	there, err := os.Stat(recorded)
	if err == nil && os.SameFile(here, there) {
		return ""
	}
	return "git records this worktree at " + recorded + ", not here: if the folder was moved, run git -C " + path +
		" worktree repair; if it is a copy, delete it yourself — then rescan"
}

// nestedWorktree returns a refusal message when another worktree registered
// in main lives inside path ("" otherwise). A registered worktree whose
// folder is confirmed gone (ENOENT) cannot be deleted along: it is ignored.
func nestedWorktree(ctx context.Context, r core.Runner, main, path string, done func(string) bool) string {
	out, err := r.Output(ctx, main, "git", "worktree", "list", "--porcelain", "-z")
	sep := byte(0)
	if err != nil { // git < 2.36 has no -z
		out, err = r.Output(ctx, main, "git", "worktree", "list", "--porcelain")
		sep = '\n'
	}
	if err != nil {
		return "cannot list the repository's worktrees (" + err.Error() + ") — use --force"
	}
	self := safety.Key(resolve(path))
	for _, f := range bytes.Split(out, []byte{sep}) {
		wp, ok := strings.CutPrefix(string(f), "worktree ")
		if !ok || wp == "" {
			continue
		}
		k := safety.Key(resolve(wp))
		if k == self || safety.Key(wp) == safety.Key(path) {
			continue
		}
		if !safety.Within(wp, path) && !safety.Within(k, self) {
			continue
		}
		if done(wp) || done(resolve(wp)) {
			continue
		}
		if _, err := os.Lstat(wp); errors.Is(err, fs.ErrNotExist) {
			continue // already gone: its admin entry is only prunable
		}
		return "contains another worktree, " + wp + " (git would delete it along) — remove it first or use --force"
	}
	return ""
}

// removeWorktree removes a linked git worktree validated by precheckWorktree.
// Branches are always kept.
func removeWorktree(ctx context.Context, it *core.Item, opt Options, plan *wtPlan) (string, error) {
	path := it.Path
	if plan.mainOK {
		// Without --force git itself refuses dirty, untracked, locked or
		// submodule worktrees: a second line of defence after our checks. The
		// config override reaches the `git status` git runs for that check.
		// Ignored files (node_modules, Pods...) never block it.
		args := []string{"git", "-c", "core.fsmonitor=false", "-c", "status.showUntrackedFiles=all", "worktree", "remove"}
		if opt.Force {
			args = append(args, "--force", "--force")
		}
		args = append(args, path)
		if _, err := runCmd(ctx, opt.Runner, plan.main, args); err != nil {
			if !opt.Force {
				return "git refused to remove it (" + err.Error() + ") — use --force", errSkip
			}
			return "", err
		}
		removeEmptyToolParent(path, opt)
		return "git worktree removed (branch kept)", nil
	}
	// Main repository confirmed gone (--force): git cannot help, remove the
	// orphaned directory. Its own .git file is expected, hence AllowGitRepo.
	if err := opt.Guard.Check(path, safety.Options{AllowGitRepo: true}); err != nil {
		return "", err
	}
	if err := RemoveAll(path); err != nil {
		return "", err
	}
	removeEmptyToolParent(path, opt)
	return "orphaned worktree directory removed", nil
}

// readGitdir parses a .git file ("gitdir: <path>") and returns the absolute,
// clean admin dir it points to.
func readGitdir(gitFile string) (string, error) {
	f, err := os.Open(gitFile)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sc := bufio.NewScanner(io.LimitReader(f, 64<<10))
	if !sc.Scan() {
		return "", errors.New("empty .git file")
	}
	gitdir, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "gitdir:")
	if !ok || strings.TrimSpace(gitdir) == "" {
		return "", errors.New("no gitdir line")
	}
	gitdir = strings.TrimSpace(gitdir)
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(filepath.Dir(gitFile), gitdir)
	}
	return filepath.Clean(gitdir), nil
}

// mainOf derives the main repository from a worktree admin dir:
// <main>/.git/worktrees/<name> → <main>; <bare>/worktrees/<name> → <bare>.
func mainOf(gitdir string) string {
	common := filepath.Dir(filepath.Dir(gitdir))
	if filepath.Base(common) == ".git" {
		return filepath.Dir(common)
	}
	return common
}

type pathState int

const (
	pathExists pathState = iota
	pathMissing
	pathUnreadable
)

// probe tells whether p exists, is confirmed missing, or cannot be judged.
// Missing requires ENOENT with a readable nearest existing ancestor on a
// mounted volume: EACCES, EPERM (macOS privacy protection), an unmounted
// volume or an unreadable parent are "unreadable", never "missing" — a
// repository we cannot see may very well exist.
func probe(p string) (pathState, error) {
	if p == "" || !filepath.IsAbs(p) {
		return pathUnreadable, errors.New("unknown location")
	}
	fi, err := os.Stat(p)
	switch {
	case err == nil && fi.IsDir():
		return pathExists, nil
	case err == nil:
		return pathUnreadable, fmt.Errorf("%s is not a directory", p)
	case !errors.Is(err, fs.ErrNotExist):
		return pathUnreadable, err
	}
	if _, err := os.Lstat(p); err == nil { // dangling symlink: its target may be offline
		return pathUnreadable, fmt.Errorf("%s is a symlink to a missing location", p)
	}
	parts := strings.Split(strings.TrimPrefix(filepath.Clean(p), "/"), "/")
	if len(parts) >= 2 && strings.EqualFold(parts[0], "Volumes") {
		vol := "/Volumes/" + parts[1]
		if _, err := os.Lstat(vol); err != nil {
			return pathUnreadable, fmt.Errorf("volume %s is not mounted", vol)
		}
	}
	for a := filepath.Dir(p); ; a = filepath.Dir(a) {
		_, err := os.Lstat(a)
		if err == nil {
			f, err := os.Open(a)
			if err != nil {
				return pathUnreadable, err
			}
			_, err = f.Readdirnames(1)
			f.Close()
			if err != nil && err != io.EOF {
				return pathUnreadable, err
			}
			return pathMissing, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return pathUnreadable, err
		}
		if a == "/" {
			return pathUnreadable, errors.New("no existing ancestor")
		}
	}
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// resolve returns p with symlinks resolved, or p when that fails.
func resolve(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// pathMax sizes F_GETPATH buffers; a variable, so that they live on the heap
// (a heap object never moves while the kernel writes into it).
var pathMax = unix.PathMax

// onDiskPath returns the path of p as the kernel reports it (on-disk case,
// symlinks and firmlinks resolved), which is how process cwd/exec paths are
// reported, or "" when it cannot tell. Only directories are opened (never a
// file: a FIFO would block, a dataless file could be fetched); for other
// entries the parent directory is resolved and the name kept.
func onDiskPath(p string) string {
	fd, err := unix.Open(p, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		if d := filepath.Dir(p); d != p && !errors.Is(err, unix.ENOENT) {
			if dp := onDiskPath(d); dp != "" {
				return filepath.Join(dp, filepath.Base(p))
			}
		}
		return ""
	}
	defer unix.Close(fd)
	buf := make([]byte, pathMax)
	_, err = unix.FcntlInt(uintptr(fd), unix.F_GETPATH, int(uintptr(unsafe.Pointer(&buf[0]))))
	runtime.KeepAlive(buf)
	if err != nil {
		return ""
	}
	if i := bytes.IndexByte(buf, 0); i >= 0 {
		buf = buf[:i]
	}
	return string(buf)
}

// toolMarkers are the only files a tool leaves next to a worktree in its
// per-task folder (e.g. ~/.codex/worktrees/<id>/{<repo>,.codex-worktree-name}).
var toolMarkers = map[string]bool{".codex-worktree-name": true, ".DS_Store": true}

// removeEmptyToolParent deletes the per-task parent folder of a removed
// worktree (e.g. ~/.codex/worktrees/<id>) when nothing but tool marker files
// is left in it. The folder must be recognisably a tool's: directly inside a
// well-known worktree home (config.DefaultWorktreeRoots), or holding a
// tool-specific marker. An empty folder elsewhere (e.g. ~/code/wts that held
// a manual worktree) is the user's and is kept.
func removeEmptyToolParent(path string, opt Options) {
	parent := filepath.Dir(path)
	entries, err := os.ReadDir(parent)
	if err != nil {
		return
	}
	toolMarker := false
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || !toolMarkers[e.Name()] || !info.Mode().IsRegular() || info.Size() > 4096 {
			return
		}
		toolMarker = toolMarker || e.Name() != ".DS_Store"
	}
	if !toolMarker && !inToolHome(parent, opt.Home) {
		return
	}
	if opt.Guard.Check(parent, safety.Options{}) != nil {
		return
	}
	// Remove the markers one by one, then the folder with rmdir: anything a
	// tool creates there meanwhile (a new worktree) makes rmdir fail instead
	// of being deleted along.
	for _, e := range entries {
		if err := os.Remove(filepath.Join(parent, e.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return
		}
	}
	_ = os.Remove(parent)
}

// inToolHome reports whether dir sits directly inside a well-known worktree
// home of an AI tool.
func inToolHome(dir, home string) bool {
	if home == "" {
		return false
	}
	up := []string{filepath.Dir(dir), resolve(filepath.Dir(dir))}
	for _, h := range []string{home, resolve(home)} {
		for _, r := range config.DefaultWorktreeRoots {
			root := filepath.Join(h, strings.TrimPrefix(r, "~/"))
			for _, u := range up {
				if safety.Key(u) == safety.Key(root) {
					return true
				}
			}
		}
	}
	return false
}
