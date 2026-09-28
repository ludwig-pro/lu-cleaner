// Package sysx wraps macOS system queries: free disk space, running
// processes, APFS local snapshots.
package sysx

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
	"golang.org/x/sys/unix"
)

// output runs a system tool and returns its stdout. WaitDelay bounds the wait
// for the pipes once the context expired (a helper holding stdout would
// otherwise keep Output blocked past the timeout).
func output(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = time.Second
	return cmd.Output()
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

var (
	procMu    sync.Mutex
	procPaths []string        // full executable paths (ps comm)
	procBase  map[string]bool // executable basenames (case-sensitive)
	procAt    time.Time
	procErr   error
)

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
	if len(patterns) == 0 {
		return nil, nil
	}
	procMu.Lock()
	defer procMu.Unlock()
	if procBase == nil || time.Since(procAt) > 2*time.Second || procErr != nil {
		procBase = map[string]bool{}
		procPaths = nil
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		out, err := output(ctx, "/bin/ps", "-axo", "comm=")
		cancel()
		procErr = err
		if err == nil && len(out) == 0 {
			procErr = errors.New("empty process list")
		}
		if err == nil {
			for _, line := range strings.Split(string(out), "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				procPaths = append(procPaths, line)
				procBase[filepath.Base(line)] = true
			}
		}
		procAt = time.Now()
	}
	var hit []string
	for _, p := range patterns {
		if strings.Contains(p, "/") {
			for _, full := range procPaths {
				if strings.Contains(full, p) {
					hit = append(hit, p)
					break
				}
			}
		} else if procBase[p] {
			hit = append(hit, p)
		}
	}
	if procErr != nil {
		return hit, fmt.Errorf("cannot list processes: %w", procErr)
	}
	return hit, nil
}

// InvalidateProcesses forces the next Running call to refresh the process list.
func InvalidateProcesses() {
	procMu.Lock()
	procBase = nil
	procMu.Unlock()
}

// LocalSnapshots lists APFS local (Time Machine) snapshots of the root
// volume. They pin deleted blocks: space is not returned until they expire.
func LocalSnapshots(ctx context.Context) []string {
	out, err := output(ctx, "/usr/bin/tmutil", "listlocalsnapshots", "/")
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
	lsofMu    sync.Mutex
	lsofCache = map[string]*lsofSnap{}
)

type lsofSnap struct {
	at    time.Time
	procs []procPath
}

type procPath struct {
	pid  string
	path string
	key  string // fsx.FoldPath(path), filled when cached
}

// newSnap caches procs with their comparison keys.
func newSnap(procs []procPath) *lsofSnap {
	for i := range procs {
		procs[i].key = fsx.FoldPath(procs[i].path)
	}
	return &lsofSnap{at: time.Now(), procs: procs}
}

// openFDs lists (pid, path) pairs for one class: "cwd" = current
// directories, "txt" = executables, loaded libraries and other mapped files.
// It uses proc_info(2) natively and falls back to a system-wide
// `lsof -d <fd> -Fpn`; cached for 5 seconds.
func openFDs(fd string) []procPath {
	lsofMu.Lock()
	defer lsofMu.Unlock()
	if c := lsofCache[fd]; c != nil && time.Since(c.at) < 5*time.Second {
		return c.procs
	}
	native := nativeCwds
	if fd == "txt" {
		native = nativeExecs
	}
	if procs, ok := native(); ok {
		lsofCache[fd] = newSnap(procs)
		return procs
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	out, _ := output(ctx, "/usr/sbin/lsof", "-w", "-n", "-P", "-d", fd, "-Fpn")
	cancel()
	var procs []procPath
	pid := ""
	for _, l := range strings.Split(string(out), "\n") {
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
	lsofCache[fd] = newSnap(procs)
	return procs
}

// invalidateFDs drops the cached process paths (tests).
func invalidateFDs() {
	lsofMu.Lock()
	clear(lsofCache)
	lsofMu.Unlock()
}

func pidsInside(fd, dir string) string {
	dirs := []string{fsx.FoldPath(filepath.Clean(dir))}
	if real, err := filepath.EvalSymlinks(dir); err == nil && real != dir {
		dirs = append(dirs, fsx.FoldPath(real)) // the kernel reports resolved paths (/private/var/…)
	}
	var pids []string
	seen := map[string]bool{}
	for _, c := range openFDs(fd) {
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
	return strings.Join(pids, ",")
}

// CwdInside returns the PIDs (comma separated) of processes whose current
// directory is dir or below it ("" if none).
func CwdInside(dir string) string { return pidsInside("cwd", dir) }

// ExecInside returns the PIDs (comma separated) of processes running an
// executable, or having a library (e.g. a native .node addon) or another
// file mapped in memory, located in dir.
func ExecInside(dir string) string { return pidsInside("txt", dir) }
