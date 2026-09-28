package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
)

// Palette. Adaptive colors keep the UI readable on dark and light terminals;
// NO_COLOR is honoured by lipgloss (every state also has a textual marker).
var (
	colAccent = lipgloss.AdaptiveColor{Light: "#5B3CD6", Dark: "#9E82FF"}
	colBrand  = lipgloss.Color("#7D56F4")
	colCyan   = lipgloss.AdaptiveColor{Light: "#00718F", Dark: "#5FD7FF"}
	colGreen  = lipgloss.AdaptiveColor{Light: "#1A7F37", Dark: "#5FD787"}
	colYellow = lipgloss.AdaptiveColor{Light: "#8A6100", Dark: "#FFD75F"}
	colOrange = lipgloss.AdaptiveColor{Light: "#B54A00", Dark: "#FF9F5F"}
	colRed    = lipgloss.AdaptiveColor{Light: "#C4162A", Dark: "#FF6B81"}
	colDim    = lipgloss.AdaptiveColor{Light: "#8C959F", Dark: "#6E7681"}
	colSubtle = lipgloss.AdaptiveColor{Light: "#57606A", Dark: "#A0A8B3"}
	colTrack  = lipgloss.AdaptiveColor{Light: "#D0D7DE", Dark: "#3A3F4B"}
)

var (
	sTitle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFFFFF")).Background(colBrand).Padding(0, 1)
	sAccent  = lipgloss.NewStyle().Foreground(colAccent)
	sAccentB = lipgloss.NewStyle().Foreground(colAccent).Bold(true)
	sCyan    = lipgloss.NewStyle().Foreground(colCyan)
	sGreen   = lipgloss.NewStyle().Foreground(colGreen)
	sYellow  = lipgloss.NewStyle().Foreground(colYellow)
	sOrange  = lipgloss.NewStyle().Foreground(colOrange)
	sRed     = lipgloss.NewStyle().Foreground(colRed)
	sRedB    = lipgloss.NewStyle().Foreground(colRed).Bold(true)
	sDim     = lipgloss.NewStyle().Foreground(colDim)
	sSubtle  = lipgloss.NewStyle().Foreground(colSubtle)
	sBold    = lipgloss.NewStyle().Bold(true)
	sKey     = lipgloss.NewStyle().Foreground(colCyan).Bold(true)
	sHeader  = lipgloss.NewStyle().Foreground(colSubtle).Bold(true)
	sTrack   = lipgloss.NewStyle().Foreground(colTrack)

	sBox       = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colAccent).Padding(0, 2)
	sBoxDanger = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colRed).Padding(0, 2)
)

// riskStyle returns the color used for a risk level.
func riskStyle(r core.Risk) lipgloss.Style {
	switch r {
	case core.RiskSafe:
		return sGreen
	case core.RiskModerate:
		return sYellow
	case core.RiskCaution:
		return sOrange
	}
	return sDim
}

// riskBadge renders a risk as a fixed-width colored word.
func riskBadge(r core.Risk, w int) string {
	return riskStyle(r).Render(pad(r.String(), w))
}

// width is the display width of s (ANSI aware, wide runes count double).
func width(s string) int { return ansi.StringWidth(s) }

// pad truncates (with …) or right-pads s to exactly w cells.
func pad(s string, w int) string {
	if w <= 0 {
		return ""
	}
	sw := width(s)
	if sw > w {
		s = ansi.Truncate(s, w, "…")
		sw = width(s)
	}
	if sw < w {
		s += strings.Repeat(" ", w-sw)
	}
	return s
}

// padLeft truncates or left-pads s to exactly w cells (right-aligned).
func padLeft(s string, w int) string {
	if w <= 0 {
		return ""
	}
	sw := width(s)
	if sw > w {
		s = ansi.Truncate(s, w, "…")
		sw = width(s)
	}
	if sw < w {
		s = strings.Repeat(" ", w-sw) + s
	}
	return s
}

// truncLeft keeps the end of s ("…/project/node_modules") so it fits w cells.
func truncLeft(s string, w int) string {
	if w <= 0 {
		return ""
	}
	sw := width(s)
	if sw <= w {
		return s
	}
	if w == 1 {
		return "…"
	}
	return ansi.TruncateLeft(s, sw-w+1, "…")
}

// padTruncLeft is truncLeft + right padding to exactly w cells.
func padTruncLeft(s string, w int) string { return pad(truncLeft(s, w), w) }

// plural returns "1 item" / "3 items" / "2 entries".
func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	if strings.HasSuffix(word, "y") {
		return fmt.Sprintf("%d %sies", n, strings.TrimSuffix(word, "y"))
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// ageText formats the time elapsed since t ("—" when unknown). Timestamps
// newer than now (touched after the scan started) read "now".
func ageText(t, now time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return fsx.Age(max(now.Sub(t), time.Second))
}

// thousands formats n with thin separators: 12,345.
func thousands(n int64) string {
	s := fmt.Sprint(n)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// clamp bounds v to [lo, hi] (hi wins when hi < lo).
func clamp(v, lo, hi int) int {
	if v > hi {
		v = hi
	}
	if v < lo {
		v = lo
	}
	return v
}

// firstLine returns the first line of s (provider errors may carry stacks).
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
