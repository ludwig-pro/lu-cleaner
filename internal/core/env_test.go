package core

import (
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestIsUserTempDir(t *testing.T) {
	for p, want := range map[string]bool{
		"/private/var/folders/xy/abc123/T":   true,
		"/private/var/folders/xy/abc123/T/x": false,
		"/private/var/folders/xy/abc123":     false,
		"/private/var/folders/xy/abc123/C":   false,
		"/private/var/folders/../../etc/T":   false,
		"/private/tmp":                       false,
		"/tmp":                               false,
		"/private":                           false,
		"":                                   false,
	} {
		if got := IsUserTempDir(p); got != want {
			t.Errorf("IsUserTempDir(%q) = %v, want %v", p, got, want)
		}
	}
}

// Under cron/launchd/sudo $TMPDIR is unset (or /tmp): the per-user temp dir
// must still be found, never the shared /private/tmp whose parent is /private.
func TestUserTmpDirWithoutTMPDIR(t *testing.T) {
	const fake = "/private/var/folders/zz/fake-user/T"
	saved := darwinUserTempDir
	defer func() { darwinUserTempDir = saved }()

	// Resolution happens on the real filesystem: use a real per-user dir when
	// the system provides one, else check the fallback logic with a stub.
	real := saved()
	if runtime.GOOS == "darwin" && real != "" {
		want, err := filepath.EvalSymlinks(filepath.Clean(real))
		if err != nil {
			t.Skip(err)
		}
		for _, env := range []string{"", "/tmp", "/private/tmp", "relative/dir"} {
			if got := userTmpDir(env); got != want {
				t.Errorf("userTmpDir(%q) = %q, want %q", env, got, want)
			}
		}
		if got := userTmpDir(real); got != want {
			t.Errorf("userTmpDir(real) = %q, want %q", got, want)
		}
		t.Setenv("TMPDIR", "")
		if e := NewEnv(); e.TmpDir != want || !IsUserTempDir(e.TmpDir) {
			t.Errorf("NewEnv().TmpDir = %q, want %q", e.TmpDir, want)
		}
	}

	darwinUserTempDir = func() string { return fake }
	if got := userTmpDir(""); got != fake {
		t.Errorf("userTmpDir with a stubbed system dir = %q, want %q", got, fake)
	}
	darwinUserTempDir = func() string { return "" }
	if got := userTmpDir(""); got == "" || got == "/private" || got == "/" {
		t.Errorf("last-resort fallback = %q", got)
	}
}

func TestExecRunnerTimeoutKillsGrandchildren(t *testing.T) {
	if _, err := exec.LookPath("/bin/sh"); err != nil {
		t.Skip(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	// The background sleep inherits stdout: killing only sh left Output
	// waiting for it (the whole 20s).
	out, err := ExecRunner{}.Output(ctx, "", "/bin/sh", "-c", "sleep 20 & echo hi; sleep 20")
	took := time.Since(start)
	if err == nil {
		t.Fatalf("expected a timeout error, got output %q", out)
	}
	if took > waitDelay+2*time.Second {
		t.Fatalf("Output returned after %v, want about the 300ms timeout", took)
	}
	// Normal commands are unaffected.
	out, err = ExecRunner{}.Output(context.Background(), "", "/bin/echo", "ok")
	if err != nil || strings.TrimSpace(string(out)) != "ok" {
		t.Errorf("echo: %q, %v", out, err)
	}
}

func TestExcludedIgnoresCaseAndNormalization(t *testing.T) {
	e := &Env{Exclude: []string{"/Users/me/Dev/Café", "/Users/me/Keep"}}
	for _, p := range []string{
		"/Users/me/Dev/Café/node_modules",
		"/users/me/dev/café/node_modules", // NFD, other case
		"/Users/me/keep",
		"/Users/me/KEEP/x",
	} {
		if !e.Excluded(p) {
			t.Errorf("Excluded(%q) = false", p)
		}
	}
	for _, p := range []string{"/Users/me/Dev/Cafe", "/Users/me/Keeper", "/Users/me/Dev"} {
		if e.Excluded(p) {
			t.Errorf("Excluded(%q) = true", p)
		}
	}
}
