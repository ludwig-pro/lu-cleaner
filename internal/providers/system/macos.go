package system

import (
	"encoding/binary"
	"strconv"
	"strings"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
	"golang.org/x/sys/unix"
)

// snapshots reports APFS local (Time Machine) snapshots of the startup disk:
// they pin deleted blocks, so what lu-cleaner removes only comes back once
// they expire or are thinned.
func (s *scan) snapshots() {
	if !s.has("tmutil") {
		return
	}
	out, err := s.run(10*time.Second, "tmutil", "listlocalsnapshots", "/")
	if err != nil {
		return
	}
	var snaps []string
	for _, l := range strings.Split(string(out), "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "com.apple.") {
			snaps = append(snaps, l)
		}
	}
	if len(snaps) == 0 {
		return
	}
	it := s.newItem("time-machine-snapshots", core.CatSystem,
		"Time Machine local snapshots ("+strconv.Itoa(len(snaps))+")", core.RiskNever)
	it.ID = itemID(it.Kind, "/")
	it.Location = "/ (APFS local snapshots)"
	it.Method = core.MethodReport
	it.Selectable = false
	it.Meta = map[string]string{"count": strconv.Itoa(len(snaps)), "oldest": snaps[0], "newest": snaps[len(snaps)-1]}
	it.Note = "Local snapshots keep deleted files' blocks: freed space only comes back when they expire (24 h) or after `tmutil thinlocalsnapshots / 999999999999 4` (size unknown without root)."
	it.Warn = "local snapshots pin deleted data — cleaning may not free space until they are thinned"
	s.emitNow(it)
}

// macosInfo reports OS-managed space that explains a full disk but is never
// cleaned by lu-cleaner: swap files and staged macOS updates.
func (s *scan) macosInfo() {
	if total, used, ok := s.p.swapUsage(); ok && total >= 1<<30 {
		it := s.newItem("macos-swap", core.CatSystem, "Swap files (VM volume)", core.RiskNever)
		it.ID = itemID(it.Kind, "/System/Volumes/VM")
		it.Location = "/System/Volumes/VM"
		it.Method = core.MethodReport
		it.Selectable = false
		it.Size = total
		it.Meta = map[string]string{"used": fsx.Bytes(used), "total": fsx.Bytes(total)}
		it.Note = "Swap grows under memory pressure (simulators, emulators, Electron apps, AI agents) and shares the disk's free space; quit memory-heavy apps or reboot to shrink it. Never delete these files."
		s.emitNow(it)
	}
	if s.p.updatesDir != "-" && isDir(s.p.updatesDir) {
		var paths []string
		for _, e := range list(s.p.updatesDir, false) {
			if e.dir {
				paths = append(paths, e.path)
			}
		}
		if len(paths) > 0 {
			it := s.newItem("macos-staged-updates", core.CatSystem, "Staged macOS updates", core.RiskNever)
			it.ID = itemID(it.Kind, s.p.updatesDir)
			it.Location = s.p.updatesDir
			it.Note = "Software updates downloaded by macOS and waiting to be installed (managed by softwareupdate); install or skip the update to reclaim them."
			s.report(it, pubOpts{measure: paths, newest: true})
		}
	}
}

// swapUsage reads sysctl vm.swapusage (struct xsw_usage).
func swapUsage() (total, used int64, ok bool) {
	b, err := unix.SysctlRaw("vm.swapusage")
	if err != nil || len(b) < 24 {
		return 0, 0, false
	}
	total = int64(binary.LittleEndian.Uint64(b[0:8]))
	used = int64(binary.LittleEndian.Uint64(b[16:24]))
	return total, used, true
}
