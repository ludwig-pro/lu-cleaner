package cli

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ludwig-pro/lu-cleaner/internal/clean"
	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/providers/artifacts"
	"github.com/ludwig-pro/lu-cleaner/internal/tui"
)

// pickerSpec configures an interactive picker command.
type pickerSpec struct {
	title string
	cats  []core.Category
	kinds []string
	roots []string
	smart bool
	flat  bool
}

// runPicker opens the TUI picker and prints the summary of what was cleaned.
func (c *cli) runPicker(ctx context.Context, spec pickerSpec) error {
	s, err := c.newScanSetup(spec.roots, ctx)
	if err != nil {
		return err
	}
	f, err := c.buildFilter(s, modeDisplay, spec.cats, spec.kinds)
	if err != nil {
		return err
	}
	provs := providersFor(c.Providers(), f.Categories)
	if len(provs) == 0 {
		return errors.New("no scanner handles these categories")
	}
	provs = scopeProviders(s, provs)
	ctx, err = c.startScan(ctx, s)
	if err != nil {
		return err
	}
	c.propagateNoColor()
	sum, err := c.Picker(ctx, tui.PickerOptions{
		Env:        s.env,
		Providers:  provs,
		Filter:     f,
		StaleAfter: s.staleAfter,
		Smart:      spec.smart,
		Title:      spec.title,
		Flat:       spec.flat,
		Clean:      s.clean,
	})
	if sum != nil && len(sum.Results) > 0 {
		c.printCleanSummary(s.env, sum) // what was done, even when interrupted
	}
	if err != nil {
		return err
	}
	if sum != nil && sum.Count(clean.StatusFailed) > 0 {
		return silentFailure()
	}
	return nil
}

// propagateNoColor makes --no-color reach the TUI, whose styles use
// lipgloss' default renderer (it honours NO_COLOR).
func (c *cli) propagateNoColor() {
	if c.f.noColor {
		_ = os.Setenv("NO_COLOR", "1")
	}
}

// listOrPick runs the picker on a terminal, or prints the report otherwise
// (pipes, --json, --list). With --yes it cleans non-interactively, like
// `clean --yes` restricted to the command's categories.
func (c *cli) listOrPick(ctx context.Context, list bool, spec pickerSpec, report reportSpec) error {
	if c.f.yes && !list {
		return c.runCleanYes(ctx, cleanSpec{cats: spec.cats, kinds: spec.kinds, roots: spec.roots})
	}
	if list || c.f.json || !c.interactive() {
		report.cats, report.kinds, report.roots = spec.cats, spec.kinds, spec.roots
		return c.runReport(ctx, report)
	}
	return c.runPicker(ctx, spec)
}

func (c *cli) artifactsCmd() *cobra.Command {
	var (
		targets []string
		list    bool
	)
	cmd := &cobra.Command{
		Use:   "artifacts [roots...]",
		Short: "npkill-like: node_modules, Pods, iOS/Android builds, .expo… per project",
		Long: `Find project artifacts (node_modules, ios/Pods, ios/build, android/build,
android/.gradle, .expo, dist…) below the project roots and pick what to delete.
Roots default to the config "roots" or auto-detected folders (~/dev, ~/Projects…)
plus the AI worktree folders. Roots given as arguments (or with --root) are the
only folders scanned.
With --yes, cleans without the picker (same rules as 'clean --yes').

Kinds for --target: node_modules, ios-pods (or pods), ios-build, android-build,
android-gradle, android-kotlin, android-cxx, expo, next, turbo, js-build, dist…
(see the "kind" of items in 'lu-cleaner scan --json'); an unknown kind is an
error. Outside an Android or iOS project, android-build is reported as
gradle-build, ios-build as xcode-build, android-gradle as gradle-cache and
android-kotlin as gradle-kotlin: either name matches both.`,
		Example: `  lu-cleaner artifacts
  lu-cleaner artifacts ~/local_sources ~/conductor/repos
  lu-cleaner artifacts -t node_modules -t ios-pods --older-than 30d
  lu-cleaner artifacts --list --all
  lu-cleaner artifacts -y -t node_modules --older-than 60d -n`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkTargets(targets); err != nil {
				return err
			}
			spec := pickerSpec{
				title: "Project artifacts",
				cats:  []core.Category{core.CatArtifacts},
				kinds: targets,
				roots: args,
				smart: c.f.smart,
				flat:  true,
			}
			return c.listOrPick(cmd.Context(), list, spec, reportSpec{top: -1})
		},
	}
	cmd.Flags().StringSliceVarP(&targets, "target", "t", nil, "artifact kinds to look for, e.g. node_modules, ios-pods (pods), android-build (repeatable)")
	cmd.Flags().BoolVarP(&list, "list", "l", false, "print the list instead of opening the picker")
	return cmd
}

// checkTargets rejects the --target values that are no artifact kind
// (artifacts.Kinds) nor a kind alias (pods, cocoapods, node-modules...): a
// typo would otherwise scan for nothing, and `artifacts -y -t <typo>` would
// report "nothing to clean" instead of the mistake.
func checkTargets(targets []string) error {
	known := artifacts.Kinds()
	for _, t := range splitList(targets) {
		if slices.ContainsFunc(known, func(k string) bool { return strings.EqualFold(k, t) }) {
			continue
		}
		if _, ok := kindAliases[strings.ToLower(t)]; ok {
			continue
		}
		return usageErr("--target: unknown artifact kind %q (known kinds: %s; aliases: %s)",
			t, strings.Join(known, ", "), strings.Join(aliasNames(), ", "))
	}
	return nil
}

// aliasNames returns the kind aliases that are not artifact kinds
// themselves (pods, cocoapods...), sorted.
func aliasNames() []string {
	known := artifacts.Kinds()
	var out []string
	for a := range kindAliases {
		if !slices.Contains(known, a) {
			out = append(out, a)
		}
	}
	slices.Sort(out)
	return out
}

func (c *cli) worktreesCmd() *cobra.Command {
	var list bool
	cmd := &cobra.Command{
		Use:     "worktrees",
		Aliases: []string{"wt"},
		Short:   "Git worktrees left by Codex, Cursor, Conductor, Claude Code…",
		Long: `List the git worktrees created by AI agents and by hand, with their tool,
branch and status (clean, dirty, unpushed, orphan, merged), and pick what to
remove. Removal uses 'git worktree remove' and keeps the branch, so commits on
a branch (pushed or not) stay in the main repository. Without --force it
refuses a worktree that is locked, has uncommitted or untracked changes, has
commits on no branch (detached HEAD), or contains another worktree or
repository; --dry-run runs the same checks.

With --yes (no picker), worktrees of risk "caution" and worktrees with a
warning (ignored .env files that would be lost…) also need --risk caution.
Worktrees are never moved to the Trash: with --trash they are skipped.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			spec := pickerSpec{
				title: "Worktrees",
				cats:  []core.Category{core.CatWorktrees},
				smart: c.f.smart,
				flat:  true,
			}
			return c.listOrPick(cmd.Context(), list, spec, reportSpec{top: -1, layout: "worktrees"})
		},
	}
	cmd.Flags().BoolVarP(&list, "list", "l", false, "print the table instead of opening the picker")
	return cmd
}

func (c *cli) devicesCmd() *cobra.Command {
	var list bool
	cmd := &cobra.Command{
		Use:   "devices",
		Short: "iOS simulators & runtimes, Android emulators (AVDs) & system images",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			spec := pickerSpec{
				title: "Simulators & emulators",
				cats:  []core.Category{core.CatSimulators, core.CatAndroid},
				smart: c.f.smart,
			}
			return c.listOrPick(cmd.Context(), list, spec, reportSpec{top: -1})
		},
	}
	cmd.Flags().BoolVarP(&list, "list", "l", false, "print the list instead of opening the picker")
	return cmd
}
