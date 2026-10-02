package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/clean"
	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/scanctl"
	"github.com/ludwig-pro/lu-cleaner/internal/tui"
)

type scanProbeProvider struct {
	fakeProvider
	scan func(context.Context, *core.Env, core.Emit) error
}

func (p *scanProbeProvider) Scan(ctx context.Context, env *core.Env, emit core.Emit) error {
	return p.scan(ctx, env, emit)
}

func TestScanModePrecedenceAndContext(t *testing.T) {
	for _, tc := range []struct {
		name, cfg, flag, walkers string
		wantMode                 scanctl.Mode
		wantIO                   int
	}{
		{name: "default", wantMode: scanctl.Eco, wantIO: 2},
		{name: "config", cfg: "fast", wantMode: scanctl.Fast, wantIO: 8},
		{name: "flag", cfg: "fast", flag: "eco", wantMode: scanctl.Eco, wantIO: 2},
		{name: "flag overrides invalid config", cfg: "typo", flag: "fast", wantMode: scanctl.Fast, wantIO: 8},
		{name: "walkers overrides eco IO", walkers: "3", wantMode: scanctl.Eco, wantIO: 3},
		{name: "invalid walkers falls back", cfg: "fast", walkers: "0", wantMode: scanctl.Fast, wantIO: 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got scanctl.Limits
			var shared bool
			p := &scanProbeProvider{fakeProvider: fakeProvider{id: "probe", cats: []core.Category{core.CatArtifacts}}}
			p.scan = func(ctx context.Context, env *core.Env, emit core.Emit) error {
				ctrl := scanctl.From(ctx)
				if ctrl != nil {
					got = ctrl.Limits()
					shared = reflect.DeepEqual(got, env.ScanLimits)
				}
				return nil
			}
			h := newHarness(t, p)
			h.cfg.ScanMode = tc.cfg
			h.app.Getenv = func(key string) string {
				if key == "LU_WALKERS" {
					return tc.walkers
				}
				return ""
			}
			args := []string{"scan", "--json"}
			if tc.flag != "" {
				args = append(args, "--scan-mode", tc.flag)
			}
			if code := h.run(args...); code != ExitOK {
				t.Fatalf("exit %d: %s", code, h.errOut.String())
			}
			if !shared || got.Mode != tc.wantMode || got.IO != tc.wantIO {
				t.Fatalf("shared=%v limits=%+v", shared, got)
			}
			var report map[string]any
			if err := json.Unmarshal(h.out.Bytes(), &report); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"scan", "scan_mode", "scan_limits"} {
				if _, added := report[key]; added {
					t.Fatalf("scan JSON gained %q", key)
				}
			}
		})
	}
}

func TestInvalidScanModeFailsBeforeScan(t *testing.T) {
	for _, tc := range []struct {
		name, cfg string
		args      []string
		code      int
	}{
		{name: "flag", args: []string{"scan", "--scan-mode", "typo"}, code: ExitUsage},
		{name: "empty flag", args: []string{"scan", "--scan-mode="}, code: ExitUsage},
		{name: "config", cfg: "typo", args: []string{"scan"}, code: ExitFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.cfg.ScanMode = tc.cfg
			calls := 0
			h.app.NewEnv = func() *core.Env { calls++; return nil }
			h.app.ActivateScan = func(scanctl.Limits) (func() error, error) { calls++; return nil, nil }
			if code := h.run(tc.args...); code != tc.code || calls != 0 {
				t.Fatalf("exit=%d calls=%d stderr=%s", code, calls, h.errOut.String())
			}
		})
	}
}

func TestScanPrioritiesOnlyOnScanPaths(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     []string
		tty      bool
		wantScan bool
	}{
		{name: "config show", args: []string{"config", "show", "--json"}},
		{name: "config path", args: []string{"config", "path"}},
		{name: "history", args: []string{"history", "--json"}},
		{name: "catalog", args: []string{"catalog", "--json"}},
		{name: "version", args: []string{"version"}},
		{name: "help", args: []string{"--help"}},
		{name: "scan", args: []string{"scan", "--json"}, wantScan: true},
		{name: "scan dry run", args: []string{"scan", "--dry-run", "--json"}, wantScan: true},
		{name: "dashboard", tty: true, wantScan: true},
		{name: "clean preparatory dry run", args: []string{"clean", "--yes", "--smart", "--dry-run", "--json"}, wantScan: true},
		{name: "worktrees", args: []string{"worktrees", "--json"}, wantScan: true},
		{name: "devices", args: []string{"devices", "--json"}, wantScan: true},
		{name: "doctor", args: []string{"doctor", "--json"}, wantScan: true},
		{name: "doctor without providers", args: []string{"doctor", "--no-scan", "--json"}, wantScan: true},
		{name: "analyze report", args: []string{"analyze", "--json"}, wantScan: true},
		{name: "analyze TUI", args: []string{"analyze"}, tty: true, wantScan: true},
		{name: "picker", args: []string{"artifacts"}, tty: true, wantScan: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, &fakeProvider{id: "probe", cats: []core.Category{core.CatArtifacts}})
			if tc.tty {
				h.tty()
			}
			activated, restored := 0, 0
			before := runtime.GOMAXPROCS(0)
			h.app.ActivateScan = func(scanctl.Limits) (func() error, error) {
				activated++
				return func() error { restored++; return nil }, nil
			}
			if code := h.run(tc.args...); code != ExitOK {
				t.Fatalf("exit=%d: %s", code, h.errOut.String())
			}
			want := 0
			if tc.wantScan {
				want = 1
			}
			if activated != want || restored != want || runtime.GOMAXPROCS(0) != before {
				t.Fatalf("activated=%d restored=%d runtime before=%d after=%d", activated, restored, before, runtime.GOMAXPROCS(0))
			}
		})
	}
}

func TestScanPrioritiesRestoredOnFailureAndCancellation(t *testing.T) {
	for _, exitErr := range []error{errors.New("TUI failed"), context.Canceled} {
		t.Run(exitErr.Error(), func(t *testing.T) {
			h := newHarness(t, &fakeProvider{id: "probe", cats: []core.Category{core.CatArtifacts}}).tty()
			restored := 0
			h.app.ActivateScan = func(scanctl.Limits) (func() error, error) {
				return func() error { restored++; return nil }, nil
			}
			h.app.Picker = func(ctx context.Context, opt tui.PickerOptions) (*clean.Summary, error) {
				if scanctl.From(ctx) == nil || opt.Env.ScanLimits.Mode != scanctl.Eco {
					t.Error("picker did not receive the resolved controller")
				}
				return nil, exitErr
			}
			wantCode := ExitFailure
			if errors.Is(exitErr, context.Canceled) {
				wantCode = ExitInterrupted
			}
			if code := h.run("artifacts"); code != wantCode || restored != 1 {
				t.Fatalf("exit=%d restored=%d: %s", code, restored, h.errOut.String())
			}
		})
	}
}

func TestScanPriorityErrorWarnsAndContinues(t *testing.T) {
	h := newHarness(t, &fakeProvider{id: "probe", cats: []core.Category{core.CatArtifacts}})
	restored := 0
	h.app.ActivateScan = func(scanctl.Limits) (func() error, error) {
		return func() error { restored++; return nil }, errors.New("background priority unavailable")
	}
	if code := h.run("scan", "--json"); code != ExitOK || restored != 1 {
		t.Fatalf("exit=%d restored=%d: %s", code, restored, h.errOut.String())
	}
	if n := strings.Count(h.errOut.String(), "background priority unavailable"); n != 1 {
		t.Fatalf("priority warning count=%d: %s", n, h.errOut.String())
	}
}

func TestVerboseScanResourceCountersNameCumulativeWaits(t *testing.T) {
	h := newHarness(t, &fakeProvider{id: "probe", cats: []core.Category{core.CatArtifacts}})
	if code := h.run("scan", "--json", "--verbose"); code != ExitOK {
		t.Fatalf("exit=%d: %s", code, h.errOut.String())
	}
	for _, want := range []string{
		"scan limits: mode=eco", "scan resources: io_max=0/2 commands_max=0/1",
		"io_wait_cumulative=", "pause_cumulative=", "command_wait_cumulative=",
		"files=", "dirs=", "cache_hits=", "cache_misses=", "cancel_join=",
	} {
		if !strings.Contains(h.errOut.String(), want) {
			t.Errorf("verbose stderr lacks %q: %s", want, h.errOut.String())
		}
	}
}

func TestConfigShowScanLimitsAndWalkersWarning(t *testing.T) {
	h := newHarness(t)
	h.cfg.ScanMode = "fast"
	walkers := "3"
	h.app.Getenv = func(key string) string {
		if key == "LU_WALKERS" {
			return walkers
		}
		return ""
	}
	if code := h.run("config", "show", "--json", "--scan-mode", "eco"); code != ExitOK {
		t.Fatalf("exit=%d: %s", code, h.errOut.String())
	}
	var v configView
	if err := json.Unmarshal(h.out.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Scan.Mode != "eco" || v.Scan.IO != 3 || v.Scan.Commands != 1 || v.Scan.Prefetch != 1 || v.Scan.BatchSize != 256 || v.Scan.PauseMS != 0 || v.Scan.GOMAXPROCS != min(runtime.GOMAXPROCS(0), 2) || v.Scan.Priority != "background" {
		t.Fatalf("effective scan=%+v", v.Scan)
	}
	if v.Config["scan_mode"] != "fast" {
		t.Fatalf("file config was replaced by flag: %v", v.Config)
	}
	walkers = "bad"
	if code := h.run("config", "show", "--json"); code != ExitOK || strings.Contains(h.errOut.String(), "LU_WALKERS") {
		t.Fatalf("invalid override should silently fall back without verbose: exit=%d stderr=%s", code, h.errOut.String())
	}
	if code := h.run("config", "show", "--json", "--verbose"); code != ExitOK || !strings.Contains(h.errOut.String(), "LU_WALKERS") {
		t.Fatalf("verbose lacks invalid override: exit=%d stderr=%s", code, h.errOut.String())
	}
}

func TestConfigShowReportsPureRuntimePolicy(t *testing.T) {
	for _, mode := range []string{"eco", "fast"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t)
			h.cfg.ScanMode = mode
			before, activated := runtime.GOMAXPROCS(0), 0
			h.app.ActivateScan = func(scanctl.Limits) (func() error, error) { activated++; return nil, nil }
			if code := h.run("config", "show", "--json"); code != ExitOK {
				t.Fatalf("exit=%d: %s", code, h.errOut.String())
			}
			var v configView
			if err := json.Unmarshal(h.out.Bytes(), &v); err != nil {
				t.Fatal(err)
			}
			wantCPUs, wantPriority := before, "inherited"
			if mode == "eco" {
				wantCPUs, wantPriority = min(before, 2), "background"
			}
			if v.Scan.GOMAXPROCS != wantCPUs || v.Scan.Priority != wantPriority {
				t.Fatalf("scan=%+v want CPUs=%d priority=%s", v.Scan, wantCPUs, wantPriority)
			}
			if code := h.run("config", "show"); code != ExitOK || !strings.Contains(h.out.String(), fmt.Sprintf("GOMAXPROCS %d · priority %s", wantCPUs, wantPriority)) {
				t.Fatalf("human config lacks runtime policy: %s", h.out.String())
			}
			if activated != 0 || runtime.GOMAXPROCS(0) != before {
				t.Fatal("config show changed process policy")
			}
		})
	}
}

func TestScanSetupControlsInitialRootReadsAndPureSetupDoesNotActivate(t *testing.T) {
	for _, scanning := range []bool{false, true} {
		name := "pure config setup"
		if scanning {
			name = "scan setup"
		}
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			root := filepath.Join(h.home, "Projects", "Fixture")
			if err := os.MkdirAll(root, 0o755); err != nil {
				t.Fatal(err)
			}
			h.cfg.Roots = []string{"~/Projects/Fixture"}
			c := newCLI(h.app)
			activated, restored := 0, 0
			before := runtime.GOMAXPROCS(0)
			h.app.ActivateScan = func(limits scanctl.Limits) (func() error, error) {
				activated++
				if limits.Mode != scanctl.Eco {
					t.Errorf("initial scan limits=%+v", limits)
				}
				return func() error { restored++; return nil }, nil
			}
			newEnv := h.app.NewEnv
			h.app.NewEnv = func() *core.Env {
				// Environment preparation precedes every root inventory. The
				// scan controller and activation must already exist at this point.
				if scanning && (activated != 1 || c.scanCtl == nil) {
					t.Error("root preparation began before scan activation")
				}
				if !scanning && (activated != 0 || c.scanCtl != nil) {
					t.Error("pure setup activated a scan policy")
				}
				return newEnv()
			}
			t.Cleanup(func() { _ = c.finishScan() })
			var s *setup
			var err error
			if scanning {
				s, err = c.newScanSetup(nil, context.Background())
			} else {
				s, err = c.newSetup(nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(s.env.Roots) != 1 || s.env.Roots[0] != root {
				t.Fatalf("resolved roots=%v want %s", s.env.Roots, root)
			}
			if scanning {
				initial := c.scanCtl
				stats := initial.Snapshot()
				if stats.IOMax != 1 || stats.ThrottleWait != 0 {
					t.Fatalf("initial root reads must use eco admission without artificial pauses: %+v", stats)
				}
				ctx, err := c.startScan(context.Background(), s)
				if err != nil || scanctl.From(ctx) != initial || activated != 1 || s.env.ProtectedContext == nil {
					t.Fatalf("completed setup lost its controller or guard: activated=%d err=%v", activated, err)
				}
			} else if activated != 0 || c.scanCtl != nil || restored != 0 {
				t.Fatal("pure setup changed scan policy")
			}
			if runtime.GOMAXPROCS(0) != before {
				t.Fatal("injected activation changed runtime policy")
			}
		})
	}
}

func TestProtectedGlobUsesCanceledScanChildContext(t *testing.T) {
	p := &scanProbeProvider{fakeProvider: fakeProvider{id: "probe", cats: []core.Category{core.CatArtifacts}}}
	h := newHarness(t, p)
	h.cfg.ScanMode = "fast"
	h.app.Getenv = func(key string) string {
		if key == "LU_WALKERS" {
			return "1"
		}
		return ""
	}
	p.scan = func(ctx context.Context, env *core.Env, emit core.Emit) error {
		if env.ProtectedContext == nil {
			t.Error("scan environment has no context-aware protection")
			return nil
		}
		release, err := scanctl.From(ctx).AcquireIO(ctx)
		if err != nil {
			return err
		}
		defer release()
		child, cancel := context.WithCancel(ctx)
		defer cancel()
		protected := make(chan bool, 1)
		// The built-in pattern .claude/projects/*/memory requires an inventory.
		go func() { protected <- env.IsProtectedContext(child, filepath.Join(h.home, ".claude", "projects")) }()
		select {
		case <-protected:
			t.Error("glob inspection bypassed occupied I/O admission")
			return nil
		case <-time.After(20 * time.Millisecond):
		}
		cancel()
		select {
		case ok := <-protected:
			if !ok {
				t.Error("interrupted protection must fail closed")
			}
		case <-time.After(time.Second):
			t.Error("child protection kept waiting on the live owner context")
		}
		if ctx.Err() != nil {
			t.Error("child cancellation canceled the scan owner")
		}
		return nil
	}
	if code := h.run("scan", "--json"); code != ExitOK {
		t.Fatalf("exit=%d: %s", code, h.errOut.String())
	}
}
