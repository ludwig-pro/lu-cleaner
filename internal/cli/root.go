package cli

import (
	"context"
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

// globalFlags are the persistent flags shared by every command.
type globalFlags struct {
	categories []string
	kinds      []string
	minSize    string
	olderThan  string
	risk       string
	smart      bool
	dryRun     bool
	yes        bool
	trash      bool
	force      bool
	json       bool
	roots      []string
	noColor    bool
	verbose    bool
}

// cli is the state of one invocation.
type cli struct {
	*App
	f       globalFlags
	started bool // a command's RunE was reached (errors are runtime errors)
	out     *output
	errw    *statusWriter
}

func newCLI(a *App) *cli {
	c := &cli{App: a}
	c.out = newOutput(a, false)
	c.errw = &statusWriter{w: a.Stderr, tty: a.StderrTTY, width: a.Width}
	return c
}

const rootLong = `lu-cleaner frees disk space on a macOS developer machine: git worktrees left
by AI agents (Codex, Cursor, Conductor, Claude Code), node_modules, Pods,
iOS/Android builds, simulators, emulators, package-manager caches, Xcode data
and AI tools data.

Without a command it opens the interactive dashboard (or prints the scan
report when the output is not a terminal). Nothing is ever deleted without an
explicit selection (picker) or --yes, and every path is re-checked by a safety
guard right before removal.`

const rootExamples = `  lu-cleaner                          # interactive dashboard
  lu-cleaner scan                     # what takes space, grouped by category
  lu-cleaner clean --yes --smart -n   # dry-run of the recommended cleanup
  lu-cleaner artifacts ~/dev          # npkill-like: node_modules, Pods, builds…
  lu-cleaner worktrees                # stale AI worktrees
  lu-cleaner doctor                   # why is my disk still full?`

func (c *cli) rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "lu-cleaner",
		Short:         "Free disk space on a macOS dev machine (React Native, iOS, Android, AI worktrees)",
		Long:          rootLong,
		Example:       rootExamples,
		Version:       c.Version,
		SilenceErrors: true,
		SilenceUsage:  true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			c.started = true
			c.out = newOutput(c.App, c.noColor())
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return c.runDashboard(cmd.Context())
		},
	}
	root.SetVersionTemplate("lu-cleaner {{.Version}}\n")
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return usageErr("%v", err) })
	root.CompletionOptions.HiddenDefaultCmd = true

	pf := root.PersistentFlags()
	pf.StringSliceVarP(&c.f.categories, "category", "c", nil, "only these categories (repeatable, comma-separated): worktrees, artifacts, simulators, xcode, android, ai, js, ide, containers, langs, system")
	pf.StringSliceVarP(&c.f.kinds, "kind", "k", nil, "only these item kinds or provider ids (repeatable, comma-separated)")
	pf.StringVar(&c.f.minSize, "min-size", "", "hide items smaller than this, e.g. 100MB (default: config min_size; 0 for clean --yes)")
	pf.StringVar(&c.f.olderThan, "older-than", "", "only items unused for longer than this, e.g. 30d, 2w, 6m")
	pf.StringVar(&c.f.risk, "risk", "", "highest risk allowed: safe|moderate|caution (default: moderate for clean --yes, everything otherwise)")
	pf.BoolVar(&c.f.smart, "smart", false, "only recommended items (safe caches, stale regenerable data)")
	pf.BoolVarP(&c.f.dryRun, "dry-run", "n", false, "show what would be cleaned, delete nothing")
	pf.BoolVarP(&c.f.yes, "yes", "y", false, "clean without the interactive picker")
	pf.BoolVar(&c.f.trash, "trash", false, "move to ~/.Trash instead of deleting (space is freed only once the Trash is emptied)")
	pf.BoolVar(&c.f.force, "force", false, "ignore running-app guards and dirty/unpushed worktree checks")
	pf.BoolVar(&c.f.json, "json", false, "machine-readable JSON output")
	pf.StringArrayVar(&c.f.roots, "root", nil, "project root to scan for artifacts (repeatable, overrides config roots)")
	pf.BoolVar(&c.f.noColor, "no-color", false, "disable colors (also honours NO_COLOR)")
	pf.BoolVarP(&c.f.verbose, "verbose", "v", false, "debug logging on stderr")

	root.AddGroup(
		&cobra.Group{ID: "clean", Title: "Clean:"},
		&cobra.Group{ID: "inspect", Title: "Inspect:"},
		&cobra.Group{ID: "setup", Title: "Setup:"},
	)
	add := func(group string, cmds ...*cobra.Command) {
		for _, cmd := range cmds {
			cmd.GroupID = group
			root.AddCommand(cmd)
		}
	}
	add("clean", c.cleanCmd(), c.artifactsCmd(), c.worktreesCmd(), c.devicesCmd())
	add("inspect", c.scanCmd(), c.analyzeCmd(), c.doctorCmd(), c.historyCmd(), c.catalogCmd())
	add("setup", c.configCmd(), c.versionCmd())
	root.AddCommand(c.genDocsCmd(root))
	root.SetHelpCommandGroupID("setup")
	return root
}

func (c *cli) noColor() bool {
	return c.f.noColor || c.Getenv("NO_COLOR") != "" || c.Getenv("TERM") == "dumb"
}

// interactive reports whether a full-screen TUI can run.
func (c *cli) interactive() bool { return c.StdinTTY && c.StdoutTTY }

// runDashboard is `lu-cleaner` without a command.
func (c *cli) runDashboard(ctx context.Context) error {
	if !c.interactive() || c.f.json {
		return c.runReport(ctx, reportSpec{})
	}
	return c.runPicker(ctx, pickerSpec{title: "lu-cleaner", smart: true})
}

func (c *cli) versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if c.f.json {
				return c.writeJSON(map[string]string{
					"version": c.Version, "go": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH,
				})
			}
			fmt.Fprintf(c.Stdout, "lu-cleaner %s (%s %s/%s)\n", c.Version, runtime.Version(), runtime.GOOS, runtime.GOARCH)
			return nil
		},
	}
}
