package tui

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
)

func waitOutput(t *testing.T, tm *teatest.TestModel, s string) {
	t.Helper()
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool { return bytes.Contains(b, []byte(s)) },
		teatest.WithDuration(5*time.Second), teatest.WithCheckInterval(20*time.Millisecond))
}

// TestPickerTeatestSmoke runs the picker inside a real Bubble Tea program.
func TestPickerTeatestSmoke(t *testing.T) {
	m := newTestPicker(t, PickerOptions{Providers: standardProviders(), Smart: true, Title: "smoke"})
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(100, 30))
	waitOutput(t, tm, "scan done")
	tm.Send(keyMsg("enter"))
	waitOutput(t, tm, "Project artifacts")
	tm.Send(keyMsg("?"))
	waitOutput(t, tm, "Navigation")
	tm.Send(keyMsg("x"))
	tm.Send(keyMsg("q"))
	fm := tm.FinalModel(t, teatest.WithFinalTimeout(5*time.Second))
	pm := fm.(*pickerModel)
	if !pm.quitting || pm.lastSummary != nil {
		t.Fatalf("quitting=%v summary=%v", pm.quitting, pm.lastSummary)
	}
	sel := ids(pm.selItems)
	sort.Strings(sel)
	if got := strings.Join(sel, ","); got != "aicache,build,nm" {
		t.Fatalf("smart selection = %q", got)
	}
}

func TestAnalyzerTeatestSmoke(t *testing.T) {
	f := newAnFixture(t)
	m := newTestAnalyzer(t, f.root, f.home)
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(100, 30))
	waitOutput(t, tm, "node_modules/")
	tm.Send(keyMsg("q"))
	fm := tm.FinalModel(t, teatest.WithFinalTimeout(5*time.Second))
	if !fm.(*analyzeModel).quitting {
		t.Fatal("not quitting")
	}
}

// TestManualHarness is a manual QA entry point, skipped by default. Build the
// test binary and drive it from a real terminal (or expect):
//
//	go test -c -o /tmp/tui.test ./internal/tui
//	LU_TUI_HARNESS=picker  /tmp/tui.test -test.run TestManualHarness
//	LU_TUI_HARNESS=analyze /tmp/tui.test -test.run TestManualHarness
//
// Everything happens in a temporary directory; the picker is a real (non
// dry-run) clean restricted to that directory by the safety guard.
func TestManualHarness(t *testing.T) {
	which := os.Getenv("LU_TUI_HARNESS")
	if which == "" {
		t.Skip("set LU_TUI_HARNESS=picker|analyze")
	}
	home := tempHome(t)
	env := testEnv(home)
	env.Now = time.Now()
	switch which {
	case "analyze":
		root := filepath.Join(home, "dev")
		for i := 0; i < 30; i++ {
			mkdirFiles(t, filepath.Join(root, fmt.Sprintf("app-%02d", i)), map[string]int{
				"package.json": 10, "node_modules/x/index.js": 1000 * (i + 1), "ios/Pods/p.m": 500 * i, "android/build/out.apk": 3000,
			})
		}
		err := RunAnalyze(context.Background(), AnalyzeOptions{Env: env, Root: root, Clean: testCleanOpts(home)})
		fmt.Println("analyze exited, err =", err)
	default:
		var items []*core.Item
		for i := 0; i < 400; i++ {
			dir := mkdirFiles(t, filepath.Join(home, "dev", fmt.Sprintf("app-%03d", i)), map[string]int{"package.json": 10})
			nm := mkdirFiles(t, filepath.Join(dir, "node_modules"), map[string]int{"a.js": 1000 * (i%17 + 1)})
			it := &core.Item{
				ID: "nm:" + nm, Category: core.CatArtifacts, Kind: "node_modules", Name: fmt.Sprintf("app-%03d/node_modules", i), Path: nm,
				Size: int64(i%17+1) * 37e6, Risk: core.Risk(i % 3), Method: core.MethodDelete, Selectable: true,
				LastUsed: env.Now.Add(-time.Duration(i%90) * day), Project: dir, RequireSibling: []string{"package.json"},
				Note: "reinstall with your package manager",
			}
			if i%7 == 0 {
				it.Warn = "the project has uncommitted changes"
				it.Meta = map[string]string{"branch": "feat/x", "dirty": "true"}
			}
			items = append(items, it)
		}
		cmd := &core.Item{ID: "simctl", Category: core.CatSimulators, Kind: "sim-unavailable", Name: "Unavailable simulators",
			Location: "~/Library/Developer/CoreSimulator/Devices", Size: 3e9, Risk: core.RiskSafe, Method: core.MethodCommand,
			Command: []string{"true"}, Selectable: true, ProcessGuard: []string{"Simulator"}}
		snap := &core.Item{ID: "snap", Category: core.CatSystem, Kind: "snapshots", Name: "APFS local snapshots", Location: "/",
			Size: 12e9, Risk: core.RiskNever, Method: core.MethodReport, Note: "tmutil deletelocalsnapshots"}
		gate := make(chan struct{})
		time.AfterFunc(1500*time.Millisecond, func() { close(gate) })
		provs := []core.Provider{
			&fakeProvider{id: "artifacts", cats: []core.Category{core.CatArtifacts}, items: items},
			&fakeProvider{id: "apple", cats: []core.Category{core.CatSimulators}, items: []*core.Item{cmd}, gate: gate},
			&fakeProvider{id: "system", cats: []core.Category{core.CatSystem}, items: []*core.Item{snap}},
		}
		sum, err := RunPicker(context.Background(), PickerOptions{
			Env: env, Providers: provs, Filter: core.Filter{MaxRisk: core.RiskNever}, StaleAfter: 14 * day,
			Smart: true, Title: "lu-cleaner (harness)", Clean: testCleanOpts(home),
		})
		if sum != nil {
			var b strings.Builder
			for _, r := range sum.Results {
				fmt.Fprintf(&b, "%s ", r.Status)
			}
			fmt.Printf("picker exited: %d results (%s) estimated=%d err=%v\n", len(sum.Results), b.String(), sum.Estimated, err)
		} else {
			fmt.Println("picker exited without cleaning, err =", err)
		}
	}
}

var _ tea.Model = (*pickerModel)(nil)
var _ tea.Model = (*analyzeModel)(nil)
