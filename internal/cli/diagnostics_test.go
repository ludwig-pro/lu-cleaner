package cli

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/diagnostics"
)

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
