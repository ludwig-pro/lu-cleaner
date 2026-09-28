package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/ludwig-pro/lu-cleaner/internal/clean"
	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
)

const (
	hardMinWidth  = 40
	hardMinHeight = 10
)

func (m *pickerModel) View() string {
	if m.quitting {
		return ""
	}
	w, h := m.w, m.h
	if w < hardMinWidth || h < hardMinHeight {
		return tooSmall(w, h)
	}
	header := m.viewHeader(w)
	var body string
	switch m.mode {
	case modeCleaning, modeSummary:
		body = m.viewRun(w, h-3)
		out := header + "\n" + fitHeight(body, h-3) + "\n" + m.viewFooter(w)
		return fitWidth(out, w)
	}
	body = m.viewBrowse(w)
	out := fitWidth(header+"\n"+body, w)
	switch m.mode {
	case modeHelp:
		out = overlay(out, m.viewHelp(w, h), w, h)
	case modeConfirm:
		out = overlay(out, m.viewConfirm(w, h), w, h)
	}
	return out
}

// ------------------------------------------------------------------ header

func (m *pickerModel) viewHeader(w int) string {
	title := sTitle.Render("🧹 " + m.opt.Title)
	var st string
	if m.scanning {
		st = m.spin.View() + " " + sAccent.Render(fmt.Sprintf("scanning %d/%d providers", len(m.provDone), len(m.opt.Providers)))
	} else {
		st = sGreen.Render("✓") + sSubtle.Render(fmt.Sprintf(" scan done in %.1fs", m.scanTook.Seconds()))
	}
	st += sSubtle.Render(" · "+plural(len(m.visible), "item")) + sSubtle.Render(" · ") + sBold.Render(fsx.Bytes(m.visTotal)) + sSubtle.Render(" reclaimable")
	if n := len(m.provErrs); n > 0 {
		st += sOrange.Render(fmt.Sprintf(" · ⚠ %s failed (?)", plural(n, "provider")))
	}
	line1 := title + " " + st
	return line1 + "\n" + diskLine(m.disk, m.diskErr, w)
}

// ------------------------------------------------------------------ browse

func (m *pickerModel) viewBrowse(w int) string {
	listRows, detailRows := m.layout()
	var b strings.Builder
	b.WriteString(m.viewCrumb(w))
	b.WriteByte('\n')
	if m.screen == scrCategories {
		b.WriteString(sHeader.Render(m.catColumns(w)))
		b.WriteByte('\n')
		b.WriteString(fitHeight(m.viewCats(w, listRows), listRows))
	} else {
		cols := m.itemCols(w)
		b.WriteString(sHeader.Render(cols.header()))
		b.WriteByte('\n')
		b.WriteString(fitHeight(m.viewItems(w, listRows, cols), listRows))
		if detailRows > 0 {
			b.WriteByte('\n')
			b.WriteString(fitHeight(m.viewDetails(w, detailRows), detailRows))
		}
	}
	b.WriteByte('\n')
	b.WriteString(m.viewStatus(w))
	b.WriteByte('\n')
	b.WriteString(m.viewFooter(w))
	return b.String()
}

func (m *pickerModel) viewCrumb(w int) string {
	var s string
	switch {
	case m.screen == scrCategories:
		s = sAccentB.Render("Categories")
		if m.catBySize {
			s += sDim.Render(" · by size")
		}
		s += sDim.Render(" · enter to open")
	case m.opt.Flat:
		s = sAccentB.Render(fmt.Sprintf("%d items", len(m.list))) + sDim.Render(" · sort: "+m.sort.String())
	default:
		info := core.LookupCategory(m.cat)
		s = sAccentB.Render(fmt.Sprintf("%s %s", info.Icon, info.Title)) + sSubtle.Render(fmt.Sprintf(" (%d)", len(m.list))) +
			sDim.Render(" · sort: "+m.sort.String()+" · ← back · tab next")
	}
	if q := m.filter.String(); q != "" && !m.filter.active {
		s += sCyan.Render(fmt.Sprintf(" · filter %q", q))
	}
	if m.showAll {
		s += sDim.Render(" · showing all")
	}
	return pad(s, w)
}

// ------------------------------------------------------------------ categories

type catCols struct{ title, size, count, sel int }

func (m *pickerModel) catLayout(w int) catCols {
	c := catCols{size: 10, count: 12, sel: 14}
	if w < 72 {
		c.sel = 0
	}
	if w < 56 {
		c.count = 0
	}
	// marker 2 + check 4 + icon 3 + spinner 2 + gaps
	c.title = w - 2 - 4 - 3 - 2 - c.size - c.count - c.sel - 1
	return c
}

func (m *pickerModel) catColumns(w int) string {
	c := m.catLayout(w)
	s := strings.Repeat(" ", 9) + pad("CATEGORY", c.title) + padLeft("SIZE", c.size)
	if c.count > 0 {
		s += padLeft("ITEMS", c.count)
	}
	if c.sel > 0 {
		s += padLeft("SELECTED", c.sel)
	}
	return s
}

func (m *pickerModel) viewCats(w, rows int) string {
	if len(m.cats) == 0 {
		if m.scanning {
			return sDim.Render("  " + m.spin.View() + " looking for things to clean…")
		}
		if m.filter.String() != "" {
			return sDim.Render("  nothing matches the filter — esc clears it")
		}
		return sGreen.Render("  ✨ nothing to clean here")
	}
	c := m.catLayout(w)
	var lines []string
	end := min(len(m.cats), m.catOffset+rows)
	for i := m.catOffset; i < end; i++ {
		r := m.cats[i]
		cur := i == m.catCursor
		marker := "  "
		if cur {
			marker = sAccentB.Render("› ")
		}
		check := sDim.Render("[·] ")
		switch {
		case r.cleanN == 0:
		case r.selN >= r.cleanN:
			check = sGreen.Render("[✓] ")
		case r.selN > 0:
			check = sYellow.Render("[~] ")
		default:
			check = "[ ] "
		}
		title := pad(r.info.Title, c.title)
		if cur {
			title = sAccentB.Render(title)
		}
		size := fsx.Bytes(r.total)
		if r.total == 0 && r.sizing {
			size = "…"
		}
		line := marker + check + pad(r.info.Icon, 2) + " " + title + sBold.Render(padLeft(size, c.size))
		if c.count > 0 {
			n := thousands(int64(len(r.items)))
			if len(r.items) == 1 {
				n += " item "
			} else {
				n += " items"
			}
			line += sSubtle.Render(padLeft(n, c.count))
		}
		if c.sel > 0 {
			sel := ""
			if r.selN > 0 {
				sel = "✓ " + fsx.Bytes(r.selTotal)
			}
			line += sGreen.Render(padLeft(sel, c.sel))
		}
		if r.sizing {
			line += " " + m.spin.View()
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// ------------------------------------------------------------------ items

type itemCols struct {
	name, loc, size, age, risk, tags int
}

func (m *pickerModel) itemCols(w int) itemCols {
	c := itemCols{size: 9, age: 5, risk: 8}
	switch {
	case w >= 120:
		c.tags = 18
	case w >= 96:
		c.tags = 10
	case w >= 70:
		c.tags = 2
	}
	// marker 2 + check 4 + gaps (name|loc|size|age|risk|tags)
	rest := w - 2 - 4 - c.size - 1 - c.age - 1 - c.risk - 1 - 1
	if c.tags > 0 {
		rest -= c.tags + 1
	}
	if rest >= 44 {
		c.name = clamp(rest*45/100, 18, 48)
		c.loc = rest - c.name - 1
	} else {
		c.name = max(8, rest)
	}
	return c
}

func (c itemCols) header() string {
	s := strings.Repeat(" ", 6) + pad("NAME", c.name) + " "
	if c.loc > 0 {
		s += pad("LOCATION", c.loc) + " "
	}
	s += padLeft("SIZE", c.size) + " " + padLeft("AGE", c.age) + " " + pad("RISK", c.risk)
	if c.tags > 0 {
		s += " " + pad("", c.tags)
	}
	return s
}

func (m *pickerModel) viewItems(w, rows int, c itemCols) string {
	if len(m.list) == 0 {
		switch {
		case m.scanning:
			return sDim.Render("  " + m.spin.View() + " scanning…")
		case m.filter.String() != "":
			return sDim.Render("  nothing matches the filter — esc clears it")
		}
		return sGreen.Render("  ✨ nothing to clean here")
	}
	var lines []string
	end := min(len(m.list), m.offset+rows)
	for i := m.offset; i < end; i++ {
		lines = append(lines, m.itemLine(m.list[i], i == m.cursor, c))
	}
	return strings.Join(lines, "\n")
}

func (m *pickerModel) itemLine(it *core.Item, cur bool, c itemCols) string {
	can := it.CanClean()
	sel := can && m.selected[it.ID]
	marker := "  "
	if cur {
		marker = sAccentB.Render("› ")
	}
	var check string
	switch {
	case !can:
		check = sDim.Render("[·] ")
	case sel:
		check = sGreen.Render("[✓] ")
	default:
		check = "[ ] "
	}
	age := it.Age(m.now)
	stale := age > 0 && age >= m.stale

	name := pad(it.Name, c.name)
	loc := ""
	if c.loc > 0 {
		loc = padTruncLeft(m.env.Pretty(it.Where()), c.loc)
	}
	size := padLeft(fsx.Bytes(it.Size), c.size)
	if it.Sizing {
		size = padLeft("… "+fsx.Bytes(it.Size), c.size)
		if it.Size == 0 {
			size = padLeft("…", c.size)
		}
	}
	ageS := padLeft(ageText(it.LastUsed, m.now), c.age)
	tags := ""
	if c.tags > 0 {
		tags = m.itemTags(it, stale, c.tags)
	}

	if !can {
		line := marker + check + sDim.Render(name+" "+loc+pick(c.loc > 0, " ", "")+size+" "+ageS+" "+pad(it.Risk.String(), c.risk))
		if c.tags > 0 {
			line += " " + tags
		}
		return line
	}
	switch {
	case cur:
		name = sAccentB.Render(name)
	case sel:
		name = sBold.Render(name)
	}
	line := marker + check + name + " "
	if c.loc > 0 {
		line += sSubtle.Render(loc) + " "
	}
	if it.Sizing {
		line += sDim.Render(size)
	} else {
		line += sBold.Render(size)
	}
	if stale {
		ageS = sYellow.Render(ageS)
	} else {
		ageS = sSubtle.Render(ageS)
	}
	line += " " + ageS + " " + riskBadge(it.Risk, c.risk)
	if c.tags > 0 {
		line += " " + tags
	}
	return line
}

func pick(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}

// itemTags renders the small badges column: ⚠ warn, stale, meta hints.
func (m *pickerModel) itemTags(it *core.Item, stale bool, w int) string {
	var parts []string
	used := 0
	add := func(s string, st lipgloss.Style) {
		sw := width(s)
		if used > 0 {
			sw++
		}
		if used+sw > w {
			return
		}
		used += sw
		parts = append(parts, st.Render(s))
	}
	if it.Warn != "" {
		add("⚠", sOrange)
	}
	if stale {
		add("stale", sYellow)
	}
	for _, b := range metaBadges(it) {
		add(b, sCyan)
	}
	return pad(strings.Join(parts, " "), w)
}

// metaBadges extracts a few well-known Meta hints for the list.
func metaBadges(it *core.Item) []string {
	var out []string
	if it.Meta == nil {
		return nil
	}
	if it.Meta["dirty"] == "true" {
		out = append(out, "dirty")
	}
	if n, _ := strconv.Atoi(it.Meta["unpushed"]); n > 0 {
		out = append(out, fmt.Sprintf("↑%d", n))
	}
	if it.Meta["locked"] == "true" {
		out = append(out, "locked")
	}
	if it.Meta["running"] == "true" {
		out = append(out, "running")
	}
	if t := it.Meta["tool"]; t != "" {
		out = append(out, t)
	}
	if b := it.Meta["branch"]; b != "" {
		out = append(out, "⎇ "+b)
	}
	return out
}

// ------------------------------------------------------------------ details

func (m *pickerModel) viewDetails(w, rows int) string {
	it := m.currentItem()
	if it == nil {
		return ""
	}
	inner := w - 2
	var lines []string
	add := func(label, value string, st lipgloss.Style) {
		if value == "" {
			return
		}
		prefix := ""
		if label != "" {
			prefix = sSubtle.Render(pad(label, 9))
		}
		lines = append(lines, prefix+st.Render(value))
	}
	lines = append(lines, sDim.Render(strings.Repeat("─", w)))
	head := sBold.Render(it.Name) + sSubtle.Render(" · ") + riskStyle(it.Risk).Render(it.Risk.String()) +
		sSubtle.Render(" · "+it.Kind+" · "+it.Provider)
	if it.Files > 0 {
		head += sSubtle.Render(" · " + thousands(it.Files) + " files")
	}
	lines = append(lines, head)
	switch {
	case len(it.Paths) > 0:
		shown := min(len(it.Paths), 3)
		for i := 0; i < shown; i++ {
			label := ""
			if i == 0 {
				label = "Paths"
			}
			add(label, truncLeft(m.env.Pretty(it.Paths[i]), inner-9), lipgloss.NewStyle())
		}
		if more := len(it.Paths) - shown; more > 0 {
			add("", fmt.Sprintf("and %d more", more), sDim)
		}
	case it.Path != "":
		add("Path", truncLeft(it.Path, inner-9), lipgloss.NewStyle())
	case it.Location != "":
		add("Location", truncLeft(it.Location, inner-9), lipgloss.NewStyle())
	}
	method := it.Method.String()
	if it.Method == core.MethodDelete && m.trash {
		method = "trash (Trash mode)"
	}
	if it.Method == core.MethodCommand && len(it.Command) > 0 {
		method += ": " + strings.Join(it.Command, " ")
	}
	add("Method", method, lipgloss.NewStyle())
	if it.Reclaim > 0 && it.Reclaim < it.Size {
		add("Reclaim", fmt.Sprintf("only %s really freed (of %s): hardlinked elsewhere", fsx.Bytes(it.Reclaim), fsx.Bytes(it.Size)), sYellow)
	}
	if age := it.Age(m.now); age > 0 {
		v := "last used " + fsx.Age(age) + " ago (" + it.LastUsed.Format("2006-01-02") + ")"
		if age >= m.stale {
			v += " · stale"
		}
		add("Age", v, lipgloss.NewStyle())
	}
	add("Warning", it.Warn, sOrange)
	add("Note", it.Note, sSubtle)
	if it.Project != "" {
		add("Project", m.env.Pretty(it.Project), sSubtle)
	}
	if len(it.Meta) > 0 {
		keys := make([]string, 0, len(it.Meta))
		for k := range it.Meta {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var kv []string
		for _, k := range keys {
			kv = append(kv, k+"="+it.Meta[k])
		}
		add("Meta", strings.Join(kv, " · "), sCyan)
	}
	if len(lines) > rows {
		lines = lines[:rows]
	}
	return fitWidth(strings.Join(lines, "\n"), w)
}

// ------------------------------------------------------------------ status & footer

func (m *pickerModel) viewStatus(w int) string {
	if m.filter.active {
		return pad(sCyan.Render("/ ")+m.filter.view()+sDim.Render("   enter keep · esc clear · ↑↓ move"), w)
	}
	if m.status == "" {
		if it := m.currentItem(); it != nil && it.Warn != "" && !m.showDetails() {
			return pad(sOrange.Render("⚠ "+it.Warn), w)
		}
		if m.screen == scrCategories && m.catCursor < len(m.cats) {
			return pad(sDim.Render(m.cats[m.catCursor].info.Desc), w)
		}
		return ""
	}
	var st lipgloss.Style
	switch m.statusKind {
	case stOK:
		st = sGreen
	case stWarn:
		st = sOrange
	case stErr:
		st = sRedB
	default:
		st = sSubtle
	}
	return pad(st.Render(m.status), w)
}

func (m *pickerModel) modeLabel() string {
	var s string
	switch {
	case m.opt.Clean.DryRun:
		s = sCyan.Render("○ dry-run")
	case m.trash:
		s = sYellow.Render("🗑 Trash mode")
	default:
		s = sRed.Render("delete permanently")
	}
	if m.opt.Clean.Force {
		s += sRedB.Render(" · force")
	}
	return s
}

func (m *pickerModel) viewFooter(w int) string {
	var sel string
	if n := len(m.selItems); n > 0 {
		sel = sGreen.Render("✓ ") + sBold.Render(fmt.Sprintf("%d selected · %s", n, fsx.Bytes(m.selTotal)))
	} else {
		sel = sDim.Render("nothing selected")
	}
	left := sel + sDim.Render(" │ ") + m.modeLabel() + sDim.Render(" │ ")
	var keys []string
	switch m.mode {
	case modeCleaning:
		keys = []string{"ctrl+c", "cancel"}
	case modeSummary:
		keys = []string{"any key", "back to the list"}
	default:
		if m.screen == scrCategories {
			keys = []string{"enter", "open", "␣", "select", "a", "smart", "d", "clean", "t", "trash", "/", "filter", "?", "help", "q", "quit"}
		} else {
			keys = []string{"␣", "select", "a", "smart", "d", "clean", "s", "sort", "/", "filter", "←", "back", "?", "help", "q", "quit"}
		}
	}
	return left + keyHelpFit(w-width(left), keys...)
}

// ------------------------------------------------------------------ overlays

func (m *pickerModel) viewHelp(w, h int) string {
	k := func(key, desc string) string { return sKey.Render(pad(key, 16)) + sSubtle.Render(desc) }
	sec := func(t string) string { return sAccentB.Render(t) }
	lines := []string{
		sec("Navigation"),
		k("↑/k ↓/j", "move · pgup/pgdn page · g/G top/bottom"),
		k("→ l enter", "open category / toggle details"),
		k("← h esc", "back to categories"),
		k("tab shift+tab", "next / previous category"),
		sec("Selection"),
		k("space", "toggle item (on a category: all but caution items)"),
		k("a", "smart select: safe caches + stale regenerable items"),
		k("A  n  i", "select all · select none · invert (current view)"),
		sec("View"),
		k("s", "sort: size / age / name (categories: order / size)"),
		k("/", "filter by text (esc clears)"),
		k("H or .", "show empty / hidden entries"),
		sec("Actions"),
		k("d or x", "clean the selection (asks for confirmation)"),
		k("t", "toggle Trash mode (Trash frees nothing until emptied)"),
		k("o", "reveal in Finder"),
		k("q  ctrl+c", "quit · ctrl+c cancels a running clean"),
		"",
		sGreen.Render("safe") + sSubtle.Render(" cache · ") + sYellow.Render("moderate") + sSubtle.Render(" regenerable · ") +
			sOrange.Render("caution") + sSubtle.Render(" may hold data · ") + sDim.Render("never") + sSubtle.Render(" report only"),
	}
	if len(m.provErrs) > 0 {
		lines = append(lines, "", sOrange.Render("Provider errors"))
		for _, e := range m.provErrs {
			lines = append(lines, sOrange.Render("• "+e))
		}
	}
	inner := min(w-6, 78)
	maxLines := max(3, h-4)
	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	body := fitWidth(strings.Join(lines, "\n"), inner)
	return sBox.Render(body)
}

func (m *pickerModel) viewConfirm(w, h int) string {
	c := m.confirm
	if c == nil {
		return ""
	}
	inner := min(w-6, 74)
	var lines []string
	title := fmt.Sprintf("Clean %s — %s?", plural(len(c.items), "item"), fsx.Bytes(c.total))
	if m.opt.Clean.DryRun {
		title = fmt.Sprintf("Dry-run %s — %s?", plural(len(c.items), "item"), fsx.Bytes(c.total))
	}
	lines = append(lines, sBold.Render(title), "")
	for r := core.RiskSafe; r <= core.RiskNever; r++ {
		rc := c.byRisk[r]
		if rc.n == 0 {
			continue
		}
		lines = append(lines, "  "+riskBadge(r, 9)+padLeft(plural(rc.n, "item"), 11)+"  "+padLeft(fsx.Bytes(rc.size), 9))
	}
	lines = append(lines, "")
	label := func(s string) string { return sSubtle.Render(pad(s, 10)) }
	switch {
	case m.opt.Clean.DryRun:
		lines = append(lines, label("Method")+sCyan.Render("dry-run — nothing will be touched"))
	case m.trash:
		lines = append(lines, label("Method")+sYellow.Render("move to Trash — space is NOT freed until you empty it"))
	default:
		lines = append(lines, label("Method")+sRed.Render("delete permanently")+sSubtle.Render(" — frees space now"))
	}
	for i, cmd := range c.commands {
		if i == 4 {
			lines = append(lines, label("")+sDim.Render(fmt.Sprintf("and %d more", len(c.commands)-4)))
			break
		}
		l := ""
		if i == 0 {
			l = "Commands"
		}
		lines = append(lines, label(l)+sCyan.Render(cmd))
	}
	switch {
	case c.checking:
		lines = append(lines, label("Running")+sDim.Render("checking "+strings.Join(c.guards, ", ")+"…"))
	case len(c.running) > 0:
		msg := " — items guarded by them will be skipped"
		if m.opt.Clean.Force {
			msg = " — force: will clean anyway"
		}
		lines = append(lines, label("Running")+sOrange.Render(strings.Join(c.running, ", ")+msg))
	}
	if h >= 24 && len(c.largest) > 0 {
		var names []string
		for _, it := range c.largest {
			names = append(names, fmt.Sprintf("%s (%s)", it.Name, fsx.Bytes(it.Freed())))
		}
		lines = append(lines, label("Largest")+sSubtle.Render(joinFit(names, inner-10)))
	}
	if m.scanning {
		lines = append(lines, label("")+sDim.Render("scan still running: sizes may be partial"))
	}
	lines = append(lines, "")
	if c.caution > 0 {
		lines = append(lines,
			sRedB.Render(fmt.Sprintf("⚠ %s selected: may hold data you care about.", plural(c.caution, "caution item"))),
			sBold.Render("Type yes to confirm: ")+c.input.view(),
		)
		if c.hint != "" {
			lines = append(lines, sOrange.Render(c.hint))
		}
		lines = append(lines, "", keyHelp("enter", "confirm", "esc", "cancel"))
	} else {
		lines = append(lines, keyHelp("y/enter", "confirm", "n/esc", "cancel"))
	}
	body := fitWidth(strings.Join(lines, "\n"), inner)
	if c.caution > 0 {
		return sBoxDanger.Render(body)
	}
	return sBox.Render(body)
}

// joinFit joins names with ", " and cuts the result at w cells.
func joinFit(names []string, w int) string {
	return pad(strings.Join(names, ", "), w)
}

// ------------------------------------------------------------------ cleaning & summary

func (m *pickerModel) viewRun(w, h int) string {
	run := m.run
	if run == nil {
		return ""
	}
	var lines []string
	if m.mode == modeCleaning {
		verb := "Cleaning"
		if m.opt.Clean.DryRun {
			verb = "Dry-run"
		} else if m.trash {
			verb = "Moving to Trash"
		}
		el := time.Since(run.start).Round(time.Second)
		lines = append(lines, "", m.spin.View()+" "+sBold.Render(fmt.Sprintf("%s %s — %s", verb, plural(run.total, "item"), fsx.Bytes(run.totalBytes)))+
			sSubtle.Render(fmt.Sprintf("   %s", el))+pick(run.cancelled, sOrange.Render("   cancelling…"), sDim.Render("   ctrl+c cancels")))
		lines = append(lines, "")
	} else {
		lines = append(lines, "", m.viewSummaryCard(w))
	}
	bw := clamp(w-30, 10, 50)
	fi, fb := 0.0, 0.0
	if run.total > 0 {
		fi = float64(run.processed) / float64(run.total)
	}
	if run.totalBytes > 0 {
		fb = float64(run.procBytes) / float64(run.totalBytes)
	} else {
		fb = fi
	}
	lines = append(lines,
		sSubtle.Render("items ")+bar(fi, bw, sAccent)+" "+fmt.Sprintf("%d/%d", run.processed, run.total),
		sSubtle.Render("data  ")+bar(fb, bw, sAccent)+" "+fmt.Sprintf("%s / %s", fsx.Bytes(run.procBytes), fsx.Bytes(run.totalBytes)),
		sSubtle.Render("freed ")+sGreen.Render(fsx.Bytes(run.freed)),
		"",
	)
	room := h - len(lines) - 1
	if m.mode == modeSummary {
		lines = append(lines, sDim.Render("press any key to return to the list"))
		room--
	}
	if room > 0 {
		results := run.results
		if m.mode == modeSummary {
			results = problemsFirst(results)
			if len(results) > room {
				results = results[:room]
			}
		} else if len(results) > room {
			results = results[len(results)-room:]
		}
		for _, r := range results {
			lines = append(lines, m.resultLine(r, w))
		}
	}
	return strings.Join(lines, "\n")
}

// problemsFirst orders failed, then skipped, then the rest (stable).
func problemsFirst(rs []clean.Result) []clean.Result {
	out := append([]clean.Result(nil), rs...)
	rank := func(s clean.Status) int {
		switch s {
		case clean.StatusFailed:
			return 0
		case clean.StatusSkipped:
			return 1
		}
		return 2
	}
	sort.SliceStable(out, func(i, j int) bool { return rank(out[i].Status) < rank(out[j].Status) })
	return out
}

func (m *pickerModel) resultLine(r clean.Result, w int) string {
	name := ""
	size := int64(0)
	if r.Item != nil {
		name = r.Item.Name
		size = r.Item.Freed()
	}
	nw := clamp(w/3, 12, 40)
	var icon, msg string
	var st lipgloss.Style
	switch r.Status {
	case clean.StatusDone:
		icon, st = "✓", sGreen
		msg = r.Message
		if msg == "" && r.Item != nil {
			msg = m.env.Pretty(r.Item.Where())
		}
	case clean.StatusDryRun:
		icon, st, msg = "○", sCyan, r.Message
	case clean.StatusSkipped:
		icon, st, msg = "↷", sYellow, "skipped: "+r.Message
	default:
		icon, st, msg = "✗", sRed, "failed: "+r.Error
	}
	rest := w - 2 - nw - 1 - 10 - 1
	return st.Render(icon) + " " + pad(name, nw) + " " + sBold.Render(padLeft(fsx.Bytes(size), 10)) + " " + st.Render(pad(msg, max(0, rest)))
}

func (m *pickerModel) viewSummaryCard(w int) string {
	s := m.lastSummary
	if s == nil {
		return ""
	}
	inner := min(w-6, 76)
	var lines []string
	done, skipped, failed, dry := s.Count(clean.StatusDone), s.Count(clean.StatusSkipped), s.Count(clean.StatusFailed), s.Count(clean.StatusDryRun)
	switch {
	case s.DryRun:
		var est int64
		for _, r := range s.Results {
			if r.Status == clean.StatusDryRun {
				est += r.Freed
			}
		}
		lines = append(lines, sCyan.Render("○ Dry-run finished")+sSubtle.Render(fmt.Sprintf(" in %.1fs", s.Took.Seconds())),
			fmt.Sprintf("%s would free about %s — nothing was touched", plural(dry, "item"), sBold.Render(fsx.Bytes(est))))
	default:
		lines = append(lines, sGreen.Render("✓ Cleaning finished")+sSubtle.Render(fmt.Sprintf(" in %.1fs", s.Took.Seconds())))
		counts := sGreen.Render(fmt.Sprintf("%d done", done))
		if skipped > 0 {
			counts += sSubtle.Render(" · ") + sYellow.Render(fmt.Sprintf("%d skipped", skipped))
		}
		if failed > 0 {
			counts += sSubtle.Render(" · ") + sRed.Render(fmt.Sprintf("%d failed", failed))
		}
		lines = append(lines, counts, "")
		lines = append(lines, sSubtle.Render(pad("Estimated freed", 17))+sBold.Render(fsx.Bytes(s.Estimated)))
		if s.DiskBefore.Total > 0 {
			lines = append(lines, sSubtle.Render(pad("Measured freed", 17))+sBold.Render(fsx.Bytes(s.Measured))+
				sSubtle.Render(fmt.Sprintf("   (%s → %s free)", fsx.Bytes(s.DiskBefore.Free), fsx.Bytes(s.DiskAfter.Free))))
		}
		if s.Trash {
			lines = append(lines, "", sYellow.Render("🗑 Items were moved to the Trash: empty it to really free the space."))
		} else if s.Estimated > 0 && s.Measured < s.Estimated/2 {
			lines = append(lines, "", sYellow.Render("Space not fully returned? APFS snapshots / hardlinks / clones / Trash — run lu-cleaner doctor"))
		}
	}
	return sBox.Render(fitWidth(ansi.Wrap(strings.Join(lines, "\n"), inner, ""), inner))
}
