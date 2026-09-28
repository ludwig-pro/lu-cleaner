package cli

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/engine"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
	"github.com/ludwig-pro/lu-cleaner/internal/sysx"
)

const defaultTop = 15

// reportSpec describes a non-interactive report (scan and the non-TTY
// fallback of the picker commands).
type reportSpec struct {
	cats    []core.Category // forced categories (single-category commands)
	kinds   []string        // extra kinds (--target)
	roots   []string        // roots override (artifacts positional args)
	summary bool            // one line per category
	top     int             // items per category: -1 = default, 0 = all
	sortKey string
	layout  string // "" (grouped items) or "worktrees"
}

func (c *cli) scanCmd() *cobra.Command {
	var (
		summary, all bool
		top          int
		sortKey      string
	)
	cmd := &cobra.Command{
		Use:   "scan",
		Short: "Report what takes space, grouped by category (non-interactive)",
		Long: `Scan every provider and print what can be cleaned, grouped by category.
★ marks the items "smart select" recommends (safe caches, stale regenerable data).`,
		Example: `  lu-cleaner scan
  lu-cleaner scan --summary
  lu-cleaner scan -c artifacts,worktrees --older-than 30d --all
  lu-cleaner scan --json | jq '.totals'`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			spec := reportSpec{summary: summary, top: -1, sortKey: sortKey}
			if cmd.Flags().Changed("top") {
				if top < 0 {
					return usageErr("--top must be >= 0")
				}
				spec.top = top
			}
			if all {
				spec.top = 0
			}
			return c.runReport(cmd.Context(), spec)
		},
	}
	cmd.Flags().BoolVar(&summary, "summary", false, "one line per category")
	cmd.Flags().IntVar(&top, "top", defaultTop, "items listed per category (0 = all)")
	cmd.Flags().BoolVar(&all, "all", false, "list every item (same as --top 0)")
	cmd.Flags().StringVar(&sortKey, "sort", "size", "sort items by size|age|name|path")
	return cmd
}

// runReport scans and prints a report (table, summary or JSON).
func (c *cli) runReport(ctx context.Context, spec reportSpec) error {
	switch spec.sortKey {
	case "", "size", "age", "name", "path":
	default:
		return usageErr("--sort must be size, age, name or path")
	}
	s, err := c.newSetup(spec.roots)
	if err != nil {
		return err
	}
	f, err := c.buildFilter(s, modeDisplay, spec.cats, spec.kinds)
	if err != nil {
		return err
	}
	res := c.collect(ctx, s, providersFor(c.Providers(), f.Categories))
	if ctx.Err() != nil {
		return ctx.Err()
	}
	items := displayItems(res.Items, f, s.env.Now)
	if c.f.smart {
		items = recommendedOnly(items, s.env.Now, s.staleAfter)
	}
	groups := groupByCategory(items, s.env.Now, s.staleAfter, spec.sortKey)

	if c.f.json {
		top := spec.top
		if top < 0 {
			top = 0
		}
		return c.writeJSON(c.buildReport(s, groups, top, res))
	}
	c.printProviderErrors(res)
	top := spec.top
	if top < 0 {
		top = defaultTop
	}
	switch {
	case spec.layout == "worktrees":
		c.printWorktrees(s, items)
	case spec.summary:
		c.printSummaryTable(groups)
	default:
		c.printGroups(s, groups, top)
	}
	c.printTotals(s, groups, f, len(spec.cats) > 0 || len(c.f.categories) > 0)
	return nil
}

// collect runs the providers with a live progress line on stderr.
func (c *cli) collect(ctx context.Context, s *setup, provs []core.Provider) *engine.Result {
	st := &scanStats{total: len(provs), sizes: map[string]int64{}}
	sp := c.startSpinner(st.line)
	res := engine.Collect(ctx, s.env, provs, func(ev engine.Event) {
		st.observe(ev)
		if ev.Done {
			if ev.Err != nil {
				c.logf("provider %s failed after %s: %v", ev.Provider, ev.Took.Round(time.Millisecond), firstLine(ev.Err.Error()))
			} else {
				c.logf("provider %s done in %s", ev.Provider, ev.Took.Round(time.Millisecond))
			}
		}
	})
	sp.stop()
	c.logf("scan: %d items in %s", len(res.Items), res.Took.Round(time.Millisecond))
	return res
}

// scanStats feeds the progress line.
type scanStats struct {
	mu          sync.Mutex
	total, done int
	sizes       map[string]int64
	sum         int64
}

func (s *scanStats) observe(ev engine.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ev.Done {
		s.done++
		return
	}
	s.sum += ev.Item.Size - s.sizes[ev.Item.ID]
	s.sizes[ev.Item.ID] = ev.Item.Size
}

func (s *scanStats) line() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fmt.Sprintf("scanning… %d/%d providers · %s items · %s",
		s.done, s.total, formatCount(len(s.sizes)), fsx.Bytes(s.sum))
}

func (c *cli) printProviderErrors(res *engine.Result) {
	ids := make([]string, 0, len(res.Errors))
	for id := range res.Errors {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		msg := res.Errors[id].Error()
		if !c.f.verbose {
			msg = firstLine(msg)
		}
		c.errw.printf("warning: provider %s: %s\n", id, msg)
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// displayItems keeps the items worth showing that pass the filter.
func displayItems(items []*core.Item, f core.Filter, now time.Time) []*core.Item {
	var out []*core.Item
	for _, it := range items {
		if it.Size == 0 && !it.CanClean() {
			continue // placeholder emitted by a provider (nothing found / too small)
		}
		if !f.Match(it, now) {
			continue
		}
		out = append(out, it)
	}
	return out
}

func recommendedOnly(items []*core.Item, now time.Time, stale time.Duration) []*core.Item {
	var out []*core.Item
	for _, it := range items {
		if core.Recommend(it, now, stale) {
			out = append(out, it)
		}
	}
	return out
}

// catGroup is the items of one category.
type catGroup struct {
	info  core.CategoryInfo
	items []*core.Item // sorted
	reco  map[*core.Item]bool
	total int64 // freeable: cleanable items only, nested paths counted once
	recoN int
	recoB int64
}

// groupByCategory groups items in display order (core.Categories, then
// unknown categories alphabetically) and sorts each group.
func groupByCategory(items []*core.Item, now time.Time, stale time.Duration, sortKey string) []*catGroup {
	byCat := map[core.Category][]*core.Item{}
	for _, it := range items {
		byCat[it.Category] = append(byCat[it.Category], it)
	}
	var order []core.Category
	for _, ci := range core.Categories {
		if len(byCat[ci.ID]) > 0 {
			order = append(order, ci.ID)
		}
	}
	var extra []core.Category
	for cat := range byCat {
		if core.LookupCategory(cat).Icon == "•" {
			extra = append(extra, cat)
		}
	}
	sort.Slice(extra, func(i, j int) bool { return extra[i] < extra[j] })
	order = append(order, extra...)

	var out []*catGroup
	for _, cat := range order {
		g := &catGroup{info: core.LookupCategory(cat), items: byCat[cat], reco: map[*core.Item]bool{}}
		core.SortBy(g.items, sortKey, now)
		var rec, cleanable []*core.Item
		for _, it := range g.items {
			if it.CanClean() {
				cleanable = append(cleanable, it)
			}
			if core.Recommend(it, now, stale) {
				g.reco[it] = true
				rec = append(rec, it)
			}
		}
		g.total = core.Total(cleanable)
		g.recoN = len(rec)
		g.recoB = core.Total(rec)
		out = append(out, g)
	}
	return out
}

// itemTable builds the NAME SIZE AGE RISK RECO PATH table.
func (c *cli) itemTable() *table {
	t := newTable("NAME", "SIZE", "AGE", "RISK", "RECO", "PATH")
	t.right[1], t.right[2] = true, true
	t.maxw[0] = 44
	t.leftTrunc[5] = true
	t.shrink = 5
	return t
}

// addItem appends one item row (and its warning line).
func (c *cli) addItem(t *table, env *core.Env, it *core.Item, reco bool, now time.Time) {
	o := c.out
	size := fsx.Bytes(it.Size)
	if it.Sizing {
		size = "…"
	} else if it.Reclaim > 0 && it.Reclaim < it.Size {
		size += "*"
	}
	star := ""
	if reco {
		star = "★"
	}
	t.add(func(col int, s string) string {
		switch col {
		case 1:
			if it.Sizing {
				return o.paint(o.faint, s)
			}
			return o.sizeText(it.Size) + strings.TrimPrefix(s, fsx.Bytes(it.Size))
		case 2:
			return o.paint(o.faint, s)
		case 3:
			if !it.CanClean() {
				return o.paint(o.faint, s)
			}
			return o.riskPaint(it.Risk, s)
		case 4:
			return o.paint(o.accent, s)
		case 5:
			return o.paint(o.faint, s)
		}
		return s
	}, it.Name, size, ageText(it, now), riskPlain(it), star, whereText(env, it))
	if it.Warn != "" {
		t.line("    " + o.paint(o.warn, "! "+it.Warn))
	}
}

func riskPlain(it *core.Item) string {
	if !it.CanClean() {
		return "info"
	}
	return it.Risk.String()
}

// printGroups prints every category with its items.
func (c *cli) printGroups(s *setup, groups []*catGroup, top int) {
	o := c.out
	if len(groups) == 0 {
		o.println("Nothing found.")
		return
	}
	t := c.itemTable()
	hasReclaim := false
	for gi, g := range groups {
		if gi > 0 {
			t.line("")
		}
		head := fmt.Sprintf("%s  %s · %s", o.paint(o.title, o.catTitle(g.info)), o.sizeText(g.total), plural(len(g.items), "item", "items"))
		if g.recoN > 0 {
			head += " · " + o.paint(o.accent, "★ "+fsx.Bytes(g.recoB)) + " recommended"
		}
		t.line(head)
		t.add(func(_ int, s string) string { return o.paint(o.dim, s) }, t.headers...)
		shown := g.items
		if top > 0 && len(shown) > top {
			shown = shown[:top]
		}
		for _, it := range shown {
			c.addItem(t, s.env, it, g.reco[it], s.env.Now)
			if it.Reclaim > 0 && it.Reclaim < it.Size {
				hasReclaim = true
			}
		}
		if rest := g.items[len(shown):]; len(rest) > 0 {
			t.line(o.paint(o.faint, fmt.Sprintf("  … %s more (%s) — use --all to list them",
				formatCount(len(rest)), fsx.Bytes(core.Total(rest)))))
		}
	}
	t.render(o, "  ", false)
	if hasReclaim {
		o.println(o.paint(o.faint, "  * shares hardlinks with files outside it: deleting frees less (see \"reclaim\" in --json)"))
	}
}

// printSummaryTable prints one line per category.
func (c *cli) printSummaryTable(groups []*catGroup) {
	o := c.out
	if len(groups) == 0 {
		o.println("Nothing found.")
		return
	}
	t := newTable("CATEGORY", "SIZE", "ITEMS", "RECOMMENDED")
	t.right[1], t.right[2], t.right[3] = true, true, true
	for _, g := range groups {
		reco := ""
		if g.recoN > 0 {
			reco = fsx.Bytes(g.recoB) + " (" + formatCount(g.recoN) + ")"
		}
		t.add(func(col int, s string) string {
			switch col {
			case 0:
				return o.paint(o.title, s)
			case 1:
				return o.sizeText(g.total)
			case 3:
				return o.paint(o.accent, s)
			}
			return s
		}, o.catTitle(g.info), fsx.Bytes(g.total), formatCount(len(g.items)), reco)
	}
	t.render(o, "", true)
}

// printWorktrees prints the worktree-specific table.
func (c *cli) printWorktrees(s *setup, items []*core.Item) {
	o := c.out
	if len(items) == 0 {
		o.println("No worktrees found.")
		return
	}
	core.SortBy(items, "size", s.env.Now)
	t := newTable("NAME", "TOOL", "BRANCH", "STATUS", "SIZE", "AGE", "PATH")
	t.right[4], t.right[5] = true, true
	t.maxw[0], t.maxw[2] = 32, 32
	t.leftTrunc[6] = true
	t.shrink = 6
	for _, it := range items {
		t.add(func(col int, v string) string {
			switch col {
			case 3:
				return o.paint(statusStyle(o, it.Meta["status"]), v)
			case 4:
				return o.sizeText(it.Size)
			case 5, 6:
				return o.paint(o.faint, v)
			}
			return v
		}, it.Name, it.Meta["tool"], it.Meta["branch"], it.Meta["status"], fsx.Bytes(it.Size), ageText(it, s.env.Now), whereText(s.env, it))
	}
	t.render(o, "", true)
}

func statusStyle(o *output, status string) lipgloss.Style {
	switch strings.ToLower(status) {
	case "clean", "merged":
		return o.good
	case "dirty", "unpushed", "locked":
		return o.bad
	case "orphan", "prunable", "stale":
		return o.warn
	}
	return o.faint
}

// printTotals prints the footer: totals, disk usage and the next step.
func (c *cli) printTotals(s *setup, groups []*catGroup, f core.Filter, narrowed bool) {
	o := c.out
	all, rec := cleanableAndRecommended(groups)
	o.println()
	line := fmt.Sprintf("%s %s in %s", o.paint(o.bold, "Total"), o.sizeText(core.Total(all)), plural(len(all), "item", "items"))
	if len(rec) > 0 {
		line += " · " + o.paint(o.accent, "★ "+fsx.Bytes(core.Total(rec))) + " recommended (" + plural(len(rec), "item", "items") + ")"
	}
	o.println(line)
	if d, err := c.Disk(s.env.Home); err == nil && d.Total > 0 {
		o.println(o.paint(o.faint, fmt.Sprintf("Disk: %s free of %s (%.0f%% used)", fsx.Bytes(d.Free), fsx.Bytes(d.Total), d.UsedPct())))
	}
	if len(rec) > 0 {
		cmd := "lu-cleaner clean --smart"
		if narrowed && len(f.Categories) > 0 {
			cmd += " -c " + strings.ReplaceAll(joinCats(f.Categories), ", ", ",")
		}
		o.println("→ run: " + o.paint(o.bold, cmd))
	}
}

// cleanableAndRecommended flattens the groups: cleanable items (totals do not
// count report-only entries) and recommended ones.
func cleanableAndRecommended(groups []*catGroup) (all, rec []*core.Item) {
	for _, g := range groups {
		for _, it := range g.items {
			if it.CanClean() {
				all = append(all, it)
			}
			if g.reco[it] {
				rec = append(rec, it)
			}
		}
	}
	return all, rec
}

// ------------------------------------------------------------------ JSON

type jsonItem struct {
	*core.Item
	LastUsed      *time.Time `json:"last_used,omitempty"`
	CategoryTitle string     `json:"category_title"`
	AgeDays       *float64   `json:"age_days"`
	Recommended   bool       `json:"recommended"`
	Cleanable     bool       `json:"cleanable"`
	Freed         int64      `json:"freed"`
}

type jsonTotals struct {
	Size        int64            `json:"size"`
	Recommended int64            `json:"recommended"`
	Items       int              `json:"items"`
	ByCategory  map[string]int64 `json:"by_category"`
}

type jsonReport struct {
	Version     string            `json:"version"`
	GeneratedAt time.Time         `json:"generated_at"`
	Disk        sysx.Disk         `json:"disk"`
	Items       []jsonItem        `json:"items"`
	Totals      jsonTotals        `json:"totals"`
	Errors      map[string]string `json:"errors"`
	// Timing is the scan duration of each provider, in milliseconds.
	Timing map[string]int64 `json:"timing_ms"`
	TookMs int64            `json:"took_ms"`
}

func newJSONItem(it *core.Item, reco bool, now time.Time) jsonItem {
	j := jsonItem{
		Item:          it,
		CategoryTitle: core.LookupCategory(it.Category).Title,
		Recommended:   reco,
		Cleanable:     it.CanClean(),
		Freed:         it.Freed(),
	}
	if !it.LastUsed.IsZero() {
		t := it.LastUsed
		j.LastUsed = &t
		days := math.Round(now.Sub(t).Hours()/24*10) / 10
		j.AgeDays = &days
	}
	return j
}

func (c *cli) buildReport(s *setup, groups []*catGroup, top int, res *engine.Result) *jsonReport {
	r := &jsonReport{
		Version:     c.Version,
		GeneratedAt: s.env.Now.UTC().Truncate(time.Second),
		Items:       []jsonItem{},
		Totals:      jsonTotals{ByCategory: map[string]int64{}},
		Errors:      map[string]string{},
	}
	r.Disk, _ = c.Disk(s.env.Home)
	for _, g := range groups {
		shown := g.items
		if top > 0 && len(shown) > top {
			shown = shown[:top]
		}
		for _, it := range shown {
			r.Items = append(r.Items, newJSONItem(it, g.reco[it], s.env.Now))
		}
		r.Totals.ByCategory[string(g.info.ID)] = g.total
	}
	all, rec := cleanableAndRecommended(groups)
	r.Totals.Size = core.Total(all)
	r.Totals.Recommended = core.Total(rec)
	r.Totals.Items = len(all)
	for id, err := range res.Errors {
		r.Errors[id] = err.Error()
	}
	r.Timing = map[string]int64{}
	for id, d := range res.Timing {
		r.Timing[id] = d.Milliseconds()
	}
	r.TookMs = res.Took.Milliseconds()
	return r
}
