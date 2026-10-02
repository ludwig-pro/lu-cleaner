package core

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
	"github.com/ludwig-pro/lu-cleaner/internal/scanctl"
)

// Env is everything a provider needs to scan. Built once per run by the CLI.
type Env struct {
	// ScanLimits is resolved without changing process policy. The invocation
	// controller carries the effective budgets through the scan context.
	ScanLimits scanctl.Limits
	Home       string    // user home, absolute
	TmpDir     string    // per-user temp dir, symlinks resolved (/private/var/folders/xx/yyyy/T), see NewEnv
	Now        time.Time // reference time for ages

	// Roots are directories scanned for project artifacts (node_modules, Pods, builds...).
	Roots []string
	// ExplicitRoots is true when the user passed roots on the command line
	// (lu-cleaner artifacts <root>, --root): scanners must then stay inside
	// Roots and not add worktree roots or built-in extra folders.
	ExplicitRoots bool
	// WorktreeRoots are extra directories that contain git worktrees (AI tools).
	WorktreeRoots []string
	// Exclude are absolute paths that are never scanned nor cleaned (prefix match).
	Exclude []string
	// MaxDepth bounds the artifact scan depth below each root.
	MaxDepth int
	// ExtraArtifacts are additional project artifact directory names (config extra_artifacts).
	ExtraArtifacts []string
	// KeepLatest is how many newest versions of versioned things to keep (config keep_latest, >= 1).
	KeepLatest int
	// StaleAfter: items unused for longer are considered stale (config stale_after).
	StaleAfter time.Duration

	// Protected reports paths the safety guard would never delete; providers
	// must not propose them. Nil means nothing is protected.
	Protected func(path string) bool
	// ProtectedContext uses the consumer's context for guard inventories, so
	// stopping a picker scan also interrupts glob checks while the invocation
	// context remains alive (for example during cleaning).
	ProtectedContext func(context.Context, string) bool

	Runner Runner                           // external command runner (git, xcrun, docker...)
	Logf   func(format string, args ...any) // debug logging, never nil
}

// Runner executes external commands. Abstracted for tests.
type Runner interface {
	// Output runs name with args in dir ("" = cwd) and returns stdout.
	Output(ctx context.Context, dir, name string, args ...string) ([]byte, error)
	// LookPath reports whether a binary exists on PATH.
	LookPath(name string) (string, error)
}

// ExecRunner is the real Runner backed by os/exec.
type ExecRunner struct{}

// waitDelay bounds how long Output waits for the stdout/stderr pipes to be
// closed once the command was killed (a helper that escaped the process
// group may still hold them).
const waitDelay = 2 * time.Second

func (ExecRunner) Output(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "LC_ALL=C", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	killGroupOnCancel(cmd)
	start := time.Now()
	out, err := cmd.Output()
	fsx.Trace("cmd", name+" "+strings.Join(args, " "), start, dir)
	return out, err
}

// killGroupOnCancel runs cmd in its own process group and, when its context
// is done, kills the whole group rather than only the direct child: wrappers
// (xcrun, node shims, brew's ruby, sh -c) often leave helpers running that
// inherited stdout, and Output would otherwise wait for them without bound.
// WaitDelay is the backstop for a helper that left the group (setsid).
func killGroupOnCancel(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) == nil {
			return nil
		}
		return cmd.Process.Kill() // no group (already gone, EPERM...): at least the child
	}
	cmd.WaitDelay = waitDelay
}

func (ExecRunner) LookPath(name string) (string, error) { return exec.LookPath(name) }

// NewEnv builds an Env with sane defaults for the current user.
func NewEnv() *Env {
	home, _ := os.UserHomeDir()
	return &Env{
		Home:       home,
		TmpDir:     userTmpDir(os.Getenv("TMPDIR")),
		Now:        time.Now(),
		MaxDepth:   8,
		KeepLatest: 1,
		StaleAfter: 14 * 24 * time.Hour,
		Runner:     ExecRunner{},
		Logf:       func(string, ...any) {},
	}
}

// darwinUserTempDir asks the system for the per-user temp dir, like
// confstr(_CS_DARWIN_USER_TEMP_DIR) does ("" when unavailable). A variable
// for tests.
var darwinUserTempDir = func() string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/getconf", "DARWIN_USER_TEMP_DIR")
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// userTmpDir resolves the per-user temp dir from $TMPDIR (tmpEnv). $TMPDIR
// is missing under cron, launchd and sudo, and may be set to the shared
// /tmp: falling back to /tmp would make the catalog's $TMPDIR entries point
// at the wrong place and turn the whole parent (/private) into an allowed
// deletion area. So when $TMPDIR does not resolve to a per-user temp dir,
// the system is asked (getconf DARWIN_USER_TEMP_DIR); /tmp is only the last
// resort.
func userTmpDir(tmpEnv string) string {
	resolve := func(p string) string {
		if p == "" || !filepath.IsAbs(p) {
			return ""
		}
		p = filepath.Clean(p)
		if r, err := filepath.EvalSymlinks(p); err == nil {
			p = r
		}
		return p
	}
	if t := resolve(tmpEnv); IsUserTempDir(t) {
		return t
	}
	if t := resolve(darwinUserTempDir()); IsUserTempDir(t) {
		return t
	}
	if t := resolve(tmpEnv); t != "" {
		return t
	}
	if t := resolve(os.TempDir()); t != "" {
		return t
	}
	return "/tmp"
}

// IsUserTempDir reports whether p is a resolved per-user temp dir of macOS:
// exactly /private/var/folders/<xx>/<id>/T.
func IsUserTempDir(p string) bool {
	rest, ok := strings.CutPrefix(p, "/private/var/folders/")
	if !ok {
		return false
	}
	parts := strings.Split(rest, "/")
	return len(parts) == 3 && parts[0] != "" && parts[1] != "" && parts[2] == "T" &&
		parts[0] != "." && parts[0] != ".." && parts[1] != "." && parts[1] != ".."
}

// Expand turns "~/x" into an absolute path under Home and cleans it.
func (e *Env) Expand(p string) string {
	if p == "~" {
		return e.Home
	}
	if strings.HasPrefix(p, "~/") {
		p = filepath.Join(e.Home, p[2:])
	}
	if strings.HasPrefix(p, "$TMPDIR") {
		p = filepath.Join(e.TmpDir, strings.TrimPrefix(p, "$TMPDIR"))
	}
	return filepath.Clean(p)
}

// Pretty shortens an absolute path for display ("~/..." instead of the home dir).
func (e *Env) Pretty(p string) string {
	if p == e.Home {
		return "~"
	}
	if strings.HasPrefix(p, e.Home+"/") {
		return "~/" + p[len(e.Home)+1:]
	}
	return p
}

// Excluded reports whether p is inside one of the Exclude paths. The
// comparison ignores case and Unicode normalization (NFC/NFD), like APFS
// does: "~/Dev/App" is excluded by an exclude entry "~/dev/app".
func (e *Env) Excluded(p string) bool {
	if len(e.Exclude) == 0 {
		return false
	}
	fp := fsx.FoldPath(p)
	for _, x := range e.Exclude {
		if fsx.Within(fp, fsx.FoldPath(x)) {
			return true
		}
	}
	return false
}

// IsProtected is a nil-safe wrapper around Protected.
func (e *Env) IsProtected(p string) bool {
	return e.Protected != nil && e.Protected(p)
}

func (e *Env) IsProtectedContext(ctx context.Context, p string) bool {
	if ctx.Err() != nil {
		return true
	}
	if e.ProtectedContext != nil {
		return e.ProtectedContext(ctx, p)
	}
	return e.IsProtected(p)
}

// Output is a shortcut for e.Runner.Output.
func (e *Env) Output(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	return OutputTimeout(ctx, e.Runner, 0, dir, name, args...)
}

// OutputTimeout admits a scan command before starting its execution timeout.
// A deadline already present on the parent context remains authoritative.
func OutputTimeout(ctx context.Context, runner Runner, timeout time.Duration, dir, name string, args ...string) ([]byte, error) {
	return scanctl.Command(ctx, timeout, func(runCtx context.Context) ([]byte, error) {
		return runner.Output(runCtx, dir, name, args...)
	})
}

func (e *Env) OutputTimeout(ctx context.Context, timeout time.Duration, dir, name string, args ...string) ([]byte, error) {
	return OutputTimeout(ctx, e.Runner, timeout, dir, name, args...)
}

// Has reports whether binary name is available.
func (e *Env) Has(name string) bool {
	_, err := e.Runner.LookPath(name)
	return err == nil
}
