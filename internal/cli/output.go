package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
)

// output renders text on stdout: styled with lipgloss on a color terminal,
// plain and emoji-free otherwise (pipes, files, --no-color).
type output struct {
	w     io.Writer
	tty   bool // stdout is a terminal (icons, truncation to the width)
	color bool
	width int

	bold, dim, faint, title, good, warn, bad, accent, size lipgloss.Style
}

func newOutput(a *App, noColor bool) *output {
	o := &output{w: a.Stdout, tty: a.StdoutTTY}
	if a.StdoutTTY && a.Width != nil {
		o.width = a.Width()
	}
	r := lipgloss.NewRenderer(a.Stdout)
	o.color = a.StdoutTTY && !noColor
	o.bold = r.NewStyle().Bold(true)
	o.dim = r.NewStyle().Faint(true)
	o.faint = r.NewStyle().Foreground(lipgloss.Color("8"))
	o.title = r.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	o.good = r.NewStyle().Foreground(lipgloss.Color("2"))
	o.warn = r.NewStyle().Foreground(lipgloss.Color("3"))
	o.bad = r.NewStyle().Foreground(lipgloss.Color("1"))
	o.accent = r.NewStyle().Foreground(lipgloss.Color("5")).Bold(true)
	o.size = r.NewStyle().Bold(true)
	return o
}

// paint applies st when colors are enabled.
func (o *output) paint(st lipgloss.Style, s string) string {
	if !o.color || s == "" {
		return s
	}
	return st.Render(s)
}

func (o *output) printf(format string, args ...any) { fmt.Fprintf(o.w, format, args...) }
func (o *output) println(args ...any)               { fmt.Fprintln(o.w, args...) }

// riskPaint colors s (a risk label) according to r.
func (o *output) riskPaint(r core.Risk, s string) string {
	switch r {
	case core.RiskSafe:
		return o.paint(o.good, s)
	case core.RiskModerate:
		return o.paint(o.warn, s)
	}
	return o.paint(o.bad, s)
}

// sizeText formats a size, highlighting the big ones.
func (o *output) sizeText(n int64) string {
	s := fsx.Bytes(n)
	switch {
	case n >= 10e9:
		return o.paint(o.bad.Bold(true), s)
	case n >= 1e9:
		return o.paint(o.warn.Bold(true), s)
	case n >= 100e6:
		return o.paint(o.size, s)
	}
	return s
}

// catTitle is the category title, with its icon on a terminal only.
// Unknown categories are titled with a provider-defined id: sanitized.
func (o *output) catTitle(info core.CategoryInfo) string {
	if o.tty && info.Icon != "" {
		return info.Icon + " " + sanitize(info.Title)
	}
	return sanitize(info.Title)
}

// writeJSON prints v as indented JSON on stdout. encoding/json escapes C0
// controls but writes DEL, C1 controls and bidi overrides raw: they are
// escaped too (\uXXXX, same decoded value) so `--json` on a terminal cannot
// be hijacked by a crafted file name either.
func (c *cli) writeJSON(v any) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return err
	}
	_, err := c.Stdout.Write(escapeJSONControls(buf.Bytes()))
	return err
}

// escapeJSONControls rewrites the unsafe runes left raw by encoding/json
// (DEL, C1 controls, bidi controls: C0 controls are already escaped inside
// strings, and outside them are the JSON's own line breaks) as \uXXXX
// escapes. They can only occur inside JSON strings (the syntax is ASCII),
// where the escape decodes to the same rune.
func escapeJSONControls(b []byte) []byte {
	raw := func(r rune) bool { return r >= 0x20 && unsafeRune(r) }
	if !bytes.ContainsFunc(b, raw) {
		return b
	}
	out := make([]byte, 0, len(b)+32)
	for len(b) > 0 {
		r, size := utf8.DecodeRune(b)
		if raw(r) {
			out = fmt.Appendf(out, `\u%04x`, r)
		} else {
			out = append(out, b[:size]...)
		}
		b = b[size:]
	}
	return out
}

// ------------------------------------------------------------ sanitizing

// unsafeRune reports runes a terminal interprets instead of printing them:
// C0 controls (ESC, BEL, CR, LF…), DEL, C1 controls (U+0080–U+009F, CSI and
// OSC in their 8-bit form) and the bidi overrides/isolates that reorder
// the rest of a line (Trojan Source style spoofing).
func unsafeRune(r rune) bool {
	switch {
	case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
		return true
	case r == 0x061c, r == 0x200e, r == 0x200f,
		r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
		return true
	}
	return false
}

// sanitize makes s safe to print on a terminal: unsafe runes (see
// unsafeRune) become visible Go escapes (\x1b, \n, \u202e…) and invalid
// UTF-8 bytes become \xNN. Every name, path, message or warning that comes
// from the file system, a provider or an external tool goes through it
// before reaching human output (JSON is escaped by writeJSON).
func sanitize(s string) string {
	ok := true
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if (r == utf8.RuneError && size == 1) || unsafeRune(r) {
			ok = false
			break
		}
		i += size
	}
	if ok {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			fmt.Fprintf(&b, `\x%02x`, s[i])
		case unsafeRune(r):
			q := strconv.QuoteRune(r) // "'\x1b'"
			b.WriteString(q[1 : len(q)-1])
		default:
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String()
}

// sanitizeLines is sanitize for multi-line text: line breaks are kept (each
// line is sanitized on its own), continuation lines are indented so they can
// never pass for a line of their own.
func sanitizeLines(s, indent string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		l = sanitize(l)
		if i > 0 {
			l = indent + l
		}
		lines[i] = l
	}
	return strings.Join(lines, "\n")
}

// ------------------------------------------------------------------ tables

// table is a simple aligned text table. Widths are computed on the plain
// text; styles are applied afterwards so ANSI codes never break alignment.
type table struct {
	headers []string
	right   []bool // right-aligned columns
	// maxw bounds a column width on a terminal (0 = unbounded); cells are
	// truncated with "…" (from the left for columns in leftTrunc).
	maxw      []int
	leftTrunc []bool
	// shrink is the column truncated first to fit the terminal width (-1 = none).
	shrink int
	rows   []tableRow
}

type tableRow struct {
	cells []string
	// paint styles a (padded) cell; nil = plain.
	paint func(col int, s string) string
	// raw lines are printed as-is (section headers, notes): not part of the grid.
	raw   string
	isRaw bool
}

func newTable(headers ...string) *table {
	n := len(headers)
	return &table{headers: headers, right: make([]bool, n), maxw: make([]int, n), leftTrunc: make([]bool, n), shrink: -1}
}

// add appends a grid row. Cells are plain text (styles are applied by
// paint): they are sanitized here, so no table can print a raw control
// character whatever the cell comes from.
func (t *table) add(paint func(int, string) string, cells ...string) {
	clean := make([]string, len(cells))
	for i, c := range cells {
		clean[i] = sanitize(c)
	}
	t.rows = append(t.rows, tableRow{cells: clean, paint: paint})
}

// line appends a raw line, printed as-is (it may hold ANSI styles): callers
// sanitize the untrusted parts before styling them.
func (t *table) line(s string) { t.rows = append(t.rows, tableRow{raw: s, isRaw: true}) }

// render writes the table. indent prefixes every grid line. On a terminal,
// columns are bounded by maxw and the table is fitted to o.width.
func (t *table) render(o *output, indent string, showHeader bool) {
	n := len(t.headers)
	widths := make([]int, n)
	for i, h := range t.headers {
		widths[i] = dispWidth(h)
	}
	for _, r := range t.rows {
		if r.isRaw {
			continue
		}
		for i := 0; i < n && i < len(r.cells); i++ {
			if w := dispWidth(r.cells[i]); w > widths[i] {
				widths[i] = w
			}
		}
	}
	if o.tty {
		for i := range widths {
			if t.maxw[i] > 0 && widths[i] > t.maxw[i] {
				widths[i] = max(t.maxw[i], dispWidth(t.headers[i]))
			}
		}
		if o.width > 0 && t.shrink >= 0 {
			t.fitWidths(widths, o.width-dispWidth(indent)-2*(n-1))
		}
	}

	fit := func(i int, s string) string {
		if w := dispWidth(s); w > widths[i] {
			if t.leftTrunc[i] {
				return truncLeft(s, widths[i])
			}
			return truncRight(s, widths[i])
		}
		return s
	}
	pad := func(i int, s string) string {
		gap := widths[i] - dispWidth(s)
		if gap <= 0 {
			return s
		}
		if t.right[i] {
			return strings.Repeat(" ", gap) + s
		}
		if i == n-1 {
			return s // no trailing spaces
		}
		return s + strings.Repeat(" ", gap)
	}
	writeRow := func(cells []string, paint func(int, string) string) {
		var b strings.Builder
		b.WriteString(indent)
		for i := 0; i < n; i++ {
			cell := ""
			if i < len(cells) {
				cell = cells[i]
			}
			cell = pad(i, fit(i, cell))
			if paint != nil {
				// Style only the text, keep the padding outside.
				trimmed := strings.TrimRight(cell, " ")
				lead := len(trimmed) - len(strings.TrimLeft(trimmed, " "))
				text := strings.TrimLeft(trimmed, " ")
				cell = trimmed[:lead] + paint(i, text) + cell[len(trimmed):]
			}
			b.WriteString(cell)
			if i < n-1 {
				b.WriteString("  ")
			}
		}
		fmt.Fprintln(o.w, strings.TrimRight(b.String(), " "))
	}
	if showHeader {
		writeRow(t.headers, func(_ int, s string) string { return o.paint(o.dim, s) })
	}
	for _, r := range t.rows {
		if r.isRaw {
			fmt.Fprintln(o.w, r.raw)
			continue
		}
		writeRow(r.cells, r.paint)
	}
}

// fitWidths shrinks column widths so their sum fits in avail: first the
// shrink column down to 24, then the other bounded columns down to 12, then
// the shrink column down to 8. Columns never grow.
func (t *table) fitWidths(widths []int, avail int) {
	over := -avail
	for _, w := range widths {
		over += w
	}
	reduce := func(i, floor int) {
		if over <= 0 || widths[i] <= floor {
			return
		}
		d := min(over, widths[i]-floor)
		widths[i] -= d
		over -= d
	}
	reduce(t.shrink, 24)
	for i := range widths {
		if i != t.shrink && t.maxw[i] > 0 {
			reduce(i, max(12, dispWidth(t.headers[i])))
		}
	}
	reduce(t.shrink, 8)
}

// dispWidth is the terminal display width of s (ANSI aware, wide runes = 2).
func dispWidth(s string) int { return lipgloss.Width(s) }

// truncRight shortens s to w columns: "abcdef" -> "abc…".
func truncRight(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if dispWidth(s) <= w {
		return s
	}
	var b strings.Builder
	cur := 0
	for _, r := range s {
		rw := dispWidth(string(r))
		if cur+rw > w-1 {
			break
		}
		b.WriteRune(r)
		cur += rw
	}
	return b.String() + "…"
}

// truncLeft shortens s to w columns keeping the end: "/a/b/c/d" -> "…/c/d".
func truncLeft(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if dispWidth(s) <= w {
		return s
	}
	rs := []rune(s)
	cur := 0
	i := len(rs)
	for i > 0 {
		rw := dispWidth(string(rs[i-1]))
		if cur+rw > w-1 {
			break
		}
		cur += rw
		i--
	}
	return "…" + string(rs[i:])
}

// formatCount groups thousands with a space: 1234567 -> "1 234 567".
func formatCount(n int) string {
	s := strconv.Itoa(n)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%s %s", formatCount(n), many)
}

// ageText formats the age of an item ("—" when unknown).
func ageText(it *core.Item, now time.Time) string { return fsx.Age(it.Age(now)) }

// whereText is the location shown for an item (path, group location or command).
func whereText(env *core.Env, it *core.Item) string {
	if w := it.Where(); w != "" {
		return env.Pretty(w)
	}
	if len(it.Command) > 0 {
		return "$ " + strings.Join(it.Command, " ")
	}
	return ""
}

// ------------------------------------------------------------ status line

// statusWriter owns stderr: a transient status line (spinner) that log
// lines never get mixed with.
type statusWriter struct {
	mu    sync.Mutex
	w     io.Writer
	tty   bool
	width func() int
	shown bool // a status line is currently displayed
}

const clearLine = "\r\x1b[2K"

// printf writes a regular line, clearing the status line first.
func (s *statusWriter) printf(format string, args ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.shown {
		fmt.Fprint(s.w, clearLine)
		s.shown = false
	}
	fmt.Fprintf(s.w, format, args...)
}

// status replaces the status line (terminal only).
func (s *statusWriter) status(line string) {
	if !s.tty {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.width != nil {
		if w := s.width(); w > 1 {
			line = truncRight(line, w-1)
		}
	}
	fmt.Fprint(s.w, clearLine+line)
	s.shown = true
}

// clear removes the status line.
func (s *statusWriter) clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.shown {
		fmt.Fprint(s.w, clearLine)
		s.shown = false
	}
}

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// spinner redraws text() on the status line every 100ms until stop is called.
type spinner struct {
	stopc chan struct{}
	done  chan struct{}
	sw    *statusWriter
}

func (c *cli) startSpinner(text func() string) *spinner {
	sp := &spinner{stopc: make(chan struct{}), done: make(chan struct{}), sw: c.errw}
	if !c.errw.tty {
		close(sp.done)
		return sp
	}
	go func() {
		defer close(sp.done)
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()
		for i := 0; ; i++ {
			sp.sw.status(spinnerFrames[i%len(spinnerFrames)] + " " + text())
			select {
			case <-sp.stopc:
				return
			case <-t.C:
			}
		}
	}()
	return sp
}

// stop ends the spinner and clears its line.
func (sp *spinner) stop() {
	select {
	case <-sp.stopc:
	default:
		close(sp.stopc)
	}
	<-sp.done
	sp.sw.clear()
}
