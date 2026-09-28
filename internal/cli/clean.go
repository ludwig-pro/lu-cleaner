package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/ludwig-pro/lu-cleaner/internal/clean"
	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
)

const cleanLong = `Select what to clean, then clean it.

Without --yes, opens the interactive picker (recommended items preselected
unless --no-smart), pre-filtered by --category, --kind, --min-size,
--older-than and --risk.

With --yes, runs non-interactively: scan, filter, print the plan, clean.
A narrowing filter is mandatory (--smart, --category or --kind) and items of
risk "caution" (user data, dirty worktrees, sessions…) are only included with
an explicit --risk caution. Use --dry-run first.`

const cleanExamples = `  lu-cleaner clean                                   # interactive picker
  lu-cleaner clean --yes --smart --dry-run           # what smart select would do
  lu-cleaner clean --yes --smart                     # clean the recommended items
  lu-cleaner clean -y -c artifacts --older-than 30d  # stale node_modules, Pods, builds
  lu-cleaner clean -y -k node_modules --min-size 200MB`

// errNoNarrowing explains why clean --yes refuses to run.
const errNoNarrowing = `refusing to clean everything: clean --yes needs a narrowing filter.
  Use --smart (recommended items only), --category/-c (e.g. -c artifacts) or --kind/-k (e.g. -k node_modules),
  and try it with --dry-run first. Run 'lu-cleaner scan' to see what would match`

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
	narrowed := smart || len(spec.cats) > 0 || len(splitList(spec.kinds)) > 0 ||
		len(splitList(c.f.categories)) > 0 || len(splitList(c.f.kinds)) > 0
	if !narrowed {
		return usageErr(errNoNarrowing)
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
	plan := planClean(res.Items, f, smart, s.env.Now, s.staleAfter)

	if len(plan) == 0 {
		if c.f.json {
			return c.writeJSON(&clean.Summary{Results: []clean.Result{}, DryRun: s.clean.DryRun, Trash: s.clean.Trash})
		}
		c.out.println("Nothing to clean with these filters.")
		if !smart && f.MaxRisk < core.RiskCaution {
			c.out.println(c.out.paint(c.out.faint, "Items of risk \"caution\" are excluded unless --risk caution is passed."))
		}
		return nil
	}
	if !c.f.json {
		c.printPlan(s, plan, smart)
	}

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
func planClean(items []*core.Item, f core.Filter, smart bool, now time.Time, stale time.Duration) []*core.Item {
	var out []*core.Item
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
		out = append(out, it)
	}
	return core.TopLevel(out)
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
	line := fmt.Sprintf("%s %s · %s", o.paint(o.bold, "Plan:"), plural(len(plan), "item", "items"), o.sizeText(core.Total(plan)))
	if len(mode) > 0 {
		line += " (" + strings.Join(mode, ", ") + ")"
	}
	o.println(line)
}

// printCleanSummary prints the outcome of clean.Run.
func (c *cli) printCleanSummary(env *core.Env, sum *clean.Summary) {
	o := c.out
	pretty := func(s string) string { return strings.ReplaceAll(s, env.Home+"/", "~/") }
	results := append([]clean.Result(nil), sum.Results...)
	sort.SliceStable(results, func(i, j int) bool { return results[i].Item.Name < results[j].Item.Name })

	var dry []clean.Result
	var dryBytes int64
	for _, r := range results {
		if r.Status == clean.StatusDryRun {
			dry = append(dry, r)
			dryBytes += r.Freed
		}
	}
	done := sum.Count(clean.StatusDone)
	skipped := sum.Count(clean.StatusSkipped)
	failed := sum.Count(clean.StatusFailed)

	o.println()
	if sum.DryRun {
		o.printf("%s nothing was deleted — %s would free %s.\n",
			o.paint(o.accent, "Dry run:"), plural(len(dry), "item", "items"), o.sizeText(dryBytes))
		for _, r := range dry {
			// Deletions are already listed in the plan: show what differs (commands, Trash moves).
			if r.Item.Method == core.MethodCommand || sum.Trash {
				o.println(o.paint(o.faint, "  "+r.Item.Name+": "+pretty(r.Message)))
			}
		}
	} else {
		mark := "Done:"
		if o.tty {
			mark = "✓"
		}
		verb := "freed"
		if sum.Trash {
			verb = "moved to the Trash"
		}
		o.printf("%s %s cleaned · %s %s (estimated)\n", o.paint(o.good, mark), plural(done, "item", "items"), o.sizeText(sum.Estimated), verb)
		if sum.DiskBefore.Total > 0 && sum.DiskAfter.Total > 0 {
			o.printf("  Disk free: %s → %s (%s measured)\n",
				fsx.Bytes(sum.DiskBefore.Free), o.paint(o.bold, fsx.Bytes(sum.DiskAfter.Free)), fsx.Bytes(sum.Measured))
		}
		if sum.Trash && done > 0 {
			o.println(o.paint(o.warn, "  Space is only freed once the Trash is emptied."))
		} else if sum.Estimated > 1e9 && sum.Measured < sum.Estimated/2 {
			o.println(o.paint(o.faint, "  Less space than expected? Run 'lu-cleaner doctor' (snapshots, Trash, open files…)."))
		}
	}
	if skipped > 0 {
		o.printf("%s %d\n", o.paint(o.warn, "Skipped:"), skipped)
		for _, r := range results {
			if r.Status == clean.StatusSkipped {
				o.printf("  - %s: %s\n", r.Item.Name, pretty(r.Message))
			}
		}
	}
	if failed > 0 {
		o.printf("%s %d\n", o.paint(o.bad, "Failed:"), failed)
		for _, r := range results {
			if r.Status == clean.StatusFailed {
				o.printf("  - %s: %s\n", r.Item.Name, pretty(r.Error))
			}
		}
	}
}
