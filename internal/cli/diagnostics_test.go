package cli

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"github.com/charmbracelet/x/ansi"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/diagnostics"
)

func TestConsentDisclosureRemainsCompleteOnSmallTerminals(t *testing.T) {
	compact := func(text string) string {
		return strings.Map(func(r rune) rune {
			if unicode.IsSpace(r) {
				return -1
			}
			return r
		}, ansi.Strip(text))
	}
	for _, width := range []int{24, 40, 80, 120} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			h := newHarness(t)
			store := diagnostics.Store{Dir: filepath.Join(h.home, "diagnostics"), DSN: "https://public@invalid.example/42"}
			h.app.Diagnostics = func() diagnostics.Store { return store }
			h.app.StderrTTY, h.app.StdoutTTY = true, true
			h.app.Width = func() int { return width }
			if code := h.run("diagnostics", "enable", "--accept-notice", diagnostics.NoticeVersion, "--no-color"); code != ExitOK {
				t.Fatalf("enable: %d %s", code, h.errOut.String())
			}
			for _, stream := range []string{h.errOut.String(), h.out.String()} {
				if strings.ContainsRune(stream, '\x1b') {
					t.Fatal("--no-color emitted terminal escapes")
				}
				for _, line := range strings.Split(stream, "\n") {
					if got := ansi.StringWidth(line); got > width {
						t.Fatalf("line width %d exceeds %d: %q", got, width, line)
					}
				}
			}
			text := compact(h.errOut.String())
			for _, disclosure := range []string{
				"Notice " + diagnostics.NoticeVersion,
				"No usage analytics or permanent user/device ID",
				"contact@ludwigvantours.dev", "ludwig-developer", "European Union (Germany)",
				"relative filenames and line numbers", "personal paths", "panic values",
				"necessarily sees the IP address of the HTTPS connection",
				"not a promise of anonymity", "no offline upload backlog",
				"expires after 7 days", "30-day lookback", "metadata may remain longer",
				"custom recipient", "refuse without losing any feature", "diagnostics disable",
				"deletion request", "diagnostics export",
				"https://ludwig-pro.github.io/lu-cleaner/guides/diagnostics/",
				"Configured endpoint: invalid.example",
			} {
				if !strings.Contains(text, compact(disclosure)) {
					t.Fatalf("missing disclosure: %s", disclosure)
				}
			}
		})
	}
}

func TestDiagnosticsConsentCommands(t *testing.T) {
	h := newHarness(t)
	store := diagnostics.Store{Dir: filepath.Join(h.home, "diagnostics"), DSN: "https://public@invalid.example/42"}
	h.app.Diagnostics = func() diagnostics.Store { return store }
	if code := h.run("diagnostics", "status", "--json"); code != ExitOK || !json.Valid(h.out.Bytes()) || store.Enabled() {
		t.Fatalf("status: %d %s %s", code, h.out.String(), h.errOut.String())
	}
	if code := h.run("diagnostics", "enable", "--yes", "--json"); code != ExitUsage || store.Enabled() {
		t.Fatal("cleaning --yes gave consent")
	}
	if code := h.run("diagnostics", "enable", "--accept-notice", "old"); code != ExitUsage || store.Enabled() {
		t.Fatal("outdated notice gave consent")
	}
	if code := h.run("diagnostics", "enable", "--accept-notice", diagnostics.NoticeVersion, "--json"); code != ExitOK || !json.Valid(h.out.Bytes()) || !store.Enabled() {
		t.Fatalf("explicit consent failed: %d %s %s", code, h.out.String(), h.errOut.String())
	}
	if !strings.Contains(h.errOut.String(), "No usage analytics") {
		t.Fatal("notice not presented")
	}
	if code := h.run("diagnostics", "disable", "--json"); code != ExitOK || !json.Valid(h.out.Bytes()) || store.Enabled() {
		t.Fatal("revocation failed")
	}
}

func TestDiagnosticsInteractiveRefusalAndMissingRecipient(t *testing.T) {
	h := newHarness(t)
	store := diagnostics.Store{Dir: filepath.Join(h.home, "diagnostics")}
	h.app.Diagnostics = func() diagnostics.Store { return store }
	if code := h.run("diagnostics", "enable", "--accept-notice", diagnostics.NoticeVersion); code != ExitUsage || store.Enabled() {
		t.Fatal("enabled without recipient")
	}
	store.DSN = "https://public@invalid.example/42"
	h.app.StdinTTY = true
	h.app.Stdin = strings.NewReader("\n")
	if code := h.run("diagnostics", "enable"); code != ExitOK || store.Enabled() {
		t.Fatal("default answer was consent")
	}
	c, err := store.Consent()
	if err != nil || c.Decision != "declined" {
		t.Fatalf("refusal not saved: %+v %v", c, err)
	}
}

func TestMainPanicIsContainedAndExportedLocally(t *testing.T) {
	h := newHarness(t)
	store := diagnostics.Store{Dir: filepath.Join(h.home, "diagnostics")}
	h.app.Diagnostics = func() diagnostics.Store { return store }
	h.app.NewEnv = func() *core.Env { panic("/Users/private/token=secret") }
	if code := h.run("scan", "--json"); code != ExitFailure || strings.Contains(h.errOut.String(), "private") {
		t.Fatalf("unsafe main panic: %d %s", code, h.errOut.String())
	}
	if code := h.run("diagnostics", "export"); code != ExitOK || !json.Valid(h.out.Bytes()) || strings.Contains(h.out.String(), "private") {
		t.Fatalf("local report invalid: %d %s %s", code, h.out.String(), h.errOut.String())
	}
}
