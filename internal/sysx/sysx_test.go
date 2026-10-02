package sysx

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
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
	cwds, ok, _ := nativeCwds(context.Background())
	if !ok {
		t.Skip("proc_info unavailable")
	}
	execs, ok, _ := nativeExecs(context.Background())
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

// A file mapped by a running process (a native .node addon, a dylib, an
// mmapped database) makes its directory busy: only the main executable was
// checked, so node_modules was deleted under a dev server that had an addon
// loaded from it.
func TestExecInsideSeesMappedFiles(t *testing.T) {
	if _, ok, _ := nativeExecs(context.Background()); !ok {
		t.Skip("proc_info unavailable")
	}
	dir := t.TempDir()
	nm := filepath.Join(dir, "node_modules")
	addon := filepath.Join(nm, "addon", "build", "Release", "addon.node")
	if err := os.MkdirAll(filepath.Dir(addon), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(addon, make([]byte, 64<<10), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(addon)
	if err != nil {
		t.Fatal(err)
	}
	mem, err := unix.Mmap(int(f.Fd()), 0, 64<<10, unix.PROT_READ, unix.MAP_SHARED)
	f.Close() // the mapping keeps the vnode referenced
	if err != nil {
		t.Skip(err)
	}
	pid := itoa(os.Getpid())
	invalidateFDs()
	has := func(pids string) bool {
		for _, p := range strings.Split(pids, ",") {
			if p == pid {
				return true
			}
		}
		return false
	}
	if pids := ExecInside(nm); !has(pids) {
		t.Errorf("ExecInside(node_modules) = %q, want our pid %s (addon mapped)", pids, pid)
	}
	// Same directory spelled with another case: APFS is case-insensitive.
	if pids := ExecInside(filepath.Join(dir, "Node_Modules")); !has(pids) {
		t.Errorf("ExecInside(Node_Modules) = %q, want our pid %s", pids, pid)
	}
	if pids := ExecInside(filepath.Join(dir, "node_modules2")); has(pids) {
		t.Errorf("sibling reported busy: %q", pids)
	}
	if err := unix.Munmap(mem); err != nil {
		t.Fatal(err)
	}
	invalidateFDs()
	if pids := ExecInside(nm); has(pids) {
		t.Errorf("after munmap: ExecInside = %q still lists us", pids)
	}
}

func TestCwdInsideIgnoresCaseAndNormalization(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Skip(err)
	}
	invalidateFDs()
	if CwdInside(wd) == "" {
		t.Skip("cwd not reported")
	}
	if pids := CwdInside(strings.ToUpper(wd)); pids == "" {
		t.Errorf("CwdInside(%q) = \"\" (case differs only)", strings.ToUpper(wd))
	}
}

func TestNativeExecsIsFast(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	start := time.Now()
	execs, ok, _ := nativeExecs(context.Background())
	if !ok {
		t.Skip("proc_info unavailable")
	}
	t.Logf("%d mapped files in %v", len(execs), time.Since(start))
}
