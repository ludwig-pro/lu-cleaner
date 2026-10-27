package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/diagnostics"
	"github.com/ludwig-pro/lu-cleaner/internal/update"
)

func updateHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t, &fakeProvider{id: "fake", cats: []core.Category{core.CatWorktrees, core.CatArtifacts, core.CatSimulators, core.CatAndroid}})
	h.app.StdinTTY, h.app.StdoutTTY, h.app.StderrTTY = true, true, true
	h.app.Diagnostics = func() diagnostics.Store { return diagnostics.Store{Dir: filepath.Join(h.home, "diagnostics")} }
	return h
}

func TestStartupUpdateEligibility(t *testing.T) {
	for _, args := range [][]string{nil, {"clean"}, {"artifacts"}, {"worktrees"}, {"devices"}, {"analyze"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			h := updateHarness(t)
			calls := 0
			h.app.StartupUpdate = func(context.Context, update.Prompt) error { calls++; return nil }
			if code := h.run(args...); code != ExitOK || calls != 1 {
				t.Fatalf("eligible command code=%d calls=%d err=%s", code, calls, h.errOut.String())
			}
		})
	}
	for _, args := range [][]string{{"scan"}, {"doctor", "--no-scan"}, {"version"}, {"--version"}, {"--help"}, {"artifacts", "--help"}, {"config", "show"}, {"history"}, {"catalog"}, {"diagnostics", "status"}, {"completion", "zsh"}, {"artifacts", "--list"}, {"worktrees", "-l"}, {"devices", "--list"}, {"artifacts", "--dry-run"}, {"clean", "--yes", "--smart", "--dry-run"}, {"artifacts", "--json"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			h := updateHarness(t)
			calls := 0
			h.app.StartupUpdate = func(context.Context, update.Prompt) error { calls++; return nil }
			if code := h.run(args...); code != ExitOK || calls != 0 {
				t.Fatalf("ineligible command code=%d calls=%d err=%s", code, calls, h.errOut.String())
			}
		})
	}
	for _, name := range []string{"stdin", "stdout", "stderr", "CI", "LU_NO_UPDATE"} {
		t.Run(name, func(t *testing.T) {
			h := updateHarness(t)
			switch name {
			case "stdin":
				h.app.StdinTTY = false
			case "stdout":
				h.app.StdoutTTY = false
			case "stderr":
				h.app.StderrTTY = false
			default:
				h.app.Getenv = func(key string) string {
					if key == name {
						return "1"
					}
					return ""
				}
			}
			h.app.StartupUpdate = func(context.Context, update.Prompt) error { t.Fatal("updater ran in script"); return nil }
			if code := h.run("artifacts"); code != ExitOK {
				t.Fatalf("script command failed %s", h.errOut.String())
			}
		})
	}
}

func TestUpdateAnswerConsumesOnlyOneCompleteLine(t *testing.T) {
	for _, tc := range []struct {
		answer string
		yes    bool
	}{{"\n", false}, {"n\n", false}, {"invalid\n", false}, {"y\n", true}, {"YES\n", true}, {"oui\n", true}, {strings.Repeat("y", 65) + "\n", false}} {
		t.Run(tc.answer[:min(8, len(tc.answer))], func(t *testing.T) {
			h := updateHarness(t)
			h.app.Stdin = strings.NewReader(tc.answer + "next input\n")
			h.app.StartupUpdate = func(ctx context.Context, p update.Prompt) error {
				yes, err := p.Confirm(ctx, update.Offer{Current: "0.4.1", Next: "0.4.2", Target: "/tmp/lu-cleaner"})
				if err != nil || yes != tc.yes {
					t.Fatalf("answer %q accepted=%t err=%v", tc.answer, yes, err)
				}
				return nil
			}
			if code := h.run("artifacts"); code != ExitOK {
				t.Fatal(h.errOut.String())
			}
			rest, err := io.ReadAll(h.app.Stdin)
			if err != nil || string(rest) != "next input\n" {
				t.Fatalf("consumed TUI input %q %v", rest, err)
			}
			if strings.Contains(h.out.String(), "is available") {
				t.Fatal("update polluted stdout")
			}
		})
	}
	for _, answer := range []string{"", "y", "yes", "oui"} {
		t.Run("EOF-"+answer, func(t *testing.T) {
			h := updateHarness(t)
			h.app.Stdin = strings.NewReader(answer)
			h.app.StartupUpdate = func(ctx context.Context, p update.Prompt) error {
				yes, err := p.Confirm(ctx, update.Offer{})
				if yes || !errors.Is(err, io.EOF) {
					t.Fatalf("partial EOF authorized update %q %t %v", answer, yes, err)
				}
				return nil
			}
			if code := h.run("artifacts"); code != ExitOK {
				t.Fatal(h.errOut.String())
			}
		})
	}
}

func TestStartupUpdateFollowsDiagnosticsAndNeverBlocksCommandOnSoftError(t *testing.T) {
	h, store := startupHarness(t)
	h.app.Stdin = strings.NewReader("n\n")
	called := false
	h.app.StartupUpdate = func(context.Context, update.Prompt) error {
		choice, err := store.Consent()
		if err != nil || choice.Decision != "declined" {
			t.Fatalf("updater preceded diagnostic consent %+v %v", choice, err)
		}
		called = true
		return errors.New("offline")
	}
	if code := h.run("artifacts"); code != ExitOK || !called || len(h.pickers) != 1 {
		t.Fatalf("optional failure stopped application code=%d called=%t pickers=%d %s", code, called, len(h.pickers), h.errOut.String())
	}
}

func TestUpdateCancellationJoinsInputBeforeStartingCommand(t *testing.T) {
	h := updateHarness(t)
	input, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer writer.Close()
	h.app.Stdin = input
	ready := make(chan struct{})
	h.app.Stderr = consentPromptWriter{Writer: &h.errOut, ready: ready}
	h.app.StartupUpdate = func(ctx context.Context, p update.Prompt) error { _, err := p.Confirm(ctx, update.Offer{}); return err }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	go func() { done <- h.app.Run(ctx, []string{"artifacts"}) }()
	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("prompt missing")
	}
	cancel()
	select {
	case code := <-done:
		if code != ExitInterrupted || len(h.pickers) != 0 {
			t.Fatalf("canceled update started UI code=%d", code)
		}
	case <-time.After(time.Second):
		t.Fatal("input reader survived cancellation")
	}
}

func TestGoInstallOfferExplainsReplacement(t *testing.T) {
	h := updateHarness(t)
	h.app.Stdin = strings.NewReader("n\n")
	h.app.StartupUpdate = func(ctx context.Context, p update.Prompt) error {
		_, err := p.Confirm(ctx, update.Offer{GoInstall: true})
		return err
	}
	if code := h.run("artifacts"); code != ExitOK || !strings.Contains(h.errOut.String(), "replaces your go install binary") {
		t.Fatalf("installation method not explained %s", h.errOut.String())
	}
}

func TestExpiredUpdateContextDoesNotStartCommand(t *testing.T) {
	h := updateHarness(t)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(10*time.Millisecond))
	defer cancel()
	h.app.StartupUpdate = func(ctx context.Context, _ update.Prompt) error { <-ctx.Done(); return ctx.Err() }
	if code := h.app.Run(ctx, []string{"artifacts"}); code == ExitOK || len(h.pickers) != 0 {
		t.Fatalf("expired startup began UI code=%d", code)
	}
}
