package aitools

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
	"github.com/ludwig-pro/lu-cleaner/internal/sysx"
)

// A version still executed by a process is kept as "running" even though ps
// only shows the symlink it was launched through (injected probe).
func TestVersionRunningThroughSymlinkIsKept(t *testing.T) {
	f := newFixture(t)
	v := ".local/share/claude/versions/"
	f.file(v+"2.1.279/claude", 1000, 60*day)
	f.file(v+"2.1.280/claude", 1000, 30*day)
	f.file(v+"2.1.283/claude", 1000, 1*day)
	f.link(".local/bin/claude", f.path(v+"2.1.283/claude"))
	// ps shows the path the binary was exec'd through: the symlink.
	f.runner.out["ps -axo comm="] = f.path(".local/bin/claude") + "\n"
	running := f.path(v + "2.1.280")
	f.p.execInside = func(dir string) bool { return fsx.Within(running, dir) }
	r := f.scan()
	var got []string
	for _, it := range r.byKind("claude-code-old-version") {
		got = append(got, it.Meta["version"])
	}
	if strings.Join(got, ",") != "2.1.279" {
		t.Errorf("stale = %v, want only 2.1.279 (2.1.280 is running)", got)
	}
}

// End to end with a real process: a binary started through bin/claude,
// whose link is then flipped to a newer version (auto-update), must still
// be seen running (proc_pidpath), not only through ps.
func TestVersionRunningRealProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a helper process")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Skip(err)
	}
	f := newFixture(t)
	f.p.execInside = sysExecInside // the real probe (proc_pidpath)
	v := ".local/share/claude/versions/"
	f.file(v+"2.1.279/claude", 1000, 60*day)
	bin := f.path(v + "2.1.280/claude")
	copyFile(t, exe, bin)
	f.ageTree(v+"2.1.280", 30*day)
	f.file(v+"2.1.283/claude", 1000, 1*day)
	link := f.link(".local/bin/claude", bin)

	cmd := exec.Command(link, "-test.run=^TestHelperSleep$")
	cmd.Env = append(os.Environ(), "LU_AITOOLS_HELPER_SLEEP=1")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start helper: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })

	// auto-update: the link now points to the newest version
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	f.link(".local/bin/claude", f.path(v+"2.1.283/claude"))

	// sysx caches the process table for a few seconds: wait until it sees us.
	dir := f.path(v + "2.1.280")
	deadline := time.Now().Add(15 * time.Second)
	for sysx.ExecInside(dir) == "" {
		if time.Now().After(deadline) {
			t.Skip("process table not readable here")
		}
		time.Sleep(200 * time.Millisecond)
	}
	// what ps shows (documents the bug): the link, not the version dir
	if out, err := exec.Command("/bin/ps", "-o", "comm=", "-p", strconv.Itoa(cmd.Process.Pid)).Output(); err == nil {
		comm := strings.TrimSpace(string(out))
		if strings.Contains(comm, "/versions/") {
			t.Logf("ps shows the resolved path here: %s", comm)
		}
		f.runner.out["ps -axo comm="] = comm + "\n"
	}
	r := f.scan()
	var got []string
	for _, it := range r.byKind("claude-code-old-version") {
		got = append(got, it.Meta["version"])
	}
	if strings.Join(got, ",") != "2.1.279" {
		t.Errorf("stale = %v, want only 2.1.279 (2.1.280 is running)", got)
	}
}

// TestHelperSleep is the helper process of TestVersionRunningRealProcess.
func TestHelperSleep(t *testing.T) {
	if os.Getenv("LU_AITOOLS_HELPER_SLEEP") == "" {
		t.Skip("helper process")
	}
	time.Sleep(60 * time.Second)
	os.Exit(0)
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}
