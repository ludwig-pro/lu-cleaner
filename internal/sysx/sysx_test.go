package sysx

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRunningMatchesSelf(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skip(err)
	}
	InvalidateProcesses()
	base := filepath.Base(exe)
	if got := Running(base, "definitely-not-running-xyz"); len(got) != 1 || got[0] != base {
		t.Errorf("Running(%q) = %v", base, got)
	}
	if got := Running("/" + base); len(got) != 1 {
		t.Errorf("path pattern: %v", got)
	}
}

func TestDiskOf(t *testing.T) {
	d, err := DiskOf("/")
	if err != nil || d.Total <= 0 || d.Free < 0 || d.UsedPct() <= 0 {
		t.Errorf("DiskOf = %+v, %v", d, err)
	}
}

func TestCwdInside(t *testing.T) {
	wd, _ := os.Getwd()
	if pids := CwdInside(wd); pids == "" {
		t.Skip("lsof did not report our cwd (sandbox?)")
	}
	if pids := CwdInside(filepath.Join(wd, "definitely-not-a-dir-xyz")); pids != "" {
		t.Errorf("unexpected pids %s", pids)
	}
}

func TestExecInside(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skip(err)
	}
	exe, _ = filepath.EvalSymlinks(exe)
	if pids := ExecInside(filepath.Dir(exe)); pids == "" {
		t.Skip("lsof did not report our executable")
	}
}

func TestNativeProcPaths(t *testing.T) {
	cwds, execs, ok := nativeProcPaths()
	if !ok {
		t.Skip("proc_info unavailable")
	}
	wd, _ := os.Getwd()
	wd, _ = filepath.EvalSymlinks(wd)
	exe, _ := os.Executable()
	exe, _ = filepath.EvalSymlinks(exe)
	pid := itoa(os.Getpid())
	foundCwd, foundExe := false, false
	for _, c := range cwds {
		if c.pid == pid && c.path == wd {
			foundCwd = true
		}
	}
	for _, e := range execs {
		if e.pid == pid && e.path == exe {
			foundExe = true
		}
	}
	if !foundCwd || !foundExe {
		t.Errorf("own process not found: cwd=%v exe=%v (%d cwds, %d execs)", foundCwd, foundExe, len(cwds), len(execs))
	}
}
