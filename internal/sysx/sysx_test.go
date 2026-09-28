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
