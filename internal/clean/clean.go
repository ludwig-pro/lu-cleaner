// Package clean executes the removal of selected items, with every safety
// check re-evaluated at the last moment, and records a history.
package clean

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/config"
	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
	"github.com/ludwig-pro/lu-cleaner/internal/safety"
	"github.com/ludwig-pro/lu-cleaner/internal/sysx"
	"golang.org/x/sys/unix"
)

// Status of one item after execution.
type Status int

const (
	StatusDone Status = iota
	StatusDryRun
	StatusSkipped
	StatusFailed
)

func (s Status) String() string {
	return [...]string{"done", "dry-run", "skipped", "failed"}[s]
}

func (s Status) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

// Options control execution.
type Options struct {
	DryRun bool
	// Trash moves filesystem path items to ~/.Trash instead of deleting them.
	// Worktree and command items are then skipped (they would delete
	// permanently), and so are items already inside the Trash.
	Trash    bool
	Force    bool // ignore running-process guards and dirty-worktree checks
	Parallel int  // concurrent deletions (default 4)
	Guard    *safety.Guard
	Runner   core.Runner
	Home     string
	// NoHistory disables writing the history file.
	NoHistory bool
}

// Messages of the Trash-mode skips.
const (
	msgTrashPermanent = "not possible in Trash mode (it would delete permanently)"
	msgAlreadyTrashed = "already in the Trash"
	msgGone           = "already gone"
)

// Result of one item.
type Result struct {
	Item   *core.Item `json:"item"`
	Status Status     `json:"status"`
	// Method is the method actually used (or that would be, in dry-run):
	// MethodTrash for a delete item cleaned in Trash mode.
	Method core.Method `json:"method"`
	// Freed is the estimated space freed (item size) for done and dry-run
	// items, and what was freed before a partial failure. A move to the
	// Trash frees nothing: its size goes to Trashed instead.
	Freed   int64         `json:"freed"`
	Trashed int64         `json:"trashed,omitempty"` // size moved to the Trash (not freed until it is emptied)
	Error   string        `json:"error,omitempty"`
	Message string        `json:"message,omitempty"`
	Took    time.Duration `json:"took"`
}

// Summary of a run.
type Summary struct {
	Results []Result `json:"results"`
	// Estimated is the space really freed (done items, plus what partial
	// failures freed). Moves to the Trash are never counted: see Trashed.
	Estimated  int64         `json:"estimated_freed"`
	Trashed    int64         `json:"trashed"` // size moved to the Trash (freed only once it is emptied)
	DiskBefore sysx.Disk     `json:"disk_before"`
	DiskAfter  sysx.Disk     `json:"disk_after"`
	Measured   int64         `json:"measured_freed"` // free space delta reported by the filesystem
	Trash      bool          `json:"trash"`
	DryRun     bool          `json:"dry_run"`
	Took       time.Duration `json:"took"`
}

// Count returns how many results have status s.
func (s *Summary) Count(st Status) int {
	n := 0
	for _, r := range s.Results {
		if r.Status == st {
			n++
		}
	}
	return n
}

// Run cleans items. Nested items are skipped when an ancestor is also
// selected, except worktrees nested in a selected worktree: they are removed
// first, each with its own checks, and the outer worktree is kept when one of
// them is not removed. Path deletions run in parallel; commands and git
// worktree removals run sequentially. progress (optional) is called after
// each item, possibly from several goroutines (calls are serialized).
func Run(ctx context.Context, items []*core.Item, opt Options, progress func(Result)) *Summary {
	start := time.Now()
	if opt.Parallel <= 0 {
		opt.Parallel = 4
	}
	if opt.Runner == nil {
		opt.Runner = core.ExecRunner{}
	}
	if opt.Home == "" {
		opt.Home, _ = os.UserHomeDir()
	}
	sum := &Summary{Trash: opt.Trash, DryRun: opt.DryRun}
	sum.DiskBefore, _ = sysx.DiskOf(opt.Home)
	sysx.InvalidateProcesses() // busy checks must not reuse the scan's snapshot

	var mu sync.Mutex
	report := func(r Result) {
		mu.Lock()
		defer mu.Unlock()
		sum.Results = append(sum.Results, r)
		switch r.Status {
		case StatusDone:
			sum.Estimated += r.Freed
			sum.Trashed += r.Trashed
		case StatusFailed:
			sum.Estimated += r.Freed // freed before the failure
		}
		if progress != nil {
			progress(r)
		}
	}
	cancelled := func(it *core.Item) Result {
		return Result{Item: it, Method: it.Method, Status: StatusSkipped, Message: "cancelled"}
	}

	var parallel, serial []*core.Item
	for _, it := range plan(items, opt) {
		if it.Method == core.MethodDelete || it.Method == core.MethodTrash {
			parallel = append(parallel, it)
		} else {
			serial = append(serial, it)
		}
	}

	sem := make(chan struct{}, opt.Parallel)
	var wg sync.WaitGroup
	for _, it := range parallel {
		if ctx.Err() != nil {
			report(cancelled(it))
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(it *core.Item) {
			defer func() { <-sem; wg.Done() }()
			report(runOne(ctx, it, opt, nil))
		}(it)
	}
	wg.Wait()

	// Worktrees are processed deepest first; removed ones (or, in dry-run,
	// the ones that would be) no longer block an outer worktree, kept ones do.
	removed := map[string]bool{}
	var kept []string
	isRemoved := func(p string) bool { return removed[safety.Key(p)] }
	for _, it := range serial {
		if ctx.Err() != nil {
			report(cancelled(it))
			continue
		}
		if it.Method != core.MethodWorktree || opt.Trash {
			report(runOne(ctx, it, opt, nil))
			continue
		}
		var r Result
		if inner := firstInside(kept, it.Path); inner != "" {
			r = Result{Item: it, Method: it.Method, Status: StatusSkipped,
				Message: "the worktree " + inner + " nested inside it was not removed — keeping this one too"}
		} else {
			r = runOne(ctx, it, opt, isRemoved)
		}
		if r.Status == StatusDone || r.Status == StatusDryRun || (r.Status == StatusSkipped && r.Message == msgGone) {
			removed[safety.Key(it.Path)] = true
			removed[safety.Key(resolve(it.Path))] = true
		} else {
			kept = append(kept, it.Path)
		}
		report(r)
	}

	sysx.InvalidateProcesses()
	sum.DiskAfter, _ = sysx.DiskOf(opt.Home)
	sum.Measured = sum.DiskAfter.Free - sum.DiskBefore.Free
	sum.Took = time.Since(start)
	if !opt.DryRun && !opt.NoHistory {
		_ = appendHistory(sum)
	}
	return sum
}

// plan orders the items of a run: core.TopLevel, except that a worktree
// nested in another selected worktree is kept (removing the outer one would
// silently skip its own checks), and that in Trash mode the items that will
// be skipped (worktrees, commands) do not make other items redundant.
// Worktrees come deepest first.
func plan(items []*core.Item, opt Options) []*core.Item {
	var rest, skipped []*core.Item
	for _, it := range items {
		if opt.Trash && (it.Method == core.MethodWorktree || it.Method == core.MethodCommand) {
			skipped = append(skipped, it)
		} else {
			rest = append(rest, it)
		}
	}
	var wts []*core.Item
	for _, it := range rest {
		if it.Method == core.MethodWorktree && it.Path != "" {
			wts = append(wts, it)
		}
	}
	nested := map[*core.Item]bool{}
	for _, a := range wts {
		for _, b := range wts {
			if a != b && safety.Key(a.Path) != safety.Key(b.Path) && safety.Within(b.Path, a.Path) {
				nested[b] = true
			}
		}
	}
	var outer, inner []*core.Item
	for _, it := range rest {
		if nested[it] {
			inner = append(inner, it)
		} else {
			outer = append(outer, it)
		}
	}
	out := core.TopLevel(outer)
	seen := map[string]bool{}
	for _, it := range inner {
		if k := safety.Key(it.Path); !seen[k] {
			seen[k] = true
			out = append(out, it)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		wi, wj := out[i].Method == core.MethodWorktree, out[j].Method == core.MethodWorktree
		if wi && wj {
			return strings.Count(out[i].Path, "/") > strings.Count(out[j].Path, "/")
		}
		return wi && !wj
	})
	return append(out, core.TopLevel(skipped)...)
}

// firstInside returns the first of paths strictly inside dir ("" if none).
func firstInside(paths []string, dir string) string {
	for _, p := range paths {
		if safety.Key(p) != safety.Key(dir) && safety.Within(p, dir) {
			return p
		}
	}
	return ""
}

// runOne cleans one item. removed (worktrees only, may be nil) reports the
// worktrees this run already removed.
func runOne(ctx context.Context, it *core.Item, opt Options, removed func(string) bool) (res Result) {
	start := time.Now()
	res = Result{Item: it, Method: it.Method}
	defer func() { res.Took = time.Since(start) }()
	skip := func(format string, a ...any) Result {
		res.Status = StatusSkipped
		res.Message = fmt.Sprintf(format, a...)
		return res
	}
	fail := func(err error) Result {
		res.Status = StatusFailed
		res.Error = err.Error()
		return res
	}
	if removed == nil {
		removed = func(string) bool { return false }
	}

	if !it.CanClean() {
		return skip("not cleanable (%s, %s)", it.Risk, it.Method)
	}
	trashDir := filepath.Join(opt.Home, ".Trash")
	if opt.Trash {
		if it.Method == core.MethodWorktree || it.Method == core.MethodCommand {
			return skip("%s", msgTrashPermanent)
		}
		for _, p := range it.Targets() {
			if opt.Home != "" && inside(p, trashDir) {
				return skip("%s", msgAlreadyTrashed)
			}
		}
	}
	if !opt.Force && len(it.ProcessGuard) > 0 {
		running, err := sysx.RunningStrict(it.ProcessGuard...)
		if err != nil {
			return skip("cannot verify that %s is closed: %v", strings.Join(it.ProcessGuard, ", "), err)
		}
		if len(running) > 0 {
			return skip("%s is running — quit it first (or use --force)", strings.Join(running, ", "))
		}
	}

	if it.Recheck != nil {
		if err := it.Recheck(ctx); err != nil {
			return skip("%v", err)
		}
	}

	method := it.Method
	if opt.Trash && method == core.MethodDelete {
		method = core.MethodTrash
	}
	res.Method = method

	// Validate every filesystem target right now (state may have changed since the scan).
	var targets []string
	if method != core.MethodCommand {
		if opt.Guard == nil {
			return skip("no safety guard — refusing")
		}
		cwd, _ := os.Getwd()
		var mounts []string
		var mountsErr error
		mountsRead := false
		for i, p := range it.Targets() {
			if _, err := os.Lstat(p); errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if i < len(it.Inodes) && it.Inodes[i] != 0 && inode(p) != it.Inodes[i] {
				return skip("%s changed since the scan (different inode) — rescan", p)
			}
			if isSQLiteFile(p) {
				pids, err := openBy(ctx, p)
				if err != nil {
					return skip("cannot verify that %s is closed: %v", p, err)
				}
				if pids != "" {
					return skip("%s is open by process %s — quit the app first", p, pids)
				}
			}
			// git itself removes a worktree (with its .git file) after our checks.
			allowRepo := it.AllowGitRepo || method == core.MethodWorktree
			if err := opt.Guard.Check(p, safety.Options{AllowGitRepo: allowRepo}); err != nil {
				return skip("%v", err)
			}
			if len(it.RequireSibling) > 0 && !hasSibling(p, it.RequireSibling) {
				return skip("marker %v not found next to %s anymore", it.RequireSibling, p)
			}
			disk := onDiskPath(p)
			if cwd != "" && (inside(cwd, p) || (disk != "" && inside(cwd, disk))) {
				return skip("current directory is inside %s", p)
			}
			if msg := removable(p); msg != "" {
				return skip("%s", msg)
			}
			if !mountsRead {
				mounts, mountsErr = mountPoints()
				mountsRead = true
			}
			if mountsErr != nil {
				return skip("cannot list the mounted volumes (%v) — refusing", mountsErr)
			}
			if m := mountInside(mounts, p, disk); m != "" {
				return skip("%s contains the mount point %s (mounted volume or disk image) — eject it first", p, m)
			}
			if !opt.Force {
				if pids := busy(sysx.CwdInside, p, disk); pids != "" {
					return skip("in use: process %s is working inside %s", pids, p)
				}
				if pids := busy(sysx.ExecInside, p, disk); pids != "" {
					return skip("in use: process %s runs a program or library from %s", pids, p)
				}
			}
			targets = append(targets, p)
		}
		if len(targets) == 0 {
			return skip("%s", msgGone)
		}
		if method == core.MethodWorktree && (len(targets) != 1 || targets[0] != it.Path) {
			return skip("worktree item must have exactly one path")
		}
	}

	var wt *wtPlan
	if method == core.MethodWorktree {
		var msg string
		var err error
		if wt, msg, err = precheckWorktree(ctx, it, opt, removed); err != nil {
			return skip("%s", msg)
		}
	}

	if opt.DryRun {
		res.Status = StatusDryRun
		res.Message = describe(it, method, wt)
		res.Freed = it.Freed()
		if method == core.MethodTrash {
			res.Freed, res.Trashed = 0, it.Freed()
		}
		return res
	}

	var err error
	switch method {
	case core.MethodDelete:
		var errs []string
		for _, p := range targets {
			if e := RemoveAll(p); e != nil {
				errs = append(errs, e.Error())
			}
		}
		if len(errs) > 0 {
			res.Freed, res.Message = partial(ctx, it, targets)
			return fail(errors.New(strings.Join(errs, "; ")))
		}
	case core.MethodTrash:
		var moved []string
		for _, p := range targets {
			dst, e := MoveToTrash(opt.Home, p)
			if e != nil {
				err = e
				break
			}
			moved = append(moved, dst)
		}
		switch {
		case err != nil && len(moved) > 0:
			res.Message = fmt.Sprintf("moved %d of %d paths to the Trash before failing", len(moved), len(targets))
		case len(moved) == 1:
			res.Message = "moved to " + moved[0]
		case len(moved) > 1:
			res.Message = fmt.Sprintf("moved %d paths to the Trash", len(moved))
		}
	case core.MethodCommand:
		if len(it.Command) == 0 {
			return fail(errors.New("no command"))
		}
		var out string
		out, err = runCmd(ctx, opt.Runner, "", it.Command)
		res.Message = lastLine(out)
	case core.MethodWorktree:
		var msg string
		msg, err = removeWorktree(ctx, it, opt, wt)
		if errors.Is(err, errSkip) {
			return skip("%s", msg)
		}
		res.Message = msg
	default:
		return skip("method %s not executable", method)
	}
	if err != nil {
		return fail(err)
	}
	for _, pc := range it.PostCommands {
		if _, perr := runCmd(ctx, opt.Runner, "", pc); perr != nil {
			res.Message = strings.TrimSpace(res.Message + " (post: " + perr.Error() + ")")
		}
	}
	res.Status = StatusDone
	res.Freed = it.Freed()
	if method == core.MethodTrash {
		res.Freed, res.Trashed = 0, it.Freed()
	}
	return res
}

// partial measures what a failed deletion left behind and returns the space
// it freed anyway with a message ("", 0 when nothing was freed).
func partial(ctx context.Context, it *core.Item, targets []string) (int64, string) {
	var left int64
	for _, p := range targets {
		// A Skip function bypasses any size cache carried by ctx: measure the disk now.
		st, err := fsx.Size(ctx, p, &fsx.Options{Skip: func(string, string) bool { return false }})
		if err == nil || !errors.Is(err, fs.ErrNotExist) {
			left += st.Bytes
		}
	}
	freed := min(max(it.Size-left, 0), it.Freed())
	if freed <= 0 {
		return 0, ""
	}
	return freed, fmt.Sprintf("partially removed: %s freed, %s left", fsx.Bytes(freed), fsx.Bytes(left))
}

// removable returns why p cannot be unlinked from its parent directory ("" if
// it can): checked before anything is touched, so that a read-only parent
// does not leave the target half deleted (its contents gone, the empty
// directory failing again on every run). A mount point is never a target.
func removable(p string) string {
	dir := filepath.Dir(p)
	if err := unix.Access(dir, unix.W_OK|unix.X_OK); err != nil {
		return fmt.Sprintf("parent directory %s is read-only (%v) — cannot remove %s, nothing was touched", dir, err, filepath.Base(p))
	}
	var pst, st unix.Stat_t
	if unix.Stat(dir, &pst) != nil || unix.Lstat(p, &st) != nil {
		return ""
	}
	if st.Dev != pst.Dev {
		return fmt.Sprintf("%s is a mount point (mounted volume or disk image) — eject it instead", p)
	}
	if pst.Mode&unix.S_ISVTX != 0 {
		if euid := uint32(os.Geteuid()); euid != 0 && st.Uid != euid && pst.Uid != euid {
			return fmt.Sprintf("%s belongs to another user in the sticky directory %s — cannot remove it", filepath.Base(p), dir)
		}
	}
	return ""
}

// mountPoints lists the mount-on paths of the mounted filesystems, as the
// kernel reports them (on-disk case, firmlinks resolved). A variable for tests.
var mountPoints = func() ([]string, error) {
	n, err := unix.Getfsstat(nil, unix.MNT_NOWAIT)
	if err != nil {
		return nil, err
	}
	buf := make([]unix.Statfs_t, n+16) // room for volumes mounted meanwhile
	if n, err = unix.Getfsstat(buf, unix.MNT_NOWAIT); err != nil {
		return nil, err
	}
	out := make([]string, 0, n)
	for _, st := range buf[:n] {
		out = append(out, unix.ByteSliceToString(st.Mntonname[:]))
	}
	return out, nil
}

// mountInside returns the first of mounts strictly inside p ("" if none):
// removing p (rm -rf, git worktree remove) would descend into that volume and
// wipe data the user never selected. p is tried as given, with symlinks
// resolved and in its on-disk spelling disk, ignoring case and Unicode form.
func mountInside(mounts []string, p, disk string) string {
	forms := []string{p, resolve(p)}
	if disk != "" {
		forms = append(forms, disk)
	}
	for _, m := range mounts {
		cands := []string{m}
		if rest, ok := strings.CutPrefix(m, "/System/Volumes/Data/"); ok {
			cands = append(cands, "/"+rest) // firmlinked spelling (/Users/…)
		}
		for _, c := range cands {
			for _, f := range forms {
				if safety.Key(c) != safety.Key(f) && safety.Within(c, f) {
					return m
				}
			}
		}
	}
	return ""
}

// inside reports whether p is dir or below it, ignoring case and Unicode
// normalization (APFS) and resolving symlinks on both sides.
func inside(p, dir string) bool {
	return safety.Within(p, dir) || safety.Within(resolve(p), resolveParent(dir))
}

// resolveParent resolves the symlinks of p's parent chain (not of p itself).
func resolveParent(p string) string {
	return filepath.Join(resolve(filepath.Dir(p)), filepath.Base(p))
}

// busy runs a process lookup (sysx.CwdInside / ExecInside) on p and, when it
// differs, on its on-disk spelling: the kernel reports process paths with
// their on-disk case, while p may carry the case of a root typed by the user.
func busy(lookup func(string) string, p, disk string) string {
	if pids := lookup(p); pids != "" {
		return pids
	}
	if disk != "" && disk != p && disk != resolveParent(p) {
		return lookup(disk)
	}
	return ""
}

func describe(it *core.Item, m core.Method, wt *wtPlan) string {
	switch m {
	case core.MethodCommand:
		return "would run: " + strings.Join(it.Command, " ")
	case core.MethodWorktree:
		if wt != nil && !wt.mainOK {
			return "would remove orphaned worktree directory " + it.Path
		}
		return "would remove worktree " + it.Path
	case core.MethodTrash:
		return "would move to Trash: " + summarize(it.Targets())
	}
	return "would delete " + summarize(it.Targets())
}

func inode(p string) uint64 {
	var st unix.Stat_t
	if unix.Lstat(p, &st) != nil {
		return 0
	}
	return st.Ino
}

// isSQLiteFile reports database files that must not be removed while open.
func isSQLiteFile(p string) bool {
	b := strings.ToLower(filepath.Base(p))
	for _, suf := range []string{".sqlite", ".sqlite3", ".db", "-wal", "-shm", "-journal", ".vscdb"} {
		if strings.HasSuffix(b, suf) {
			return true
		}
	}
	return false
}

// openBy returns the PIDs (comma separated) holding path open ("" if none).
// Fail-closed: any lsof failure other than "no match" (exit 1) is an error.
func openBy(ctx context.Context, path string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "/usr/sbin/lsof", "-w", "-t", "--", path).Output()
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 1 || cctx.Err() != nil {
			return "", fmt.Errorf("lsof: %w", err)
		}
	}
	return strings.Join(strings.Fields(string(out)), ","), nil
}

func summarize(paths []string) string {
	if len(paths) == 1 {
		return paths[0]
	}
	return fmt.Sprintf("%d paths", len(paths))
}

// globEscape escapes the filepath.Match metacharacters of a literal path.
func globEscape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\', '*', '?', '[':
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// hasSibling reports whether one of the marker names (globs, possibly with a
// "../" prefix) exists next to path. The directory part is literal: a project
// folder named "[wip] shop" or "app?" must not be read as a pattern.
func hasSibling(path string, names []string) bool {
	dir := globEscape(filepath.Dir(path))
	for _, n := range names {
		if matches, _ := filepath.Glob(filepath.Join(dir, n)); len(matches) > 0 {
			return true
		}
	}
	return false
}

func runCmd(ctx context.Context, r core.Runner, dir string, argv []string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	out, err := r.Output(cctx, dir, argv[0], argv[1:]...)
	if err != nil {
		return string(out), fmt.Errorf("%s: %w%s", strings.Join(argv, " "), err, stderrOf(err))
	}
	return string(out), nil
}

func stderrOf(err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(ee.Stderr) > 0 {
		return ": " + lastLine(string(ee.Stderr))
	}
	return ""
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		s = s[i+1:]
	}
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}

// ---------------------------------------------------------------- history

// maxHistoryPaths caps the paths recorded for one group item.
const maxHistoryPaths = 50

// HistoryEntry is one line of history.jsonl.
type HistoryEntry struct {
	Time     time.Time `json:"time"`
	Kind     string    `json:"kind"`
	Category string    `json:"category"`
	Name     string    `json:"name"`
	Path     string    `json:"path,omitempty"`
	// Location is the display location of a group or command item (Path empty).
	Location string `json:"location,omitempty"`
	// Paths are the first targets of a group item (at most 50; Count has the total).
	Paths   []string `json:"paths,omitempty"`
	Count   int      `json:"count,omitempty"` // number of targets of a group item
	Command string   `json:"command,omitempty"`
	// Method is the method actually used ("trash" for a delete item moved to
	// the Trash: its Size was not freed).
	Method  string `json:"method"`
	Status  string `json:"status"`
	Size    int64  `json:"size"`
	Error   string `json:"error,omitempty"`
	Message string `json:"message,omitempty"` // outcome details, e.g. why an item was skipped
}

// HistoryPath is the JSONL file recording every cleaning action.
func HistoryPath() string { return filepath.Join(config.StateDir(), "history.jsonl") }

// historyEntry builds the history line of one result.
func historyEntry(now time.Time, r Result) HistoryEntry {
	it := r.Item
	e := HistoryEntry{
		Time: now, Kind: it.Kind, Category: string(it.Category), Name: it.Name,
		Path: it.Path, Location: it.Location, Command: strings.Join(it.Command, " "),
		Method: r.Method.String(), Status: r.Status.String(), Size: it.Size,
		Error: r.Error, Message: r.Message,
	}
	if len(it.Paths) > 0 {
		e.Count = len(it.Paths)
		e.Paths = append([]string(nil), it.Paths[:min(len(it.Paths), maxHistoryPaths)]...)
	}
	return e
}

func appendHistory(s *Summary) error {
	if err := os.MkdirAll(config.StateDir(), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(HistoryPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	now := time.Now()
	for _, r := range s.Results {
		if r.Item == nil || (r.Status == StatusSkipped && r.Message == msgGone) {
			continue
		}
		_ = enc.Encode(historyEntry(now, r))
	}
	return nil
}

// ReadHistory returns all history entries (oldest first).
func ReadHistory() ([]HistoryEntry, error) {
	data, err := os.ReadFile(HistoryPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []HistoryEntry
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e HistoryEntry
		if json.Unmarshal([]byte(line), &e) == nil {
			out = append(out, e)
		}
	}
	return out, nil
}
