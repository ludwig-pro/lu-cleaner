// Package sysx wraps macOS system queries: free disk space, running
// processes, APFS local snapshots.
package sysx

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

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
	procCache map[string]bool
	procAt    time.Time
)

// Running reports which of the given process names are currently running.
// Names are compared case-insensitively against the executable basename
// (`ps -axo comm=`). The process list is cached for 2 seconds.
func Running(names ...string) []string {
	if len(names) == 0 {
		return nil
	}
	procMu.Lock()
	defer procMu.Unlock()
	if procCache == nil || time.Since(procAt) > 2*time.Second {
		procCache = map[string]bool{}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		out, err := exec.CommandContext(ctx, "ps", "-axo", "comm=").Output()
		cancel()
		if err == nil {
			for _, line := range strings.Split(string(out), "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				procCache[strings.ToLower(filepath.Base(line))] = true
				procCache[strings.ToLower(line)] = true
			}
		}
		procAt = time.Now()
	}
	var hit []string
	for _, n := range names {
		if procCache[strings.ToLower(n)] {
			hit = append(hit, n)
		}
	}
	return hit
}

// InvalidateProcesses forces the next Running call to refresh the process list.
func InvalidateProcesses() {
	procMu.Lock()
	procCache = nil
	procMu.Unlock()
}

// LocalSnapshots lists APFS local (Time Machine) snapshots of the root
// volume. They pin deleted blocks: space is not returned until they expire.
func LocalSnapshots(ctx context.Context) []string {
	out, err := exec.CommandContext(ctx, "tmutil", "listlocalsnapshots", "/").Output()
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
