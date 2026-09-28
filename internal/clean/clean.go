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
	"strings"
	"sync"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/config"
	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/safety"
	"github.com/ludwig-pro/lu-cleaner/internal/sysx"
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
	DryRun   bool
	Trash    bool // move path items to ~/.Trash instead of deleting
	Force    bool // ignore running-process guards and dirty-worktree checks
	Parallel int  // concurrent deletions (default 4)
	Guard    *safety.Guard
	Runner   core.Runner
	Home     string
	// NoHistory disables writing the history file.
	NoHistory bool
}

// Result of one item.
type Result struct {
	Item    *core.Item    `json:"item"`
	Status  Status        `json:"status"`
	Freed   int64         `json:"freed"` // estimate (item size) for done items
	Error   string        `json:"error,omitempty"`
	Message string        `json:"message,omitempty"`
	Took    time.Duration `json:"took"`
}

// Summary of a run.
type Summary struct {
	Results    []Result  `json:"results"`
	Estimated  int64     `json:"estimated_freed"`
	DiskBefore sysx.Disk `json:"disk_before"`
	DiskAfter  sysx.Disk `json:"disk_after"`
	Measured   int64     `json:"measured_freed"` // free space delta reported by the filesystem
	Trash      bool      `json:"trash"`
	DryRun     bool      `json:"dry_run"`
	Took       time.Duration
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
// selected. Path deletions run in parallel; commands and git worktree
// removals run sequentially. progress (optional) is called after each item,
// possibly from several goroutines (calls are serialized).
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

	var mu sync.Mutex
	report := func(r Result) {
		mu.Lock()
		defer mu.Unlock()
		sum.Results = append(sum.Results, r)
		if r.Status == StatusDone {
			sum.Estimated += r.Freed
		}
		if progress != nil {
			progress(r)
		}
	}

	top := core.TopLevel(items)
	var parallel, serial []*core.Item
	for _, it := range top {
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
			report(Result{Item: it, Status: StatusSkipped, Message: "cancelled"})
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(it *core.Item) {
			defer func() { <-sem; wg.Done() }()
			report(runOne(ctx, it, opt))
		}(it)
	}
	wg.Wait()
	for _, it := range serial {
		if ctx.Err() != nil {
			report(Result{Item: it, Status: StatusSkipped, Message: "cancelled"})
			continue
		}
		report(runOne(ctx, it, opt))
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

func runOne(ctx context.Context, it *core.Item, opt Options) (res Result) {
	start := time.Now()
	res = Result{Item: it}
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

	if !it.CanClean() {
		return skip("not cleanable (%s, %s)", it.Risk, it.Method)
	}
	if !opt.Force && len(it.ProcessGuard) > 0 {
		if running := sysx.Running(it.ProcessGuard...); len(running) > 0 {
			return skip("%s is running — quit it first (or use --force)", strings.Join(running, ", "))
		}
	}

	method := it.Method
	if opt.Trash && method == core.MethodDelete {
		method = core.MethodTrash
	}

	// Validate every filesystem target right now (state may have changed since the scan).
	var targets []string
	if method != core.MethodCommand {
		cwd, _ := os.Getwd()
		for _, p := range it.Targets() {
			if _, err := os.Lstat(p); errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err := opt.Guard.Check(p, safety.Options{AllowGitRepo: it.AllowGitRepo}); err != nil {
				return skip("%v", err)
			}
			if len(it.RequireSibling) > 0 && !hasSibling(p, it.RequireSibling) {
				return skip("marker %v not found next to %s anymore", it.RequireSibling, p)
			}
			if cwd != "" && within(cwd, p) {
				return skip("current directory is inside %s", p)
			}
			targets = append(targets, p)
		}
		if len(targets) == 0 {
			return skip("already gone")
		}
		if method == core.MethodWorktree && (len(targets) != 1 || targets[0] != it.Path) {
			return skip("worktree item must have exactly one path")
		}
	}

	if opt.DryRun {
		res.Status = StatusDryRun
		res.Freed = it.Freed()
		res.Message = describe(it, method)
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
			err = errors.New(strings.Join(errs, "; "))
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
		if len(moved) == 1 {
			res.Message = "moved to " + moved[0]
		} else if len(moved) > 1 {
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
		msg, err = removeWorktree(ctx, it, opt)
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
	return res
}

func describe(it *core.Item, m core.Method) string {
	switch m {
	case core.MethodCommand:
		return "would run: " + strings.Join(it.Command, " ")
	case core.MethodWorktree:
		return "would remove worktree " + it.Path
	case core.MethodTrash:
		return "would move to Trash: " + summarize(it.Targets())
	}
	return "would delete " + summarize(it.Targets())
}

func summarize(paths []string) string {
	if len(paths) == 1 {
		return paths[0]
	}
	return fmt.Sprintf("%d paths", len(paths))
}

func within(p, parent string) bool {
	return p == parent || strings.HasPrefix(p, parent+"/")
}

func hasSibling(path string, names []string) bool {
	dir := filepath.Dir(path)
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

// HistoryEntry is one line of history.jsonl.
type HistoryEntry struct {
	Time     time.Time `json:"time"`
	Kind     string    `json:"kind"`
	Category string    `json:"category"`
	Name     string    `json:"name"`
	Path     string    `json:"path,omitempty"`
	Command  string    `json:"command,omitempty"`
	Method   string    `json:"method"`
	Status   string    `json:"status"`
	Size     int64     `json:"size"`
	Error    string    `json:"error,omitempty"`
}

// HistoryPath is the JSONL file recording every cleaning action.
func HistoryPath() string { return filepath.Join(config.StateDir(), "history.jsonl") }

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
		if r.Status == StatusSkipped && r.Message == "already gone" {
			continue
		}
		method := r.Item.Method.String()
		if s.Trash && r.Item.Method == core.MethodDelete {
			method = core.MethodTrash.String()
		}
		_ = enc.Encode(HistoryEntry{
			Time: now, Kind: r.Item.Kind, Category: string(r.Item.Category), Name: r.Item.Name,
			Path: r.Item.Path, Command: strings.Join(r.Item.Command, " "), Method: method,
			Status: r.Status.String(), Size: r.Item.Size, Error: r.Error,
		})
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
