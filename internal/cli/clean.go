package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/ludwig-pro/lu-cleaner/internal/clean"
	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
	"github.com/ludwig-pro/lu-cleaner/internal/safety"
)

const cleanLong = `Select what to clean, then clean it.

Without --yes, opens the interactive picker (recommended items preselected
unless --no-smart), pre-filtered by --category, --kind, --min-size,
--older-than and --risk.

With --yes, runs non-interactively: scan, filter, print the plan, clean.
A narrowing filter is mandatory (--smart, --category or --kind). Items of
risk "caution" (user data, dirty worktrees, sessions…) and items the scan
flagged with a warning (in use by a running process, ignored .env files that
would be lost, runtime used by booted simulators…) are only included with an
explicit --risk caution. Use --dry-run first.

With --trash (or use_trash), files and folders go to the Trash; worktrees and
commands (simctl, docker, brew…) cannot be undone, so they are skipped.`

const cleanExamples = `  lu-cleaner clean                                   # interactive picker
  lu-cleaner clean --yes --smart --dry-run           # what smart select would do
  lu-cleaner clean --yes --smart                     # clean the recommended items
  lu-cleaner clean -y -c artifacts --older-than 30d  # stale node_modules, Pods, builds
  lu-cleaner clean -y -k node_modules --min-size 200MB`

// errNoNarrowing explains why clean --yes refuses to run (continuation
// lines are indented when printed).
const errNoNarrowing = `refusing to clean everything: clean --yes needs a narrowing filter.
Use --smart (recommended items only), --category/-c (e.g. -c artifacts) or --kind/-k (e.g. -k node_modules),
and try it with --dry-run first. Run 'lu-cleaner scan' to see what would match`

// errBroadKind explains why a --kind naming a multi-category scanner is not
// enough narrowing for clean --yes (args: provider id, its categories, one
// of them).
const errBroadKind = `refusing to clean: --kind %s names a scanner that spans several categories (%s),
which is not a narrowing filter for clean --yes. Add --category/-c (e.g. -c %s) or --smart,
or use item kinds (the "kind" of the items in 'lu-cleaner scan --json'), and try it with --dry-run first`

// broadProvider returns the provider whose id is one of kinds and that
// emits more than one category (nil when there is none). Kinds match an
// item's Kind or Provider (core.Filter), so such a value selects a whole
// scanner, across categories.
func broadProvider(provs []core.Provider, kinds []string) core.Provider {
	for _, p := range provs {
		if len(p.Categories()) < 2 {
			continue
		}
		if slices.ContainsFunc(kinds, func(k string) bool { return strings.EqualFold(k, p.ID()) }) {
			return p
		}
	}
	return nil
}

func (c *cli) cleanCmd() *cobra.Command {
	var noSmart bool
	cmd := &cobra.Command{
		Use:     "clean",
		Short:   "Select and clean (interactive picker, or --yes for scripts)",
		Long:    cleanLong,
		Example: cleanExamples,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if c.f.yes {
				return c.runCleanYes(cmd.Context(), cleanSpec{noSmart: noSmart})
			}
			if c.f.json {
				return usageErr("--json needs --yes (the interactive picker has no JSON output)")
			}
			if !c.interactive() {
				return usageErr("clean needs a terminal for the interactive picker; use --yes with --smart, --category or --kind to clean non-interactively")
			}
			return c.runPicker(cmd.Context(), pickerSpec{title: "lu-cleaner", smart: !noSmart})
		},
	}
	cmd.Flags().BoolVar(&noSmart, "no-smart", false, "do not preselect (picker) / keep only (--yes) recommended items")
	return cmd
}

// cleanSpec narrows a non-interactive clean (single-category commands).
type cleanSpec struct {
	noSmart bool
	cats    []core.Category // forced categories
	kinds   []string        // extra kinds (--target)
	roots   []string        // roots override
}

// runCleanYes is the non-interactive clean.
func (c *cli) runCleanYes(ctx context.Context, spec cleanSpec) error {
	smart := c.f.smart && !spec.noSmart
	kinds := append(splitList(c.f.kinds), splitList(spec.kinds)...)
	byScope := smart || len(spec.cats) > 0 || len(splitList(c.f.categories)) > 0
	if !byScope && len(kinds) == 0 {
		return usageErr(errNoNarrowing)
	}
	if !byScope {
		// A kind list narrows only if none of its values is a scanner that
		// spans several categories: -k catalog matches every cache, log
		// and tool-data entry of the catalog, in every category.
		if p := broadProvider(c.Providers(), kinds); p != nil {
			return usageErr(errBroadKind, p.ID(), joinCats(p.Categories()), p.Categories()[0])
		}
	}
	s, err := c.newSetup(spec.roots)
	if err != nil {
		return err
	}
	f, err := c.buildFilter(s, modeCleanYes, spec.cats, spec.kinds)
	if err != nil {
		return err
	}
	res := c.collect(ctx, s, providersFor(c.Providers(), f.Categories))
	if ctx.Err() != nil {
		return ctx.Err()
	}
	c.printProviderErrors(res)
	c.warnUnmatchedKinds(spec.kinds, res.Items)
	plan, held := planClean(res.Items, f, smart, s.clean.Trash, s.env.Now, s.staleAfter)

	if len(plan) == 0 {
		if c.f.json {
			c.printHeld(held)
			return c.writeJSON(&clean.Summary{Results: []clean.Result{}, DryRun: s.clean.DryRun, Trash: s.clean.Trash})
		}
		c.out.println("Nothing to clean with these filters.")
		if !smart && f.MaxRisk < core.RiskCaution {
			c.out.println(c.out.paint(c.out.faint, "Items of risk \"caution\" and items with a warning are excluded unless --risk caution is passed."))
		}
		c.printHeld(held)
		return nil
	}
	if !c.f.json {
		c.printPlan(s, plan, smart)
	}
	c.printHeld(held)

	var (
		mu    sync.Mutex
		n     int
		freed int64
	)
	verb := "cleaning…"
	if s.clean.DryRun {
		verb = "simulating…"
	}
	sp := c.startSpinner(func() string {
		mu.Lock()
		defer mu.Unlock()
		return fmt.Sprintf("%s %d/%d · %s", verb, n, len(plan), fsx.Bytes(freed))
	})
	sum := c.Clean(ctx, plan, s.clean, func(r clean.Result) {
		mu.Lock()
		n++
		if r.Status == clean.StatusDone || r.Status == clean.StatusDryRun {
			freed += r.Freed
		}
		mu.Unlock()
		c.logf("%s: %s %s%s", r.Status, r.Item.Name, r.Message, r.Error)
	})
	sp.stop()

	if c.f.json {
		if err := c.writeJSON(sum); err != nil {
			return err
		}
	} else {
		c.printCleanSummary(s.env, sum)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if sum.Count(clean.StatusFailed) > 0 {
		return silentFailure()
	}
	return nil
}

// planClean selects the items a non-interactive clean processes: cleanable,
// measured, matching the filter, recommended when smart, top-level only.
//
// Items the scan flagged with a warning (Warn: in use, secrets that would be
// lost, runtime of booted simulators…) are treated like caution items: they
// need an explicit --risk caution. Without it they are returned in held
// (top-level, not covered by a planned item) so the user is told about them.
//
// In Trash mode, worktree and command items are skipped by the executor:
// they stay in the plan (to be reported) but do not make the items inside
// them redundant, like clean.Run does.
func planClean(items []*core.Item, f core.Filter, smart, trash bool, now time.Time, stale time.Duration) (plan, held []*core.Item) {
	var out, warned []*core.Item
	for _, it := range items {
		if !it.CanClean() || it.Sizing {
			continue
		}
		if !f.Match(it, now) {
			continue
		}
		if smart && !core.Recommend(it, now, stale) {
			continue
		}
		if it.Warn != "" && f.MaxRisk < core.RiskCaution {
			warned = append(warned, it)
			continue
		}
		out = append(out, it)
	}
	// covering are the planned items that really remove what is inside them.
	covering := core.TopLevel(out)
	plan = covering
	if trash {
		var rest, permanent []*core.Item
		for _, it := range out {
			if it.Method == core.MethodWorktree || it.Method == core.MethodCommand {
				permanent = append(permanent, it)
			} else {
				rest = append(rest, it)
			}
		}
		covering = core.TopLevel(rest)
		plan = append(append([]*core.Item{}, covering...), core.TopLevel(permanent)...)
	}
	if len(warned) > 0 {
		// Held items removed anyway with a planned parent are not "held".
		all := core.TopLevel(append(append([]*core.Item{}, covering...), warned...))
		for _, it := range all {
			if slices.Contains(warned, it) {
				held = append(held, it)
			}
		}
	}
	return plan, held
}

// printHeld lists the items planClean held back because of their warning
// (stdout in text mode, stderr with --json).
func (c *cli) printHeld(held []*core.Item) {
	if len(held) == 0 {
		return
	}
	o := c.out
	msg := fmt.Sprintf("%s with a warning left out (%s): pass --risk caution to include them.",
		plural(len(held), "item", "items"), fsx.Bytes(core.Total(held)))
	if c.f.json {
		c.errw.printf("warning: %s\n", msg)
		return
	}
	o.println(o.paint(o.warn, "Held back: ") + msg)
	for _, it := range held {
		o.println("  - " + sanitize(it.Name) + ": " + o.paint(o.warn, sanitize(it.Warn)))
	}
}

// trashSkip returns why an item cannot be cleaned in Trash mode ("" when
// it can): only files and folders can be moved to the Trash; worktree
// removals and commands delete permanently, and what is already in the
// Trash cannot go there again. The executor skips these items with the
// same messages.
func trashSkip(it *core.Item, home string) string {
	switch it.Method {
	case core.MethodWorktree, core.MethodCommand:
		return "not possible in Trash mode (it would delete permanently)"
	}
	if inTrash(it, home) {
		return "already in the Trash"
	}
	return ""
}

// inTrash reports whether a target of it is in ~/.Trash (or is it).
func inTrash(it *core.Item, home string) bool {
	if home == "" {
		return false
	}
	trash := filepath.Join(home, ".Trash")
	return slices.ContainsFunc(it.Targets(), func(p string) bool { return safety.Within(p, trash) })
}

// printPlan lists what is about to be cleaned.
func (c *cli) printPlan(s *setup, plan []*core.Item, smart bool) {
	o := c.out
	groups := groupByCategory(plan, s.env.Now, s.staleAfter, "size")
	if !smart {
		for _, g := range groups {
			g.reco = map[*core.Item]bool{} // stars only make sense for a smart plan
			g.recoN, g.recoB = 0, 0
		}
	}
	c.printGroups(s, groups, 0)
	o.println()
	var mode []string
	if s.clean.DryRun {
		mode = append(mode, "dry run")
	}
	if s.clean.Trash {
		mode = append(mode, "to the Trash")
	}
	if s.clean.Force {
		mode = append(mode, "--force")
	}
	run, skipped := plan, []*core.Item(nil)
	if s.clean.Trash {
		run = nil
		for _, it := range plan {
			if trashSkip(it, s.env.Home) != "" {
				skipped = append(skipped, it)
			} else {
				run = append(run, it)
			}
		}
	}
	line := fmt.Sprintf("%s %s · %s", o.paint(o.bold, "Plan:"), plural(len(run), "item", "items"), o.sizeText(core.Total(run)))
	if len(mode) > 0 {
		line += " (" + strings.Join(mode, ", ") + ")"
	}
	o.println(line)
	if len(skipped) > 0 {
		how := "rerun without --trash to remove them permanently"
		if s.cfg.UseTrash {
			how = "use_trash is set in the config: rerun with --trash=false to remove them permanently"
		}
		o.printf("%s %s will be skipped (%s):\n",
			o.paint(o.warn, "Trash mode:"), plural(len(skipped), "item", "items"), how)
		for _, it := range skipped {
			o.println("  - " + sanitize(it.Name) + ": " + o.paint(o.faint, trashSkip(it, s.env.Home)))
		}
	}
}

// printCleanSummary prints the outcome of clean.Run. Moves to the Trash
// and permanent removals are totalled separately: only the latter free
// space.
func (c *cli) printCleanSummary(env *core.Env, sum *clean.Summary) {
	o := c.out
	pretty := func(s string) string { return prettyText(env, s) }
	results := append([]clean.Result(nil), sum.Results...)
	sort.SliceStable(results, func(i, j int) bool { return results[i].Item.Name < results[j].Item.Name })

	// Moves to the Trash (Result.Trashed) free nothing: they are totalled
	// apart from what was removed for good (Result.Freed).
	var dry []clean.Result
	var dryTrash, dryFreed int64
	for _, r := range results {
		if r.Status == clean.StatusDryRun {
			dry = append(dry, r)
			dryTrash += r.Trashed
			dryFreed += r.Freed
		}
	}
	trashed, freed := sum.Trashed, sum.Estimated
	var amounts []string
	if trashed > 0 {
		amounts = append(amounts, o.sizeText(trashed)+" moved to the Trash")
	}
	if freed > 0 || trashed == 0 {
		amounts = append(amounts, o.sizeText(freed)+" freed")
	}
	done := sum.Count(clean.StatusDone)
	skipped := sum.Count(clean.StatusSkipped)
	failed := sum.Count(clean.StatusFailed)

	o.println()
	if sum.DryRun {
		would := "free " + o.sizeText(dryFreed)
		switch {
		case dryTrash > 0 && dryFreed > 0:
			would = "move " + o.sizeText(dryTrash) + " to the Trash and free " + o.sizeText(dryFreed)
		case dryTrash > 0:
			would = "move " + o.sizeText(dryTrash) + " to the Trash"
		}
		o.printf("%s nothing was deleted — %s would %s.\n",
			o.paint(o.accent, "Dry run:"), plural(len(dry), "item", "items"), would)
		for _, r := range dry {
			// Deletions are already listed in the plan: show what differs (commands, Trash moves).
			if r.Item.Method == core.MethodCommand || sum.Trash {
				o.println(o.paint(o.faint, "  "+sanitize(r.Item.Name)+": "+pretty(r.Message)))
			}
		}
	} else {
		mark := "Done:"
		if o.tty {
			mark = "✓"
		}
		o.printf("%s %s cleaned · %s (estimated)\n", o.paint(o.good, mark), plural(done, "item", "items"),
			strings.Join(amounts, " · "))
		if sum.DiskBefore.Total > 0 && sum.DiskAfter.Total > 0 {
			o.printf("  Disk free: %s → %s (%s measured)\n",
				fsx.Bytes(sum.DiskBefore.Free), o.paint(o.bold, fsx.Bytes(sum.DiskAfter.Free)), fsx.Bytes(sum.Measured))
		}
		if trashed > 0 {
			o.println(o.paint(o.warn, "  Space is only freed once the Trash is emptied."))
		}
		if freed > 1e9 && sum.Measured < freed/2 {
			o.println(o.paint(o.faint, "  Less space than expected? Run 'lu-cleaner doctor' (snapshots, Trash, open files…)."))
		}
	}
	if skipped > 0 {
		o.printf("%s %d\n", o.paint(o.warn, "Skipped:"), skipped)
		for _, r := range results {
			if r.Status == clean.StatusSkipped {
				o.printf("  - %s: %s\n", sanitize(r.Item.Name), pretty(r.Message))
			}
		}
	}
	if failed > 0 {
		o.printf("%s %d\n", o.paint(o.bad, "Failed:"), failed)
		for _, r := range results {
			if r.Status == clean.StatusFailed {
				o.printf("  - %s: %s\n", sanitize(r.Item.Name), pretty(r.Error))
			}
		}
	}
}
