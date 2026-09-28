package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/ludwig-pro/lu-cleaner/internal/clean"
	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
)

func (m *analyzeModel) View() string {
	if m.quitting {
		return ""
	}
	w, h := m.w, m.h
	if w < hardMinWidth || h < hardMinHeight {
		return tooSmall(w, h)
	}
	d := m.cur()
	var b strings.Builder
	title := sTitle.Render("🔍 analyze")
	b.WriteString(title + " " + sAccentB.Render(truncLeft(safeText(m.env.Pretty(m.cwd)), w-width(title)-1)))
	b.WriteByte('\n')
	b.WriteString(diskLine(m.disk, m.diskErr, w))
	b.WriteByte('\n')
	b.WriteString(m.viewTotal(d, w))
	b.WriteByte('\n')
	cols := m.cols(w)
	b.WriteString(sHeader.Render(cols.header()))
	b.WriteByte('\n')
	rows := m.listRows()
	b.WriteString(fitHeight(m.viewList(d, rows, cols), rows))
	b.WriteByte('\n')
	b.WriteString(m.viewStatus(w))
	b.WriteByte('\n')
	b.WriteString(m.viewFooter(w))
	out := fitWidth(b.String(), w)
	switch m.mode {
	case anHelp:
		out = overlay(out, m.viewHelp(w, h), w, h)
	case anConfirm:
		out = overlay(out, m.viewConfirm(w, h), w, h)
	case anDeleting:
		verb := "Deleting…"
		if m.opt.Clean.Trash && !m.opt.Clean.DryRun {
			verb = "Moving to the Trash…"
		}
		out = overlay(out, sBoxDanger.Render(m.spin.View()+" "+sBold.Render(verb)), w, h)
	case anResult:
		out = overlay(out, m.viewResult(w, h), w, h)
	}
	return out
}

func (m *analyzeModel) viewTotal(d *anDir, w int) string {
	if d == nil || !d.loaded {
		return m.spin.View() + sSubtle.Render(" reading directory…")
	}
	if d.err != nil {
		return sRed.Render("✗ " + safeText(d.err.Error()))
	}
	sum, files, complete, sized := m.total(d)
	s := sSubtle.Render("Total ") + sBold.Render(fsx.Bytes(sum))
	if !complete {
		s += sDim.Render("…")
	}
	s += sSubtle.Render(fmt.Sprintf(" · %s files · %s", thousands(files), plural(len(d.entries), "entry")))
	if !complete {
		s += "  " + m.spin.View() + sAccent.Render(fmt.Sprintf(" sizing %d/%d", sized, len(d.entries)))
	}
	s += sDim.Render(" · sort: " + m.sort.String())
	if !m.showHidden {
		s += sDim.Render(" · hidden files hidden")
	}
	return pad(s, w)
}

type anCols struct {
	size, pct, bar, files, age, name, tag int
}

func (m *analyzeModel) cols(w int) anCols {
	c := anCols{size: 10, pct: 7, bar: 16, files: 10, age: 6}
	if w < 130 {
		c.bar = 12
	}
	if w < 100 {
		c.bar = 10
	}
	if w < 80 {
		c.files = 0
		c.bar = 8
	}
	if w < 64 {
		c.age = 0
	}
	// marker 2 + mark 2 + separators
	rest := w - 4 - c.size - c.pct - 1 - c.bar - 1 - c.files - c.age - 1
	c.name = rest
	if rest >= 44 {
		c.name = clamp(rest*45/100, 20, 60)
		c.tag = rest - c.name - 1
	}
	return c
}

func (c anCols) header() string {
	s := "    " + padLeft("SIZE", c.size) + padLeft("%", c.pct) + " " + pad("", c.bar) + " "
	if c.files > 0 {
		s += padLeft("FILES", c.files)
	}
	if c.age > 0 {
		s += padLeft("AGE", c.age)
	}
	s += " " + pad("NAME", c.name)
	return s
}

func (m *analyzeModel) viewList(d *anDir, rows int, c anCols) string {
	if d == nil || !d.loaded || d.err != nil {
		return ""
	}
	if len(d.view) == 0 {
		if len(d.entries) > 0 {
			return sDim.Render("  only hidden files here — press . to show them")
		}
		return sDim.Render("  empty directory")
	}
	sum, _, _, _ := m.total(d)
	var largest int64
	for _, e := range d.view {
		largest = max(largest, e.size)
	}
	var lines []string
	end := min(len(d.view), d.offset+rows)
	for i := d.offset; i < end; i++ {
		lines = append(lines, m.entryLine(d.view[i], i == d.cursor, sum, largest, c))
	}
	return strings.Join(lines, "\n")
}

func (m *analyzeModel) entryLine(e *anEntry, cur bool, sum, largest int64, c anCols) string {
	marker := "  "
	if cur {
		marker = sAccentB.Render("› ")
	}
	mark := "  "
	if m.marked[e.path] != nil {
		mark = sRedB.Render("● ")
	}
	var size, pct, gauge string
	if e.sized {
		size = sBold.Render(padLeft(fsx.Bytes(e.size), c.size))
		p := 0.0
		if sum > 0 {
			p = float64(e.size) / float64(sum) * 100
		}
		pct = sSubtle.Render(padLeft(fmt.Sprintf("%.1f%%", p), c.pct))
		f := 0.0
		if largest > 0 {
			f = float64(e.size) / float64(largest)
		}
		gauge = bar(f, c.bar, sizeStyle(p))
	} else {
		size = sDim.Render(padLeft("…", c.size))
		pct = padLeft("", c.pct)
		gauge = sDim.Render(strings.Repeat("·", c.bar))
	}
	line := marker + mark + size + pct + " " + gauge + " "
	if c.files > 0 {
		f := ""
		if e.isDir && e.sized {
			f = thousands(e.files)
		}
		line += sSubtle.Render(padLeft(f, c.files))
	}
	if c.age > 0 {
		line += sSubtle.Render(padLeft(ageText(e.newest, m.now), c.age))
	}
	name := safeText(e.name)
	var nst lipgloss.Style
	switch {
	case e.isLink:
		name += "@"
		nst = sDim
	case e.isDir:
		name += "/"
		nst = sCyan
	default:
		nst = lipgloss.NewStyle()
	}
	if cur {
		nst = nst.Bold(true)
	}
	line += " " + nst.Render(pad(name, c.name))
	tag := e.tag
	if e.errs > 0 {
		if tag != "" {
			tag += " · "
		}
		tag += fmt.Sprintf("⚠ %d unreadable", e.errs)
	}
	if c.tag > 0 && tag != "" {
		st := sYellow
		switch e.git.kind {
		case gitWorktree:
			st = sGreen
		case gitRepo, gitOther:
			st = sDim // never deleted by the analyzer
		}
		line += " " + st.Render(pad(safeText(tag), c.tag))
	}
	return line
}

// sizeStyle colors a gauge by its share of the directory.
func sizeStyle(pct float64) lipgloss.Style {
	switch {
	case pct >= 40:
		return sRed
	case pct >= 15:
		return sOrange
	case pct >= 5:
		return sYellow
	}
	return sAccent
}

func (m *analyzeModel) viewStatus(w int) string {
	if m.status == "" {
		if e := m.curEntry(); e != nil {
			s := m.env.Pretty(e.path)
			switch {
			case e.isWorktree():
				s += "  (worktree of " + m.env.Pretty(e.git.main) + ")"
			case e.refusal() != "":
				s += "  (" + e.refusal() + " — never deleted here)"
			}
			return sDim.Render(truncLeft(safeText(s), w))
		}
		return ""
	}
	st := sSubtle
	switch m.statusKind {
	case stOK:
		st = sGreen
	case stWarn:
		st = sOrange
	case stErr:
		st = sRedB
	}
	return pad(st.Render(safeText(m.status)), w)
}

func (m *analyzeModel) viewFooter(w int) string {
	left := ""
	if n := len(m.marked); n > 0 {
		var es []*anEntry
		for _, e := range m.marked {
			es = append(es, e)
		}
		left = sRedB.Render("● ") + sBold.Render(fmt.Sprintf("%d marked · %s", n, fsx.Bytes(core.Total(m.itemsFor(es))))) + sDim.Render(" │ ")
	}
	switch {
	case m.opt.Clean.DryRun:
		left += sCyan.Render("dry-run") + sDim.Render(" │ ")
	case m.opt.Clean.Trash:
		left += sYellow.Render("Trash mode") + sDim.Render(" │ ")
	}
	return left + keyHelpFit(w-width(left),
		"→", "open", "←", "up", "␣", "mark", "d", "delete", "s", "sort", "o", "finder", "r", "rescan", ".", "hidden", "?", "help", "q", "quit")
}

func (m *analyzeModel) viewHelp(w, h int) string {
	k := func(key, desc string) string { return sKey.Render(pad(key, 14)) + sSubtle.Render(desc) }
	lines := []string{
		sAccentB.Render("Disk analyzer"),
		k("↑/k ↓/j", "move · pgup/pgdn page · g/G top/bottom"),
		k("→ l enter", "open directory"),
		k("← h backspace", "parent directory"),
		k("space", "mark / unmark (esc clears marks)"),
		k("d", "delete marked entries (or the current one) — asks for \"yes\""),
		k("s", "sort: size / name / age"),
		k(".", "show / hide hidden files"),
		k("r", "rescan the current directory"),
		k("o", "reveal in Finder"),
		k("q  ctrl+c", "quit"),
		"",
		sDim.Render("Sizes are allocated blocks on disk; hardlinks are counted once per entry."),
		sDim.Render("Deletions go through the safety guard: protected paths are refused."),
		sDim.Render("Entries that are git repositories, submodules or other checkouts are never deleted"),
		sDim.Render("(a folder holding some is deleted with them); linked worktrees go through git."),
	}
	if len(lines) > h-4 {
		lines = lines[:max(1, h-4)]
	}
	return sBox.Render(fitWidth(strings.Join(lines, "\n"), min(w-6, 78)))
}

func (m *analyzeModel) viewConfirm(w, h int) string {
	inner := min(w-6, 78)
	trash := m.opt.Clean.Trash && !m.opt.Clean.DryRun
	var lines []string
	verb := "Delete"
	switch {
	case m.opt.Clean.DryRun:
		verb = "Dry-run delete"
	case trash:
		verb = "Move to Trash"
	}
	n := len(m.confirm)
	notes := make([]string, len(m.confirm))
	pathW, noteW := 0, 0
	for i, e := range m.confirm {
		notes[i] = e.tag
		if e.isWorktree() {
			notes[i] = "🌳 worktree → git worktree remove"
			if m.opt.Clean.Trash {
				notes[i] = "↷ skipped in Trash mode"
				n--
			}
		}
		pathW = max(pathW, width(safeText(m.env.Pretty(e.path))))
		noteW = max(noteW, width(notes[i]))
	}
	lines = append(lines, sBold.Render(fmt.Sprintf("%s %s — %s?", verb, plural(n, "entry"), fsx.Bytes(m.confTotal))), "")
	show := clamp(h-14, 1, 8)
	noteW = min(noteW, inner/3)
	pathW = clamp(pathW, 8, max(8, inner-2-2-9-2-noteW))
	for i, e := range m.confirm {
		if i == show {
			lines = append(lines, sDim.Render(fmt.Sprintf("  and %d more", len(m.confirm)-show)))
			break
		}
		size := "…"
		if e.sized {
			size = fsx.Bytes(e.size)
		}
		line := "  " + padTruncLeft(safeText(m.env.Pretty(e.path)), pathW) + "  " + sBold.Render(padLeft(size, 9))
		if noteW > 0 {
			line += "  " + sYellow.Render(pad(safeText(notes[i]), noteW))
		}
		lines = append(lines, line)
	}
	if skipped := len(m.confirm) - n; skipped > 0 {
		lines = append(lines, sOrange.Render(fmt.Sprintf("  ↷ %s skipped:", plural(skipped, "worktree"))),
			sOrange.Render("    "+trashSkipMsg))
	}
	if k := len(m.confRefused); k > 0 {
		names := make([]string, 0, min(k, 3))
		for i, e := range m.confRefused {
			if i == 3 {
				names = append(names, "…")
				break
			}
			names = append(names, safeText(e.name))
		}
		lines = append(lines, sOrange.Render(pad(fmt.Sprintf("  %s left out (git repository or checkout, never deleted here): %s",
			plural(k, "marked entry"), strings.Join(names, ", ")), inner)))
	}
	lines = append(lines, "")
	label := func(s string) string { return sSubtle.Render(pad(s, 9)) }
	switch {
	case m.opt.Clean.DryRun:
		lines = append(lines, label("Method")+sCyan.Render("dry-run — nothing will be touched"))
	case trash:
		lines = append(lines, label("Method")+sYellow.Render("move to Trash — space is NOT freed until you empty it"))
	default:
		lines = append(lines, label("Method")+sRed.Render("delete permanently")+sSubtle.Render(" — cannot be undone"))
	}
	lines = append(lines, label("Safety")+sSubtle.Render("every path is re-checked by the safety guard"), "")
	lines = append(lines, sBold.Render("Type yes to confirm: ")+m.input.view())
	if m.hint != "" {
		lines = append(lines, sOrange.Render(m.hint))
	}
	lines = append(lines, "", keyHelp("enter", "confirm", "esc", "cancel"))
	return sBoxDanger.Render(fitWidth(strings.Join(lines, "\n"), inner))
}

func (m *analyzeModel) viewResult(w, h int) string {
	s := m.result
	inner := min(w-6, 90)
	if s == nil {
		return sBox.Render("nothing done")
	}
	var lines []string
	freed, moved, nMoved := trashSplit(s)
	nDeleted := s.Count(clean.StatusDone) - nMoved
	var head string
	switch {
	case s.DryRun:
		head = sCyan.Render(fmt.Sprintf("○ dry-run: %d would be deleted", s.Count(clean.StatusDryRun)))
		if s.Trash {
			head = sCyan.Render(fmt.Sprintf("○ dry-run: %d would be moved to the Trash", s.Count(clean.StatusDryRun)))
		}
	case nMoved > 0 && nDeleted > 0:
		head = sGreen.Render(fmt.Sprintf("✓ %d deleted · %d moved to the Trash", nDeleted, nMoved))
	case nMoved > 0:
		head = sGreen.Render(fmt.Sprintf("✓ %d moved to the Trash", nMoved))
	default:
		head = sGreen.Render(fmt.Sprintf("✓ %d deleted", nDeleted))
	}
	if n := s.Count(clean.StatusSkipped); n > 0 {
		head += sSubtle.Render(" · ") + sYellow.Render(fmt.Sprintf("↷ %d skipped", n))
	}
	if n := s.Count(clean.StatusFailed); n > 0 {
		head += sSubtle.Render(" · ") + sRed.Render(fmt.Sprintf("✗ %d failed", n))
	}
	if !s.DryRun {
		// a move to the Trash frees nothing until the Trash is emptied
		if freed > 0 || nMoved == 0 {
			head += sSubtle.Render(" · freed ") + sBold.Render(fsx.Bytes(freed))
		}
		if nMoved > 0 {
			head += sSubtle.Render(" · in the Trash ") + sBold.Render(fsx.Bytes(moved))
		}
	}
	lines = append(lines, head, "")
	show := clamp(h-10, 1, 10)
	for i, r := range problemsFirst(s.Results) {
		if i == show {
			lines = append(lines, sDim.Render(fmt.Sprintf("and %d more", len(s.Results)-show)))
			break
		}
		name := ""
		if r.Item != nil {
			name = safeText(r.Item.Name)
		}
		switch r.Status {
		case clean.StatusDone:
			lines = append(lines, sGreen.Render("✓ ")+name)
		case clean.StatusDryRun:
			lines = append(lines, sCyan.Render("○ ")+name+sSubtle.Render("  "+safeText(r.Message)))
		case clean.StatusSkipped:
			lines = append(lines, sYellow.Render("↷ ")+name+sSubtle.Render("  "+safeText(r.Message)))
		default:
			lines = append(lines, sRed.Render("✗ ")+name+sSubtle.Render("  "+safeText(r.Error)))
		}
	}
	if nMoved > 0 {
		lines = append(lines, "", sYellow.Render("🗑 Moved to the Trash: empty it to really free the space."))
	}
	lines = append(lines, "", sDim.Render("press any key"))
	return sBox.Render(fitWidth(strings.Join(lines, "\n"), inner))
}
