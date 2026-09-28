package core

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Env is everything a provider needs to scan. Built once per run by the CLI.
type Env struct {
	Home   string    // user home, absolute
	TmpDir string    // $TMPDIR resolved (e.g. /var/folders/xx/.../T)
	Now    time.Time // reference time for ages

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

func (ExecRunner) Output(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "LC_ALL=C", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	return cmd.Output()
}

func (ExecRunner) LookPath(name string) (string, error) { return exec.LookPath(name) }

// NewEnv builds an Env with sane defaults for the current user.
func NewEnv() *Env {
	home, _ := os.UserHomeDir()
	tmp := os.Getenv("TMPDIR")
	if tmp == "" {
		tmp = os.TempDir()
	}
	tmp = filepath.Clean(tmp)
	if r, err := filepath.EvalSymlinks(tmp); err == nil {
		tmp = r
	}
	return &Env{
		Home:       home,
		TmpDir:     tmp,
		Now:        time.Now(),
		MaxDepth:   8,
		KeepLatest: 1,
		StaleAfter: 14 * 24 * time.Hour,
		Runner:     ExecRunner{},
		Logf:       func(string, ...any) {},
	}
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

// Excluded reports whether p is inside one of the Exclude paths.
func (e *Env) Excluded(p string) bool {
	for _, x := range e.Exclude {
		if p == x || strings.HasPrefix(p, x+"/") {
			return true
		}
	}
	return false
}

// IsProtected is a nil-safe wrapper around Protected.
func (e *Env) IsProtected(p string) bool {
	return e.Protected != nil && e.Protected(p)
}

// Output is a shortcut for e.Runner.Output.
func (e *Env) Output(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	return e.Runner.Output(ctx, dir, name, args...)
}

// Has reports whether binary name is available.
func (e *Env) Has(name string) bool {
	_, err := e.Runner.LookPath(name)
	return err == nil
}
