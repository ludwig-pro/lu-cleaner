package tui

import (
	"fmt"
	"math"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
	"github.com/ludwig-pro/lu-cleaner/internal/sysx"
)

const resetSeq = "\x1b[0m"

var eighths = []string{"", "▏", "▎", "▍", "▌", "▋", "▊", "▉"}

// bar renders a horizontal gauge of w cells filled at frac (0..1), with
// eighth-block precision. The empty part uses a light shade so the gauge stays
// readable without colors.
func bar(frac float64, w int, fill lipgloss.Style) string {
	if w <= 0 {
		return ""
	}
	if math.IsNaN(frac) || frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	units := int(math.Round(frac * float64(w) * 8))
	full := units / 8
	rem := units % 8
	s := strings.Repeat("█", full)
	used := full
	if rem > 0 && used < w {
		s += eighths[rem]
		used++
	}
	return fill.Render(s) + sTrack.Render(strings.Repeat("░", w-used))
}

// diskColor picks green / yellow / red for a usage percentage.
func diskColor(pct float64) lipgloss.Style {
	switch {
	case pct >= 90:
		return sRed
	case pct >= 80:
		return sYellow
	}
	return sGreen
}

// diskLine renders "💾 [bar] 87% used · 62.3 GB free of 494 GB" within w cells.
func diskLine(d sysx.Disk, err error, w int) string {
	if err != nil || d.Total == 0 {
		return sDim.Render("disk: unknown")
	}
	pct := d.UsedPct()
	st := diskColor(pct)
	text := st.Render(fmt.Sprintf("%.0f%% used", pct)) + sSubtle.Render(" · ") +
		sBold.Render(fsx.Bytes(d.Free)) + sSubtle.Render(" free of "+fsx.Bytes(d.Total))
	bw := clamp(w-width(text)-6, 0, 24)
	if bw < 6 {
		return "💾 " + text
	}
	return "💾 " + bar(pct/100, bw, st) + " " + text
}

// lineInput is a minimal single-line text input (the bubbles textinput pulls a
// clipboard dependency we do not ship). The cursor is always at the end.
type lineInput struct {
	value  []rune
	active bool
}

func (in *lineInput) String() string { return string(in.value) }

func (in *lineInput) reset() { in.value = nil }

// handle applies an editing key and reports whether the value changed.
func (in *lineInput) handle(k tea.KeyMsg) bool {
	switch k.Type {
	case tea.KeyRunes:
		in.value = append(in.value, k.Runes...)
		return true
	case tea.KeySpace:
		in.value = append(in.value, ' ')
		return true
	case tea.KeyBackspace:
		if k.Alt {
			return in.deleteWord()
		}
		if len(in.value) > 0 {
			in.value = in.value[:len(in.value)-1]
			return true
		}
	case tea.KeyCtrlW:
		return in.deleteWord()
	case tea.KeyCtrlU:
		if len(in.value) > 0 {
			in.value = nil
			return true
		}
	}
	return false
}

func (in *lineInput) deleteWord() bool {
	if len(in.value) == 0 {
		return false
	}
	i := len(in.value)
	for i > 0 && in.value[i-1] == ' ' {
		i--
	}
	for i > 0 && in.value[i-1] != ' ' {
		i--
	}
	in.value = in.value[:i]
	return true
}

// view renders the value with a block cursor when active.
func (in *lineInput) view() string {
	if in.active {
		return string(in.value) + sAccent.Render("▏")
	}
	return string(in.value)
}

// splitRunes replays a burst of runes delivered as one key message (fast
// typing, or input read while the UI was busy) as individual key presses, so
// that "jjj" moves three times instead of matching no binding. Pastes are
// left alone.
func splitRunes(k tea.KeyMsg, handle func(tea.KeyMsg) tea.Cmd) (tea.Cmd, bool) {
	if k.Type != tea.KeyRunes || len(k.Runes) < 2 || k.Paste {
		return nil, false
	}
	cmds := make([]tea.Cmd, 0, len(k.Runes))
	for _, r := range k.Runes {
		cmds = append(cmds, handle(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}, Alt: k.Alt}))
	}
	return tea.Batch(cmds...), true
}

// overlay draws box centered over base (both multi-line strings) in a w×h
// screen. Base content stays visible around the box.
func overlay(base, box string, w, h int) string {
	lines := strings.Split(base, "\n")
	for len(lines) < h {
		lines = append(lines, "")
	}
	boxLines := strings.Split(box, "\n")
	bw := 0
	for _, l := range boxLines {
		bw = max(bw, width(l))
	}
	x := max(0, (w-bw)/2)
	y := max(0, (h-len(boxLines))/2)
	for i, bl := range boxLines {
		row := y + i
		if row >= len(lines) {
			break
		}
		bl = pad(bl, bw)
		baseLine := lines[row]
		left := ansi.Truncate(baseLine, x, "")
		if lw := width(left); lw < x {
			left += strings.Repeat(" ", x-lw)
		}
		right := ""
		if bwid := width(baseLine); bwid > x+bw {
			right = ansi.Cut(baseLine, x+bw, bwid)
		}
		lines[row] = left + resetSeq + bl + resetSeq + right
	}
	return strings.Join(lines, "\n")
}

// fitHeight pads or cuts s to exactly h lines.
func fitHeight(s string, h int) string {
	if h <= 0 {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > h {
		lines = lines[:h]
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

// fitWidth truncates every line of s to w cells.
func fitWidth(s string, w int) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if width(l) > w {
			lines[i] = ansi.Truncate(l, w, "…")
		}
	}
	return strings.Join(lines, "\n")
}

// keyHelp renders "key label" pairs compactly: "␣ select  a smart  q quit".
func keyHelp(pairs ...string) string {
	var parts []string
	for i := 0; i+1 < len(pairs); i += 2 {
		parts = append(parts, sKey.Render(pairs[i])+" "+sSubtle.Render(pairs[i+1]))
	}
	return strings.Join(parts, "  ")
}

// keyHelpFit renders as many key pairs as fit in w cells.
func keyHelpFit(w int, pairs ...string) string {
	out := ""
	for i := 0; i+1 < len(pairs); i += 2 {
		part := sKey.Render(pairs[i]) + " " + sSubtle.Render(pairs[i+1])
		cand := part
		if out != "" {
			cand = out + "  " + part
		}
		if width(cand) > w {
			break
		}
		out = cand
	}
	return out
}

// tooSmall is shown when the terminal cannot hold a usable layout.
func tooSmall(w, h int) string {
	msg := fmt.Sprintf("Terminal too small (%dx%d)\nneed at least %dx%d\n\nq to quit", w, h, minWidth, minHeight)
	return lipgloss.Place(max(w, 1), max(h, 1), lipgloss.Center, lipgloss.Center, msg)
}

const (
	minWidth  = 60
	minHeight = 15
)
