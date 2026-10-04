package cli

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/ludwig-pro/lu-cleaner/internal/diagnostics"
)

// Consent belongs on stderr, including when stdout is redirected or emits JSON.
func (c *cli) consentOutput() *output {
	a := *c.App
	a.Stdout, a.StdoutTTY = c.Stderr, c.StderrTTY
	o := newOutput(&a, c.noColor() || c.f.json)
	if o.width <= 0 {
		o.width = 80
	}
	o.width = min(o.width, 88)
	return o
}

func (c *cli) printConsentNotice(destination string) {
	o := c.consentOutput()
	indent := "  "
	if o.width < 40 {
		indent = ""
	}
	width := max(1, o.width-len(indent))
	line := func(text string) {
		for _, wrapped := range strings.Split(ansi.Wrap(text, width, ""), "\n") {
			o.println(indent + wrapped)
		}
	}
	sections := strings.Split(strings.TrimSpace(diagnostics.Notice), "\n\n")
	title, notice, _ := strings.Cut(sections[0], "\n")
	o.println()
	if o.tty && width >= 38 {
		brand := "lu-cleaner / PRIVACY"
		border := o.bad
		o.println(indent + o.paint(border, "╭─ ") + o.paint(border.Bold(true), brand) + o.paint(border, " "+strings.Repeat("─", width-ansi.StringWidth(brand)-5)+"╮"))
		for _, text := range []string{o.paint(o.bold, title), o.paint(o.dim, notice+" · Off by default")} {
			for _, wrapped := range strings.Split(ansi.Wrap(text, width-4, ""), "\n") {
				o.println(indent + o.paint(border, "│ ") + wrapped + strings.Repeat(" ", width-4-ansi.StringWidth(wrapped)) + o.paint(border, " │"))
			}
		}
		o.println(indent + o.paint(border, "╰"+strings.Repeat("─", width-2)+"╯"))
	} else {
		line(o.paint(o.bold, title))
		line(o.paint(o.dim, notice))
	}
	for _, section := range sections[1:] {
		heading, body, _ := strings.Cut(section, "\n")
		o.println()
		line(o.paint(o.bad.Bold(true), heading))
		line(strings.Join(strings.Fields(body), " "))
		if heading == "Recipient" {
			line(o.paint(o.dim, "Configured endpoint: "+sanitize(destination)))
		}
	}
	o.println()
}

func (c *cli) printConsentPrompt() {
	o := c.consentOutput()
	// Wrap the prompt too: small terminals must still display the default refusal.
	o.println(ansi.Wrap("  Enter = no. All features stay available.", o.width, ""))
	o.printf("%s ", ansi.Wrap("  "+o.paint(o.bold, "Allow these optional reports?")+" "+o.paint(o.bad.Bold(true), "[y/N]"), max(1, o.width-1), ""))
}

func (c *cli) printConsentResult(accepted bool) {
	printConsentResult(c.out, accepted)
}

func printConsentResult(o *output, accepted bool) {
	marker := ""
	indent := ""
	if o.tty {
		indent = "  "
		marker = "✓ "
	}
	width := o.width
	if width <= 0 {
		width = 80
	}
	line := func(text string) {
		for _, wrapped := range strings.Split(ansi.Wrap(text, max(1, width-len(indent)), ""), "\n") {
			o.println(indent + wrapped)
		}
	}
	o.println()
	if accepted {
		line(o.paint(o.good.Bold(true), marker+"Technical reports enabled."))
		line(o.paint(o.dim, "Revoke any time: lu-cleaner diagnostics disable"))
	} else {
		line(o.paint(o.bold, "Technical reports disabled."))
		line(o.paint(o.dim, "All features remain available."))
	}
	o.println()
}
