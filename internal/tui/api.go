// Package tui contains the interactive Bubble Tea interfaces:
//   - the picker (npkill / CleanMyMac-like): streaming scan results grouped by
//     category, multi-select, confirm, clean, freed-space summary;
//   - the analyzer (ncdu / "mo analyze"-like): navigate any directory tree by size.
package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ludwig-pro/lu-cleaner/internal/clean"
	"github.com/ludwig-pro/lu-cleaner/internal/core"
)

// PickerOptions configures RunPicker.
type PickerOptions struct {
	Env       *core.Env
	Providers []core.Provider
	// Filter hides non-matching items. Note that its zero MaxRisk (RiskSafe)
	// hides every moderate/caution item: set MaxRisk to RiskNever to show
	// everything, report-only entries included.
	Filter     core.Filter
	StaleAfter time.Duration // for smart select / "stale" badge (default 14 days)
	Smart      bool          // preselect core.Recommend items when the scan ends
	Title      string        // header title, e.g. "lu-cleaner", "Worktrees", "Project artifacts"
	// Flat skips the category overview and lists items directly (used by
	// single-category commands such as `lu-cleaner artifacts`).
	Flat bool
	// Clean holds the execution options (Guard, Trash, Force, DryRun...).
	// A nil Guard is replaced by safety.New(Env.Home, Env.TmpDir, roots, Env.Exclude);
	// empty Home / nil Runner default to Env.Home / Env.Runner.
	Clean clean.Options
}

// RunPicker runs the interactive picker. It returns the summary of the
// cleaning performed (nil if the user quit without cleaning). When several
// cleanings happen in one session, the last summary is returned.
func RunPicker(ctx context.Context, opt PickerOptions) (*clean.Summary, error) {
	m := newPicker(ctx, opt)
	warmTerminal()
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx))
	_, err := p.Run()
	if m.scanCancel != nil {
		m.scanCancel()
	}
	m.waitCleanFinished()
	return m.lastSummary, runErr(ctx, err)
}

// AnalyzeOptions configures RunAnalyze.
type AnalyzeOptions struct {
	Env   *core.Env
	Root  string        // absolute directory to explore
	Clean clean.Options // used when the user deletes an entry (same defaults as PickerOptions.Clean)
}

// RunAnalyze runs the interactive disk analyzer.
func RunAnalyze(ctx context.Context, opt AnalyzeOptions) error {
	root := opt.Root
	if !filepath.IsAbs(root) {
		abs, err := filepath.Abs(root)
		if err != nil {
			return err
		}
		root = abs
	}
	root = filepath.Clean(root)
	fi, err := os.Stat(root)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", root)
	}
	opt.Root = root
	m := newAnalyzer(ctx, opt)
	warmTerminal()
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx))
	_, err = p.Run()
	m.waitDelete()
	m.cancel()
	return runErr(ctx, err)
}

// warmTerminal detects the color profile and background color before Bubble
// Tea takes over stdin: a lazy detection during rendering would race with the
// program's input reader and could leak the terminal's reply as key presses.
func warmTerminal() {
	_ = lipgloss.ColorProfile()
	_ = lipgloss.HasDarkBackground()
}

// runErr maps Bubble Tea's exit errors: a cancelled parent context is
// reported as such, a normal quit as nil.
func runErr(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil && errors.Is(err, tea.ErrProgramKilled) {
		return ctx.Err()
	}
	return err
}
