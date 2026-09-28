// Package cli wires the cobra commands of lu-cleaner.
//
// Every external dependency (providers, config, environment, TUI, cleaner,
// system queries) lives in App so tests can swap them for fakes.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/term"

	"github.com/ludwig-pro/lu-cleaner/internal/clean"
	"github.com/ludwig-pro/lu-cleaner/internal/config"
	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/providers"
	"github.com/ludwig-pro/lu-cleaner/internal/providers/catalog"
	"github.com/ludwig-pro/lu-cleaner/internal/sysx"
	"github.com/ludwig-pro/lu-cleaner/internal/tui"
)

// Exit codes.
const (
	ExitOK      = 0
	ExitFailure = 1
	ExitUsage   = 2
	// ExitInterrupted is returned after Ctrl-C (128 + SIGINT).
	ExitInterrupted = 130
)

// App holds the I/O streams and the dependencies of the CLI.
type App struct {
	Version string

	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer

	// Terminal capabilities of the streams.
	StdinTTY, StdoutTTY, StderrTTY bool
	// Width returns the terminal width (0 = unknown / not a terminal).
	Width func() int

	Getenv     func(string) string
	LoadConfig func() (*config.Config, error)
	NewEnv     func() *core.Env
	Providers  func() []core.Provider
	Catalog    func() []catalog.Entry

	Picker  func(context.Context, tui.PickerOptions) (*clean.Summary, error)
	Analyze func(context.Context, tui.AnalyzeOptions) error
	Clean   func(context.Context, []*core.Item, clean.Options, func(clean.Result)) *clean.Summary

	Disk      func(path string) (sysx.Disk, error)
	Snapshots func(context.Context) []string
	Running   func(names ...string) []string
	History   func() ([]clean.HistoryEntry, error)
}

// NewApp returns an App wired to the real system.
func NewApp(version string) *App {
	return &App{
		Version:    version,
		Stdin:      os.Stdin,
		Stdout:     os.Stdout,
		Stderr:     os.Stderr,
		StdinTTY:   isTerminal(os.Stdin),
		StdoutTTY:  isTerminal(os.Stdout),
		StderrTTY:  isTerminal(os.Stderr),
		Width:      terminalWidth,
		Getenv:     os.Getenv,
		LoadConfig: config.Load,
		NewEnv:     core.NewEnv,
		Providers:  providers.All,
		Catalog:    catalog.Entries,
		Picker:     tui.RunPicker,
		Analyze:    tui.RunAnalyze,
		Clean:      clean.Run,
		Disk:       sysx.DiskOf,
		Snapshots:  sysx.LocalSnapshots,
		Running:    sysx.Running,
		History:    clean.ReadHistory,
	}
}

// Execute runs the CLI with the process arguments and returns the exit code.
func Execute(version string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return NewApp(version).Run(ctx, os.Args[1:])
}

// Run executes the command line args and returns the exit code.
func (a *App) Run(ctx context.Context, args []string) int {
	c := newCLI(a)
	root := c.rootCmd()
	root.SetArgs(args)
	root.SetIn(a.Stdin)
	root.SetOut(a.Stdout)
	root.SetErr(a.Stderr)
	cmd, err := root.ExecuteContextC(ctx)
	if err == nil {
		return ExitOK
	}
	if errors.Is(err, context.Canceled) {
		fmt.Fprintln(a.Stderr, "lu-cleaner: interrupted")
		return ExitInterrupted
	}
	var ee *exitError
	if errors.As(err, &ee) {
		if ee.err != nil {
			fmt.Fprintf(a.Stderr, "lu-cleaner: %v\n", ee.err)
			if ee.code == ExitUsage && cmd != nil {
				fmt.Fprintf(a.Stderr, "Run '%s --help' for usage.\n", cmd.CommandPath())
			}
		}
		return ee.code
	}
	fmt.Fprintf(a.Stderr, "lu-cleaner: %v\n", err)
	if !c.started {
		// Cobra failed before running the command: unknown command, bad
		// flag, wrong number of arguments...
		if cmd != nil {
			fmt.Fprintf(a.Stderr, "Run '%s --help' for usage.\n", cmd.CommandPath())
		}
		return ExitUsage
	}
	return ExitFailure
}

// exitError carries an exit code. A nil err means "already reported".
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string {
	if e.err == nil {
		return fmt.Sprintf("exit status %d", e.code)
	}
	return e.err.Error()
}

func (e *exitError) Unwrap() error { return e.err }

// usageErr reports a command line mistake (exit code 2).
func usageErr(format string, args ...any) error {
	return &exitError{code: ExitUsage, err: fmt.Errorf(format, args...)}
}

// silentFailure makes the command exit with code 1 without printing anything
// more (the failure was already reported).
func silentFailure() error { return &exitError{code: ExitFailure} }

func isTerminal(f *os.File) bool { return term.IsTerminal(int(f.Fd())) }

func terminalWidth() int {
	for _, f := range []*os.File{os.Stdout, os.Stderr} {
		if w, _, err := term.GetSize(int(f.Fd())); err == nil && w > 0 {
			return w
		}
	}
	return 0
}
