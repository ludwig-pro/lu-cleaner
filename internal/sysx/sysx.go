// Package sysx wraps macOS system queries: free disk space, running
// processes, APFS local snapshots.
package sysx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
	"github.com/ludwig-pro/lu-cleaner/internal/scanctl"
	"golang.org/x/sys/unix"
)

// output runs a system tool and returns its stdout. WaitDelay bounds the wait
// for the pipes once the context expired (a helper holding stdout would
// otherwise keep Output blocked past the timeout).
func output(ctx context.Context, timeout time.Duration, name string, args ...string) ([]byte, error) {
	return scanctl.Command(ctx, timeout, func(ctx context.Context) ([]byte, error) {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error {
			if err := unix.Kill(-cmd.Process.Pid, unix.SIGKILL); err != nil {
				if errors.Is(err, unix.ESRCH) {
					return os.ErrProcessDone
				}
				return err
			}
			return nil
		}
		cmd.WaitDelay = time.Second
		out, err := cmd.Output()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return out, err
	})
}

// Disk describes the volume holding a path.
type Disk struct {
	Total int64 `json:"total"`
	Free  int64 `json:"free"` // available to the user (f_bavail)
	Used  int64 `json:"used"`
}

// UsedPct returns the used percentage (0-100).
func (d Disk) UsedPct() float64 {
	if d.Total == 0 {
		return 0
	}
	return float64(d.Total-d.Free) / float64(d.Total) * 100
}

// DiskOf returns space information for the volume containing path.
func DiskOf(path string) (Disk, error) {
	var s unix.Statfs_t
	if err := unix.Statfs(path, &s); err != nil {
		return Disk{}, err
	}
	bs := int64(s.Bsize)
	d := Disk{Total: int64(s.Blocks) * bs, Free: int64(s.Bavail) * bs}
	d.Used = d.Total - int64(s.Bfree)*bs
	return d, nil
}

type processList struct {
	paths []string
	base  map[string]bool
}

var processes probeCache[processList]

// A hook for tests; the cache only receives complete process lists.
var listProcesses = readProcesses

func readProcesses(ctx context.Context) (processList, error) {
	out, err := output(ctx, 3*time.Second, "/bin/ps", "-axo", "comm=")
	if err != nil {
		return processList{}, fmt.Errorf("cannot list processes: %w", err)
	}
	list := processList{base: map[string]bool{}}
	for _, line := range strings.Split(string(out), "\n") {
		if err := ctx.Err(); err != nil {
			return processList{}, err
		}
		line = strings.TrimSpace(line)
		if line != "" {
			list.paths = append(list.paths, line)
			list.base[filepath.Base(line)] = true
		}
	}
	if len(list.paths) == 0 {
		return processList{}, errors.New("cannot list processes: empty process list")
	}
	return list, nil
}

// Running reports which of the given process patterns are currently running.
//
// A pattern without "/" matches an executable basename exactly and
// case-sensitively ("Claude" is the desktop app, "claude" the CLI, "Xcode",
// "Simulator", "Cursor"). A pattern containing "/" matches as a substring of
// the full executable path (e.g. "/Cursor.app/Contents/MacOS/",
// "Codex Framework.framework"). The process list is cached for 2 seconds.
func Running(patterns ...string) []string {
	hit, _ := RunningStrict(patterns...)
	return hit
}

// RunningStrict is Running but reports an error when the process list could
// not be read: callers about to delete must then treat the state as unknown
// and refuse (mole-style tri-state probe).
func RunningStrict(patterns ...string) ([]string, error) {
	return RunningContext(context.Background(), patterns...)
}

// RunningContext is RunningStrict with cancellation, including while waiting
// for another caller's process-list refresh.
func RunningContext(ctx context.Context, patterns ...string) ([]string, error) {
	ctx = scanctl.Ensure(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(patterns) == 0 {
		return nil, nil
	}
	list, err := processes.get(ctx, 2*time.Second, listProcesses)
	if err != nil {
		return nil, err
	}
	var hit []string
	for _, p := range patterns {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if strings.Contains(p, "/") {
			for _, full := range list.paths {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				if strings.Contains(full, p) {
					hit = append(hit, p)
					break
				}
			}
		} else if list.base[p] {
			hit = append(hit, p)
		}
	}
	return hit, ctx.Err()
}

// InvalidateProcesses forces the next Running call to refresh the process list.
func InvalidateProcesses() {
	processes.invalidate()
}

// LocalSnapshots lists APFS local (Time Machine) snapshots of the root
// volume. They pin deleted blocks: space is not returned until they expire.
func LocalSnapshots(ctx context.Context) []string {
	ctx = scanctl.Ensure(ctx)
	out, err := output(ctx, 15*time.Second, "/usr/bin/tmutil", "listlocalsnapshots", "/")
	if err != nil {
		return nil
	}
	var snaps []string
	for _, l := range strings.Split(string(out), "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "com.apple.") {
			snaps = append(snaps, l)
		}
	}
	return snaps
}

var (
	cwdPaths     probeCache[[]procPath]
	execPaths    probeCache[[]procPath]
	inspectCwds  = nativeCwds
	inspectExecs = nativeExecs
)

type procPath struct {
	pid  string
	path string
	key  string // fsx.FoldPath(path), filled when cached
}

// newSnap caches procs with their comparison keys.
func newSnap(ctx context.Context, procs []procPath) ([]procPath, error) {
	for i := range procs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		procs[i].key = fsx.FoldPath(procs[i].path)
	}
	return procs, ctx.Err()
}

// openFDs lists (pid, path) pairs for one class: "cwd" = current
// directories, "txt" = executables, loaded libraries and other mapped files.
// It uses proc_info(2) natively and falls back to a system-wide
// `lsof -d <fd> -Fpn`; cached for 5 seconds.
func openFDs(ctx context.Context, fd string) ([]procPath, error) {
	cache, native := &cwdPaths, inspectCwds
	if fd == "txt" {
		cache, native = &execPaths, inspectExecs
	}
	return cache.get(ctx, 5*time.Second, func(ctx context.Context) ([]procPath, error) {
		procs, ok, err := native(ctx)
		if err != nil {
			return nil, err
		}
		if ok {
			return newSnap(ctx, procs)
		}
		out, err := output(ctx, 15*time.Second, "/usr/sbin/lsof", "-w", "-n", "-P", "-d", fd, "-Fpn")
		if err != nil {
			return nil, fmt.Errorf("cannot list process %s paths: %w", fd, err)
		}
		pid := ""
		for _, l := range strings.Split(string(out), "\n") {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if len(l) < 2 {
				continue
			}
			switch l[0] {
			case 'p':
				pid = l[1:]
			case 'n':
				procs = append(procs, procPath{pid: pid, path: l[1:]})
			}
		}
		return newSnap(ctx, procs)
	})
}

// invalidateFDs drops the cached process paths (tests).
func invalidateFDs() {
	cwdPaths.invalidate()
	execPaths.invalidate()
}

func pidsInside(ctx context.Context, fd, dir string) (string, error) {
	ctx = scanctl.Ensure(ctx)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	dirs := []string{fsx.FoldPath(filepath.Clean(dir))}
	var real string
	err := scanctl.DoIO(ctx, func() error {
		var err error
		real, err = filepath.EvalSymlinks(dir)
		return err
	})
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err == nil && real != dir {
		dirs = append(dirs, fsx.FoldPath(real)) // the kernel reports resolved paths (/private/var/…)
	}
	procs, err := openFDs(ctx, fd)
	if err != nil {
		return "", err
	}
	var pids []string
	seen := map[string]bool{}
	for _, c := range procs {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if seen[c.pid] {
			continue
		}
		// Case- and normalization-insensitive, like APFS: a target spelled
		// "~/Dev/App" is busy when a process works in "~/dev/app".
		inside := false
		for _, d := range dirs {
			if fsx.Within(c.key, d) {
				inside = true
				break
			}
		}
		if inside {
			seen[c.pid] = true
			pids = append(pids, c.pid)
		}
	}
	return strings.Join(pids, ","), ctx.Err()
}

// CwdInside returns the PIDs (comma separated) of processes whose current
// directory is dir or below it ("" if none).
func CwdInside(dir string) string {
	pids, _ := CwdInsideContext(context.Background(), dir)
	return pids
}

// CwdInsideContext reports an error when inspection is incomplete or cancelled.
func CwdInsideContext(ctx context.Context, dir string) (string, error) {
	return pidsInside(ctx, "cwd", dir)
}

// ExecInside returns the PIDs (comma separated) of processes running an
// executable, or having a library (e.g. a native .node addon) or another
// file mapped in memory, located in dir.
func ExecInside(dir string) string {
	pids, _ := ExecInsideContext(context.Background(), dir)
	return pids
}

// ExecInsideContext reports an error when inspection is incomplete or cancelled.
func ExecInsideContext(ctx context.Context, dir string) (string, error) {
	return pidsInside(ctx, "txt", dir)
}
