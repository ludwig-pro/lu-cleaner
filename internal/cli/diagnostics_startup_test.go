package cli

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/diagnostics"
)

func startupHarness(t *testing.T) (*harness, diagnostics.Store) {
	t.Helper()
	h := newHarness(t, &fakeProvider{id: "fake", cats: []core.Category{core.CatWorktrees, core.CatArtifacts, core.CatSimulators, core.CatAndroid}})
	h.app.StdinTTY, h.app.StdoutTTY, h.app.StderrTTY = true, true, true
	store := diagnostics.Store{Dir: filepath.Join(h.home, "diagnostics"), DSN: "https://public@invalid.example/42"}
	h.app.Diagnostics = func() diagnostics.Store { return store }
	return h, store
}

func TestFirstInteractiveUseAsksBeforeWorkAndRemembersChoice(t *testing.T) {
	for _, answer := range []string{"\n", "n\n", "Y\n", "oui\n"} {
		t.Run(strings.TrimSpace(answer), func(t *testing.T) {
			h, store := startupHarness(t)
			h.app.Stdin = strings.NewReader(answer + "next input\n")
			wantEnabled := answer == "Y\n" || answer == "oui\n"
			newEnv := h.app.NewEnv
			h.app.NewEnv = func() *core.Env {
				choice, err := store.Consent()
				if err != nil || choice.Decision == "" || store.Enabled() != wantEnabled {
					t.Fatalf("application started before decision: %+v %v", choice, err)
				}
				return newEnv()
			}
			if code := h.run("scan", "--summary", "--dry-run"); code != ExitOK {
				t.Fatalf("first scan: %d %s", code, h.errOut.String())
			}
			if !strings.Contains(h.errOut.String(), "Allow these optional reports?") || strings.Contains(h.out.String(), "Technical reports") {
				t.Fatal("consent must precede the scan and stay on stderr")
			}
			remaining, err := io.ReadAll(h.app.Stdin)
			if err != nil || string(remaining) != "next input\n" {
				t.Fatalf("consumed subsequent input: %q %v", remaining, err)
			}
			before, _ := store.Consent()
			h.app.Stdin = strings.NewReader("")
			if code := h.run("scan", "--summary"); code != ExitOK || strings.Contains(h.errOut.String(), "Allow these optional reports?") {
				t.Fatalf("asked again: %d %s", code, h.errOut.String())
			}
			after, _ := store.Consent()
			if after != before {
				t.Fatal("rewrote the saved decision")
			}
		})
	}
}

func TestFirstUseCoversApplicationEntrypoints(t *testing.T) {
	for _, args := range [][]string{nil, {"scan"}, {"clean"}, {"artifacts"}, {"worktrees"}, {"devices"}, {"analyze"}, {"doctor", "--no-scan"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			h, store := startupHarness(t)
			h.app.Stdin = strings.NewReader("\n")
			if code := h.run(args...); code != ExitOK {
				t.Fatalf("command: %d %s", code, h.errOut.String())
			}
			choice, err := store.Consent()
			if err != nil || choice.Decision != "declined" || strings.Count(h.errOut.String(), "Allow these optional reports?") != 1 {
				t.Fatalf("missing first-use question: %+v %v %s", choice, err, h.errOut.String())
			}
		})
	}
}

func TestStartupNeverPromptsInScriptsOrUtilityCommands(t *testing.T) {
	tests := []struct {
		name string
		args []string
		edit func(*harness)
	}{
		{"json", []string{"scan", "--json"}, nil},
		{"yes", []string{"clean", "--yes", "--smart", "--dry-run"}, nil},
		{"stdin", []string{"scan"}, func(h *harness) { h.app.StdinTTY = false }},
		{"stdout", []string{"scan"}, func(h *harness) { h.app.StdoutTTY = false }},
		{"stderr", []string{"scan"}, func(h *harness) { h.app.StderrTTY = false }},
		{"version", []string{"version"}, nil},
		{"version-flag", []string{"--version"}, nil},
		{"help", []string{"--help"}, nil},
		{"config", []string{"config", "show"}, nil},
		{"history", []string{"history"}, nil},
		{"status", []string{"diagnostics", "status"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, store := startupHarness(t)
			h.app.Stdin = strings.NewReader("Y\n")
			if tt.edit != nil {
				tt.edit(h)
			}
			if code := h.run(tt.args...); code != ExitOK {
				t.Fatalf("command: %d %s", code, h.errOut.String())
			}
			choice, err := store.Consent()
			if err != nil || choice.Decision != "" || store.Enabled() || strings.Contains(h.errOut.String(), "Allow these optional reports?") {
				t.Fatalf("unexpected question or consent: %+v %v", choice, err)
			}
			if tt.name == "json" && !json.Valid(h.out.Bytes()) {
				t.Fatalf("polluted JSON: %s", h.out.String())
			}
		})
	}
}

func TestStartupConsentUnavailableDoesNotBlockWork(t *testing.T) {
	for _, state := range []string{"no-recipient", "broken-state", "EOF"} {
		t.Run(state, func(t *testing.T) {
			h, store := startupHarness(t)
			h.app.Stdin = strings.NewReader("")
			switch state {
			case "no-recipient":
				store.DSN = ""
				h.app.Diagnostics = func() diagnostics.Store { return store }
			case "broken-state":
				if err := os.WriteFile(store.Dir, []byte("unsafe state"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if code := h.run("scan", "--summary"); code != ExitOK || store.Enabled() {
				t.Fatalf("optional consent blocked work: %d %s", code, h.errOut.String())
			}
			if state != "EOF" && strings.Contains(h.errOut.String(), "Allow these optional reports?") {
				t.Fatal("asked without an available recipient or state")
			}
			if state == "EOF" {
				choice, err := store.Consent()
				if err != nil || choice.Decision != "" {
					t.Fatal("no answer was recorded as a decision")
				}
			}
		})
	}
}

type consentBeforeRead struct {
	io.Reader
	before func()
}

func (r *consentBeforeRead) Read(p []byte) (int, error) {
	if r.before != nil {
		r.before()
		r.before = nil
	}
	return r.Reader.Read(p)
}

func TestStartupCannotSaveConsentContinuesWithoutReporting(t *testing.T) {
	h, store := startupHarness(t)
	h.app.Stdin = &consentBeforeRead{Reader: strings.NewReader("Y\n"), before: func() {
		// The state was available before asking, then became unwritable.
		if err := os.WriteFile(store.Dir, []byte("unsafe state"), 0o600); err != nil {
			t.Fatal(err)
		}
	}}
	c := newCLI(h.app)
	used, err := c.prepareDiagnosticsConsent(context.Background(), store)
	if err != nil || used.DSN != "" || !strings.Contains(h.errOut.String(), "could not save") {
		t.Fatalf("failed persistence enabled reporting or stopped work: %v %s", err, h.errOut.String())
	}
	// The following invocation also stays functional despite the broken state.
	if code := h.run("scan", "--summary"); code != ExitOK || store.Enabled() {
		t.Fatalf("broken state blocked the scan: %d %s", code, h.errOut.String())
	}
}

type consentPromptWriter struct {
	io.Writer
	ready chan struct{}
}

func (w consentPromptWriter) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	if strings.Contains(string(p), "[y/N]") {
		select {
		case <-w.ready:
		default:
			close(w.ready)
		}
	}
	return n, err
}

func TestStartupConsentCancellationJoinsReaderBeforeWork(t *testing.T) {
	h, store := startupHarness(t)
	input, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer writer.Close()
	h.app.Stdin = input
	ready := make(chan struct{})
	h.app.Stderr = consentPromptWriter{Writer: &h.errOut, ready: ready}
	started := false
	newEnv := h.app.NewEnv
	h.app.NewEnv = func() *core.Env { started = true; return newEnv() }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	go func() { done <- h.app.Run(ctx, []string{"scan"}) }()
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("prompt never appeared")
	}
	cancel()
	select {
	case code := <-done:
		if code != ExitInterrupted || started {
			t.Fatalf("cancellation: code=%d work=%t", code, started)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reader survived cancellation")
	}
	choice, err := store.Consent()
	if err != nil || choice.Decision != "" {
		t.Fatal("cancelled question saved a decision")
	}
}
