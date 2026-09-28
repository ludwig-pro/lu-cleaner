package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/clean"
	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/providers/catalog"
	"github.com/ludwig-pro/lu-cleaner/internal/safety"
	"github.com/ludwig-pro/lu-cleaner/internal/tui"
)

// fixture installs three fake providers:
//
//	artifacts: safe 500MB (recommended), stale 2GB node_modules (recommended),
//	           fresh 1GB node_modules, caution 3GB pods, tiny 10kB dist
//	worktrees: one dirty codex worktree (caution)
//	ai:        one safe 800MB cache (recommended)
func fixture(t *testing.T) *harness {
	h := newHarness(t)
	h.provs = []core.Provider{
		&fakeProvider{id: "artifacts", cats: []core.Category{core.CatArtifacts}, items: []*core.Item{
			h.mkItem("app-safe", core.CatArtifacts, "metro-cache", 500*mb, core.RiskSafe, 2*day),
			h.mkItem("app-stale", core.CatArtifacts, "node_modules", 2*gb, core.RiskModerate, 60*day),
			h.mkItem("app-fresh", core.CatArtifacts, "node_modules", 1*gb, core.RiskModerate, 2*day),
			h.mkItem("app-caution", core.CatArtifacts, "pods", 3*gb, core.RiskCaution, 90*day),
			h.mkItem("tiny", core.CatArtifacts, "dist", 10_000, core.RiskSafe, 90*day),
		}},
		&fakeProvider{id: "worktrees", cats: []core.Category{core.CatWorktrees}, items: []*core.Item{
			func() *core.Item {
				it := h.mkItem("feature-x", core.CatWorktrees, "codex-worktree", 4*gb, core.RiskCaution, 20*day)
				it.Method = core.MethodWorktree
				it.Meta = map[string]string{"tool": "codex", "branch": "feature/x", "status": "dirty"}
				it.Warn = "3 uncommitted changes"
				return it
			}(),
		}},
		&fakeProvider{id: "ai", cats: []core.Category{core.CatAI}, items: []*core.Item{
			h.mkItem("claude-cache", core.CatAI, "claude-cache", 800*mb, core.RiskSafe, 0),
		}},
	}
	return h
}

func sorted(s []string) []string {
	s = append([]string(nil), s...)
	slices.Sort(s)
	return s
}

func expectNames(t *testing.T, got []string, want ...string) {
	t.Helper()
	if !reflect.DeepEqual(sorted(got), sorted(want)) {
		t.Fatalf("items = %v, want %v", sorted(got), sorted(want))
	}
}

// ------------------------------------------------------------ clean --yes

func TestCleanYesRefusesWithoutNarrowingFilter(t *testing.T) {
	h := fixture(t)
	for _, args := range [][]string{
		{"clean", "--yes"},
		{"clean", "-y", "--older-than", "30d", "--min-size", "1GB"},
		{"clean", "-y", "--smart", "--no-smart"},
		{"clean", "-y", "-c", ""},
	} {
		if code := h.run(args...); code != ExitUsage {
			t.Errorf("%v: exit %d, want %d", args, code, ExitUsage)
		}
		if !strings.Contains(h.errOut.String(), "narrowing filter") {
			t.Errorf("%v: stderr %q does not explain the refusal", args, h.errOut.String())
		}
	}
	if len(h.cleaned) != 0 {
		t.Fatalf("clean must not run, got %d calls", len(h.cleaned))
	}
}

func TestCleanYesExcludesCautionUnlessExplicit(t *testing.T) {
	h := fixture(t)
	if code := h.run("clean", "--yes", "-c", "artifacts"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut.String())
	}
	// min-size defaults to 0 for clean --yes: "tiny" is included.
	expectNames(t, h.lastClean(), "app-safe", "app-stale", "app-fresh", "tiny")

	if code := h.run("clean", "--yes", "-c", "artifacts", "--risk", "caution"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut.String())
	}
	expectNames(t, h.lastClean(), "app-safe", "app-stale", "app-fresh", "app-caution", "tiny")

	if code := h.run("clean", "--yes", "-c", "artifacts", "--risk", "safe", "--min-size", "1MB"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	expectNames(t, h.lastClean(), "app-safe")
}

func TestCleanYesSmartKeepsRecommendedOnly(t *testing.T) {
	h := fixture(t)
	if code := h.run("clean", "--yes", "--smart"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut.String())
	}
	// fresh node_modules (not stale), caution pods, dirty worktree and tiny (< 1 MiB) are left alone.
	expectNames(t, h.lastClean(), "app-safe", "app-stale", "claude-cache")
	out := h.out.String()
	for _, want := range []string{"Plan:", "3 items", "cleaned", "Disk free:"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestCleanYesKindAndOlderThan(t *testing.T) {
	h := fixture(t)
	if code := h.run("clean", "-y", "-k", "node_modules"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	expectNames(t, h.lastClean(), "app-stale", "app-fresh")
	if code := h.run("clean", "-y", "-k", "node_modules", "--older-than", "30d"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	expectNames(t, h.lastClean(), "app-stale")
}

func TestCleanYesOptions(t *testing.T) {
	h := fixture(t)
	if code := h.run("clean", "-y", "--smart", "-n", "--force"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	opt := h.cleanOpt[len(h.cleanOpt)-1]
	if !opt.DryRun || !opt.Force || opt.Trash || opt.Guard == nil || opt.Home != h.home {
		t.Fatalf("unexpected options %+v", opt)
	}
	if !strings.Contains(h.out.String(), "Dry run") {
		t.Errorf("dry-run summary missing:\n%s", h.out.String())
	}

	h.cfg.UseTrash = true
	if code := h.run("clean", "-y", "--smart"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if opt := h.cleanOpt[len(h.cleanOpt)-1]; !opt.Trash || opt.DryRun {
		t.Fatalf("use_trash not honoured: %+v", opt)
	}
	if !strings.Contains(h.out.String(), "Trash is emptied") {
		t.Errorf("trash warning missing:\n%s", h.out.String())
	}
}

func TestCleanYesFailureExitCode(t *testing.T) {
	h := fixture(t)
	h.failNames["app-stale"] = true
	if code := h.run("clean", "-y", "--smart"); code != ExitFailure {
		t.Fatalf("exit %d, want %d", code, ExitFailure)
	}
	out := h.out.String()
	if !strings.Contains(out, "Failed: 1") || !strings.Contains(out, "app-stale: permission denied") {
		t.Fatalf("failure not reported:\n%s", out)
	}
}

func TestCleanYesNothingToClean(t *testing.T) {
	h := fixture(t)
	if code := h.run("clean", "-y", "-k", "does-not-exist"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if len(h.cleaned) != 0 || !strings.Contains(h.out.String(), "Nothing to clean") {
		t.Fatalf("unexpected: %d calls, out %q", len(h.cleaned), h.out.String())
	}
}

func TestCleanYesJSON(t *testing.T) {
	h := fixture(t)
	if code := h.run("clean", "-y", "--smart", "--json", "-n"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	// Risk and Method only marshal as text: decode generically.
	var sum struct {
		DryRun  bool             `json:"dry_run"`
		Results []map[string]any `json:"results"`
	}
	if err := json.Unmarshal(h.out.Bytes(), &sum); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, h.out.String())
	}
	if !sum.DryRun || len(sum.Results) != 3 || sum.Results[0]["status"] != "dry-run" {
		t.Fatalf("unexpected summary %+v", sum)
	}
}

func TestCleanYesDisabledCategories(t *testing.T) {
	h := fixture(t)
	h.cfg.DisabledCategories = []string{"ai"}
	if code := h.run("clean", "-y", "--smart"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	expectNames(t, h.lastClean(), "app-safe", "app-stale")

	if code := h.run("clean", "-y", "-c", "ai"); code != ExitUsage {
		t.Fatalf("exit %d, want usage error", code)
	}
	if !strings.Contains(h.errOut.String(), "disabled") {
		t.Fatalf("stderr: %s", h.errOut.String())
	}
}

func TestPlanCleanSkipsNestedAndUnselectable(t *testing.T) {
	h := newHarness(t)
	outer := h.mkItem("outer", core.CatArtifacts, "node_modules", 2*gb, core.RiskModerate, 30*day)
	inner := h.mkItem("inner", core.CatArtifacts, "cache", 1*gb, core.RiskSafe, 30*day)
	inner.Path = filepath.Join(outer.Path, ".cache")
	report := h.mkItem("report", core.CatSystem, "downloads", 5*gb, core.RiskSafe, 30*day)
	report.Method = core.MethodReport
	sizing := h.mkItem("sizing", core.CatArtifacts, "dist", 1*gb, core.RiskSafe, 30*day)
	sizing.Sizing = true
	locked := h.mkItem("locked", core.CatArtifacts, "dist", 1*gb, core.RiskSafe, 30*day)
	locked.Selectable = false

	f := core.Filter{MaxRisk: core.RiskModerate}
	plan, _ := planClean([]*core.Item{inner, outer, report, sizing, locked}, f, false, false, testNow, 14*day)
	if len(plan) != 1 || plan[0] != outer {
		t.Fatalf("plan = %v", plan)
	}
}

// ------------------------------------------------------------ pickers

func TestCleanInteractiveOpensPicker(t *testing.T) {
	h := fixture(t).tty()
	if code := h.run("clean"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut.String())
	}
	opt := h.pickers[0]
	if !opt.Smart || opt.Title != "lu-cleaner" || opt.Flat || len(opt.Providers) != 3 {
		t.Fatalf("unexpected picker options %+v", opt)
	}
	if opt.Filter.MaxRisk != core.RiskNever || opt.Filter.MinSize != 1e6 || opt.StaleAfter != 14*day {
		t.Fatalf("unexpected filter %+v stale %v", opt.Filter, opt.StaleAfter)
	}
	if opt.Clean.Guard == nil || opt.Env == nil || opt.Env.Protected == nil {
		t.Fatal("picker needs a guard and an env")
	}

	if code := h.run("clean", "--no-smart", "-c", "artifacts", "--risk", "moderate", "-n"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	opt = h.pickers[1]
	if opt.Smart || !opt.Clean.DryRun || opt.Filter.MaxRisk != core.RiskModerate ||
		!reflect.DeepEqual(opt.Filter.Categories, []core.Category{core.CatArtifacts}) ||
		len(opt.Providers) != 1 || opt.Providers[0].ID() != "artifacts" {
		t.Fatalf("unexpected picker options %+v", opt)
	}
}

func TestCleanWithoutTerminalNeedsYes(t *testing.T) {
	h := fixture(t)
	if code := h.run("clean"); code != ExitUsage {
		t.Fatalf("exit %d", code)
	}
	if len(h.pickers) != 0 {
		t.Fatal("picker must not open without a terminal")
	}
}

func TestDashboard(t *testing.T) {
	h := fixture(t)
	// Not a terminal: behaves like scan.
	if code := h.run(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if len(h.pickers) != 0 || !strings.Contains(h.out.String(), "Project artifacts") {
		t.Fatalf("expected the scan report, got:\n%s", h.out.String())
	}
	h.tty()
	if code := h.run(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if len(h.pickers) != 1 {
		t.Fatal("dashboard picker not opened")
	}
	opt := h.pickers[0]
	if opt.Title != "lu-cleaner" || !opt.Smart || len(opt.Providers) != 3 || len(opt.Filter.Categories) != 0 {
		t.Fatalf("unexpected dashboard options %+v", opt)
	}
}

func TestArtifactsCommand(t *testing.T) {
	h := fixture(t).tty()
	root := filepath.Join(h.home, "work")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if code := h.run("artifacts", root, "-t", "node_modules,pods", "-t", "android-build"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut.String())
	}
	opt := h.pickers[0]
	if !opt.Flat || opt.Title != "Project artifacts" || opt.Smart {
		t.Fatalf("unexpected options %+v", opt)
	}
	if !reflect.DeepEqual(opt.Filter.Categories, []core.Category{core.CatArtifacts}) {
		t.Fatalf("categories %v", opt.Filter.Categories)
	}
	// Aliases are added: pods is the ios-pods kind, android-build is
	// gradle-build outside an Android project.
	if !reflect.DeepEqual(opt.Filter.Kinds, []string{"node_modules", "pods", "android-build", "ios-pods", "gradle-build"}) {
		t.Fatalf("kinds %v", opt.Filter.Kinds)
	}
	if !reflect.DeepEqual(opt.Env.Roots, []string{root}) {
		t.Fatalf("roots %v", opt.Env.Roots)
	}
	if len(opt.Providers) != 1 || opt.Providers[0].ID() != "artifacts" {
		t.Fatalf("providers %v", opt.Providers)
	}
	// The root itself is protected, its content is not.
	if !opt.Env.IsProtected(root) || opt.Env.IsProtected(filepath.Join(root, "app", "node_modules")) {
		t.Fatal("wrong protection of roots")
	}

	if code := h.run("artifacts", filepath.Join(h.home, "missing")); code != ExitUsage {
		t.Fatalf("missing root: exit %d", code)
	}
	if code := h.run("artifacts", "-c", "ai"); code != ExitUsage {
		t.Fatalf("incompatible category: exit %d", code)
	}
}

func TestArtifactsListWithoutTerminal(t *testing.T) {
	h := fixture(t)
	if code := h.run("artifacts", "-t", "node_modules"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	out := h.out.String()
	if !strings.Contains(out, "app-stale") || strings.Contains(out, "app-safe") || strings.Contains(out, "feature-x") {
		t.Fatalf("unexpected report:\n%s", out)
	}
}

func TestWorktreesTable(t *testing.T) {
	h := fixture(t)
	if code := h.run("worktrees"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	out := h.out.String()
	for _, want := range []string{"NAME", "TOOL", "BRANCH", "STATUS", "feature-x", "codex", "feature/x", "dirty", "4.00 GB", "~/dev/feature-x/codex-worktree"} {
		if !strings.Contains(out, want) {
			t.Errorf("worktree table lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "app-stale") {
		t.Errorf("worktree table shows other categories:\n%s", out)
	}

	h.tty()
	if code := h.run("wt"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if opt := h.pickers[0]; !opt.Flat || opt.Title != "Worktrees" || opt.Filter.Categories[0] != core.CatWorktrees {
		t.Fatalf("unexpected options %+v", opt)
	}
}

func TestDevicesPicker(t *testing.T) {
	h := fixture(t).tty()
	h.provs = append(h.provs, &fakeProvider{id: "apple", cats: []core.Category{core.CatSimulators, core.CatXcode}},
		&fakeProvider{id: "android", cats: []core.Category{core.CatAndroid}})
	if code := h.run("devices"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	opt := h.pickers[0]
	if !reflect.DeepEqual(opt.Filter.Categories, []core.Category{core.CatSimulators, core.CatAndroid}) {
		t.Fatalf("categories %v", opt.Filter.Categories)
	}
	var ids []string
	for _, p := range opt.Providers {
		ids = append(ids, p.ID())
	}
	if !reflect.DeepEqual(ids, []string{"apple", "android"}) {
		t.Fatalf("providers %v", ids)
	}
}

// ------------------------------------------------------------ scan

func TestScanTable(t *testing.T) {
	h := fixture(t)
	if code := h.run("scan"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut.String())
	}
	out := h.out.String()
	for _, want := range []string{
		"Worktrees", "Project artifacts", "AI tools",
		"NAME", "SIZE", "AGE", "RISK", "RECO", "PATH",
		"~/dev/app-stale/node_modules", "2.00 GB", "2mo", "★",
		"! 3 uncommitted changes",
		"run: lu-cleaner clean --smart",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("scan output lacks %q", want)
		}
	}
	if strings.Contains(out, "tiny") {
		t.Error("items below min_size must be hidden")
	}
	if strings.Contains(out, "📦") || strings.Contains(out, "\x1b[") {
		t.Error("no emoji nor ANSI codes when stdout is not a terminal")
	}
	// Category order follows core.Categories: worktrees, artifacts, ..., ai.
	if !(strings.Index(out, "Worktrees") < strings.Index(out, "Project artifacts") &&
		strings.Index(out, "Project artifacts") < strings.Index(out, "AI tools")) {
		t.Errorf("wrong category order:\n%s", out)
	}
	// Within a category: biggest first.
	if strings.Index(out, "app-caution") > strings.Index(out, "app-stale") {
		t.Errorf("items not sorted by size:\n%s", out)
	}
	if !strings.Contains(out, "★ 3.30 GB recommended") {
		t.Errorf("recommended total missing:\n%s", out)
	}
	t.Log("\n" + out)
}

func TestScanTableAlignment(t *testing.T) {
	h := fixture(t)
	h.run("scan", "-c", "artifacts")
	var header string
	var rows []string
	for _, l := range strings.Split(h.out.String(), "\n") {
		if strings.Contains(l, "NAME") && header == "" {
			header = l
		} else if strings.Contains(l, "~/dev/") {
			rows = append(rows, l)
		}
	}
	col := strings.Index(header, "PATH")
	if col < 0 || len(rows) != 4 {
		t.Fatalf("header %q rows %d", header, len(rows))
	}
	for _, r := range rows {
		// ★ is multi-byte: compare display columns.
		if got := dispWidth(r[:strings.Index(r, "~/dev/")]); got != col {
			t.Errorf("PATH not aligned (col %d, want %d): %q", got, col, r)
		}
	}
}

func TestScanFiltersAndTop(t *testing.T) {
	h := newHarness(t)
	var items []*core.Item
	for i := 0; i < 20; i++ {
		items = append(items, h.mkItem(fmt.Sprintf("p%02d", i), core.CatArtifacts, "node_modules", int64(i+2)*mb, core.RiskModerate, 40*day))
	}
	h.provs = []core.Provider{&fakeProvider{id: "artifacts", cats: []core.Category{core.CatArtifacts}, items: items}}

	h.run("scan")
	if !strings.Contains(h.out.String(), "… 5 more") {
		t.Fatalf("default top 15 not applied:\n%s", h.out.String())
	}
	h.run("scan", "--all")
	if strings.Contains(h.out.String(), "more (") || !strings.Contains(h.out.String(), "p00") {
		t.Fatalf("--all must list everything:\n%s", h.out.String())
	}
	h.run("scan", "--top", "3")
	if !strings.Contains(h.out.String(), "… 17 more") {
		t.Fatalf("--top 3:\n%s", h.out.String())
	}
	h.run("scan", "--min-size", "20MB", "--all")
	if strings.Count(h.out.String(), "node_modules") != 2 { // p18 (20MB), p19 (21MB)
		t.Fatalf("--min-size:\n%s", h.out.String())
	}
	if code := h.run("scan", "--top", "-1"); code != ExitUsage {
		t.Fatalf("negative top: exit %d", code)
	}
}

func TestScanSummaryAndSmart(t *testing.T) {
	h := fixture(t)
	h.run("scan", "--summary")
	out := h.out.String()
	if !strings.Contains(out, "CATEGORY") || !strings.Contains(out, "Project artifacts") || strings.Contains(out, "app-stale") {
		t.Fatalf("summary:\n%s", out)
	}
	h.run("scan", "--smart")
	out = h.out.String()
	if strings.Contains(out, "app-fresh") || strings.Contains(out, "feature-x") || !strings.Contains(out, "app-stale") {
		t.Fatalf("--smart:\n%s", out)
	}
}

func TestScanJSONShape(t *testing.T) {
	h := fixture(t)
	h.provs = append(h.provs, &fakeProvider{id: "broken", cats: []core.Category{core.CatSystem}, err: errors.New("boom")})
	if code := h.run("scan", "--json"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	var r map[string]any
	if err := json.Unmarshal(h.out.Bytes(), &r); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, h.out.String())
	}
	for _, k := range []string{"version", "generated_at", "disk", "items", "totals", "errors"} {
		if _, ok := r[k]; !ok {
			t.Errorf("missing key %q", k)
		}
	}
	disk := r["disk"].(map[string]any)
	for _, k := range []string{"total", "free", "used"} {
		if _, ok := disk[k]; !ok {
			t.Errorf("disk lacks %q", k)
		}
	}
	items := r["items"].([]any)
	if len(items) != 6 { // tiny is below min_size
		t.Fatalf("%d items", len(items))
	}
	byName := map[string]map[string]any{}
	for _, raw := range items {
		it := raw.(map[string]any)
		for _, k := range []string{"id", "provider", "category", "category_title", "kind", "name", "size", "risk", "method", "age_days", "recommended"} {
			if _, ok := it[k]; !ok {
				t.Errorf("item lacks %q: %v", k, it)
			}
		}
		byName[it["name"].(string)] = it
	}
	stale := byName["app-stale"]
	if stale["recommended"] != true || stale["age_days"].(float64) != 60 || stale["risk"] != "moderate" ||
		stale["category_title"] != "Project artifacts" || stale["method"] != "delete" {
		t.Errorf("app-stale: %v", stale)
	}
	if byName["app-fresh"]["recommended"] != false {
		t.Error("app-fresh must not be recommended")
	}
	if byName["claude-cache"]["age_days"] != nil {
		t.Error("unknown age must be null")
	}
	if _, ok := byName["claude-cache"]["last_used"]; ok {
		t.Error("zero last_used must be omitted")
	}
	totals := r["totals"].(map[string]any)
	if totals["recommended"].(float64) != float64(500*mb+2*gb+800*mb) {
		t.Errorf("recommended total %v", totals["recommended"])
	}
	if totals["size"].(float64) != float64(500*mb+2*gb+1*gb+3*gb+4*gb+800*mb) {
		t.Errorf("total %v", totals["size"])
	}
	byCat := totals["by_category"].(map[string]any)
	if byCat["worktrees"].(float64) != float64(4*gb) || len(byCat) != 3 {
		t.Errorf("by_category %v", byCat)
	}
	if errs := r["errors"].(map[string]any); errs["broken"] != "boom" {
		t.Errorf("errors %v", errs)
	}
	if h.errOut.Len() != 0 {
		t.Errorf("JSON mode must keep stderr quiet without a terminal: %q", h.errOut.String())
	}
}

// ------------------------------------------------------------ usage errors

func TestUsageErrors(t *testing.T) {
	h := fixture(t)
	for _, args := range [][]string{
		{"foo"},
		{"scan", "--bogus"},
		{"scan", "extra-arg"},
		{"scan", "--risk", "wild"},
		{"scan", "-c", "nope"},
		{"scan", "--older-than", "soon"},
		{"scan", "--min-size", "big"},
		{"scan", "--sort", "color"},
		{"analyze", "a", "b"},
		{"clean", "--json"},
		{"history", "--limit", "-2"},
	} {
		if code := h.run(args...); code != ExitUsage {
			t.Errorf("%v: exit %d, want %d (stderr %q)", args, code, ExitUsage, h.errOut.String())
		}
		if !strings.Contains(h.errOut.String(), "--help") {
			t.Errorf("%v: no usage hint in %q", args, h.errOut.String())
		}
	}
}

func TestVersion(t *testing.T) {
	h := newHarness(t)
	h.run("--version")
	if strings.TrimSpace(h.out.String()) != "lu-cleaner test" {
		t.Fatalf("--version: %q", h.out.String())
	}
	h.run("version", "--json")
	var v map[string]string
	if err := json.Unmarshal(h.out.Bytes(), &v); err != nil || v["version"] != "test" {
		t.Fatalf("version --json: %v %q", err, h.out.String())
	}
}

// ------------------------------------------------------------ other commands

func TestHistory(t *testing.T) {
	h := newHarness(t)
	if h.run("history"); !strings.Contains(h.out.String(), "Nothing cleaned yet") {
		t.Fatalf("empty history: %q", h.out.String())
	}
	h.history = []clean.HistoryEntry{
		{Time: testNow.Add(-48 * 3600e9), Name: "old", Path: filepath.Join(h.home, "a"), Method: "delete", Status: "done", Size: 2 * gb},
		{Time: testNow.Add(-3600e9), Name: "sims", Command: "xcrun simctl delete unavailable", Method: "command", Status: "done", Size: 1 * gb},
		{Time: testNow, Name: "busy", Path: filepath.Join(h.home, "b"), Method: "delete", Status: "failed", Size: 5 * gb, Error: "permission denied"},
	}
	h.run("history", "--limit", "2")
	out := h.out.String()
	for _, want := range []string{"TIME", "STATUS", "METHOD", "PATH/COMMAND", "$ xcrun simctl delete unavailable", "! permission denied", "last 2 of 3", "3.00 GB"} {
		if !strings.Contains(out, want) {
			t.Errorf("history lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "~/a") {
		t.Errorf("--limit 2 must show the newest two only:\n%s", out)
	}
	if strings.Index(out, "busy") > strings.Index(out, "sims") {
		t.Errorf("newest first expected:\n%s", out)
	}

	h.run("history", "--json", "--limit", "0")
	var r struct {
		Freed   int64                `json:"freed"`
		Total   int                  `json:"total"`
		Entries []clean.HistoryEntry `json:"entries"`
	}
	if err := json.Unmarshal(h.out.Bytes(), &r); err != nil || r.Freed != 3*gb || len(r.Entries) != 3 {
		t.Fatalf("history --json: %v %+v", err, r)
	}
}

func TestDoctor(t *testing.T) {
	h := fixture(t)
	h.app.Snapshots = func(context.Context) []string {
		return []string{"com.apple.TimeMachine.2026-09-27-101010.local"}
	}
	h.app.Running = func(names ...string) []string {
		if names[0] == "Xcode" {
			return []string{"Xcode"}
		}
		return nil
	}
	trash := filepath.Join(h.home, ".Trash")
	if err := os.MkdirAll(trash, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(trash, "big.bin"), make([]byte, 64<<10), 0o600); err != nil {
		t.Fatal(err)
	}

	if code := h.run("doctor", "--json"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	var r doctorReport
	if err := json.Unmarshal(h.out.Bytes(), &r); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(r.Snapshots) != 1 || len(r.Running) != 1 || r.Running[0].App != "Xcode" {
		t.Fatalf("unexpected report %+v", r)
	}
	if !r.Trash.Readable || r.Trash.Size < 64<<10 || r.Trash.Files != 1 {
		t.Fatalf("trash %+v", r.Trash)
	}
	if !r.Scanned || len(r.Categories) != 3 || r.Recommended != 500*mb+2*gb+800*mb || len(r.Tips) < 5 {
		t.Fatalf("scan summary %+v", r)
	}

	h.run("doctor", "--no-scan")
	out := h.out.String()
	for _, want := range []string{
		"90% used", "APFS local snapshots (1)", "tmutil thinlocalsnapshots / 999999999999 4",
		"sudo tmutil deletelocalsnapshots 2026-09-27-101010", "Xcode", "empty it to free the space",
		"Why isn't my space freed?", "Hardlinks", "Purgeable",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Space by category") {
		t.Error("--no-scan must skip the scan")
	}
}

func TestCatalogCommand(t *testing.T) {
	h := newHarness(t)
	h.entries = []catalog.Entry{
		{ID: "xcode-derived-data", Category: core.CatXcode, Name: "DerivedData", Paths: []string{"~/Library/Developer/Xcode/DerivedData"}, Risk: core.RiskSafe},
		{ID: "simctl-unavailable", Category: core.CatSimulators, Name: "Unavailable simulators", Method: core.MethodCommand,
			Command: []string{"xcrun", "simctl", "delete", "unavailable"}, Risk: core.RiskSafe, Mode: catalog.Each},
	}
	h.run("catalog")
	out := h.out.String()
	for _, want := range []string{"ID", "CATEGORY", "RISK", "METHOD", "PATHS", "xcode-derived-data", "$ xcrun simctl delete unavailable", "2 entries"} {
		if !strings.Contains(out, want) {
			t.Errorf("catalog lacks %q:\n%s", want, out)
		}
	}
	h.run("catalog", "-c", "sim", "--json")
	var entries []map[string]any
	if err := json.Unmarshal(h.out.Bytes(), &entries); err != nil || len(entries) != 1 ||
		entries[0]["id"] != "simctl-unavailable" || entries[0]["mode"] != "each" || entries[0]["method"] != "command" {
		t.Fatalf("catalog --json: %v %+v", err, entries)
	}
}

func TestConfigShow(t *testing.T) {
	h := newHarness(t)
	for _, d := range []string{"dev", "Projects", ".codex/worktrees"} {
		if err := os.MkdirAll(filepath.Join(h.home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	h.cfg.Exclude = []string{"~/dev/huge"}
	if code := h.run("config", "show", "--json"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut.String())
	}
	var v configView
	if err := json.Unmarshal(h.out.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(v.Roots, []string{filepath.Join(h.home, "dev"), filepath.Join(h.home, "Projects")}) || v.RootsSource != "auto-detected" {
		t.Fatalf("roots %v (%s)", v.Roots, v.RootsSource)
	}
	if !reflect.DeepEqual(v.WorktreeRoots, []string{filepath.Join(h.home, ".codex/worktrees")}) {
		t.Fatalf("worktree roots %v", v.WorktreeRoots)
	}
	if !reflect.DeepEqual(v.Exclude, []string{filepath.Join(h.home, "dev/huge")}) || v.Config["stale_after"] != "14d" {
		t.Fatalf("view %+v", v)
	}
	h.run("config")
	if !strings.Contains(h.out.String(), "~/dev") || !strings.Contains(h.out.String(), "stale_after") {
		t.Fatalf("config show:\n%s", h.out.String())
	}
	h.run("config", "path")
	if strings.TrimSpace(h.out.String()) != os.Getenv("LU_CLEANER_CONFIG") {
		t.Fatalf("config path: %q", h.out.String())
	}
	if code := h.run("config", "init"); code != 0 || !fileExists(os.Getenv("LU_CLEANER_CONFIG")) {
		t.Fatalf("config init: exit %d %s", code, h.errOut.String())
	}
	if code := h.run("config", "init"); code != ExitFailure {
		t.Fatalf("second config init must fail, exit %d", code)
	}
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func TestAnalyze(t *testing.T) {
	h := newHarness(t)
	dir := filepath.Join(h.home, "proj")
	for name, size := range map[string]int{"small.txt": 10, "sub/big.bin": 300 << 10} {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if code := h.run("analyze", dir); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut.String())
	}
	out := h.out.String()
	if !strings.Contains(out, "sub/") || strings.Index(out, "sub/") > strings.Index(out, "small.txt") {
		t.Fatalf("analyze report:\n%s", out)
	}
	h.tty()
	if code := h.run("analyze"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if len(h.analyzes) != 1 || h.analyzes[0].Root != h.home || h.analyzes[0].Clean.Guard == nil {
		t.Fatalf("analyze options %+v", h.analyzes)
	}
	if code := h.run("analyze", filepath.Join(dir, "small.txt")); code != ExitUsage {
		t.Fatalf("file argument: exit %d", code)
	}
}

// ------------------------------------------------------------ rendering

func TestTableFitsTerminalWidth(t *testing.T) {
	var buf strings.Builder
	o := &output{w: &buf, tty: true, width: 60}
	tb := newTable("NAME", "SIZE", "PATH")
	tb.right[1] = true
	tb.maxw[0] = 44
	tb.leftTrunc[2] = true
	tb.shrink = 2
	tb.add(nil, "a-very-long-worktree-name-that-goes-on-and-on", "12.0 GB", "~/local_sources/some/really/long/path/to/node_modules")
	tb.add(nil, "日本語-app", "1.20 GB", "~/short")
	tb.render(o, "  ", true)
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines: %q", lines)
	}
	pathCol := dispWidth(lines[0][:strings.Index(lines[0], "PATH")])
	for _, l := range lines {
		if w := dispWidth(l); w > 60 {
			t.Errorf("line wider than the terminal (%d): %q", w, l)
		}
	}
	if !strings.HasSuffix(lines[1], "node_modules") || !strings.Contains(lines[1], "…") {
		t.Errorf("path must be truncated from the left: %q", lines[1])
	}
	if got := dispWidth(lines[2][:strings.Index(lines[2], "~/short")]); got != pathCol {
		t.Errorf("wide runes break alignment: col %d want %d: %q", got, pathCol, lines[2])
	}
}

func TestTruncateAndFormat(t *testing.T) {
	cases := []struct{ got, want string }{
		{truncRight("abcdef", 4), "abc…"},
		{truncRight("abc", 4), "abc"},
		{truncLeft("/a/b/c/d", 5), "…/c/d"},
		{truncRight("日本語テキスト", 5), "日本…"},
		{truncLeft("日本語テキスト", 5), "…スト"},
		{formatCount(0), "0"},
		{formatCount(999), "999"},
		{formatCount(1234), "1 234"},
		{formatCount(1234567), "1 234 567"},
		{formatCount(-1234), "-1 234"},
		{bar(50, 4), "██░░"},
		{snapshotDate("com.apple.TimeMachine.2026-09-27-101010.local"), "2026-09-27-101010"},
	}
	for i, c := range cases {
		if c.got != c.want {
			t.Errorf("case %d: got %q want %q", i, c.got, c.want)
		}
	}
}

func TestInterruptedScan(t *testing.T) {
	h := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if code := h.app.Run(ctx, []string{"scan"}); code != ExitInterrupted {
		t.Fatalf("exit %d, want %d", code, ExitInterrupted)
	}
	if code := h.app.Run(ctx, []string{"clean", "-y", "--smart"}); code != ExitInterrupted || len(h.cleaned) != 0 {
		t.Fatalf("interrupted clean: exit %d, %d clean calls", code, len(h.cleaned))
	}
}

func TestRootJSONIsScanJSON(t *testing.T) {
	h := fixture(t).tty()
	if code := h.run("--json", "-c", "worktrees"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	var r map[string]any
	if err := json.Unmarshal(h.out.Bytes(), &r); err != nil || len(r["items"].([]any)) != 1 {
		t.Fatalf("root --json: %v\n%s", err, h.out.String())
	}
	if len(h.pickers) != 0 {
		t.Fatal("--json must not open the picker")
	}
	if !strings.Contains(h.out.String(), `"meta"`) || !strings.Contains(h.out.String(), `"branch": "feature/x"`) {
		t.Fatalf("worktree meta missing:\n%s", h.out.String())
	}
}

func TestSetupRootsAndGuard(t *testing.T) {
	h := newHarness(t)
	mk := func(rel string) string {
		p := filepath.Join(h.home, rel)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	dev := mk("dev")
	mk("Projects/nested") // nested dirs are not roots by themselves
	nm := mk("dev/app/node_modules")
	if err := os.WriteFile(filepath.Join(dev, "app", "package.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dev, filepath.Join(h.home, "code")); err != nil { // same dir as ~/dev
		t.Fatal(err)
	}
	h.cfg.Protect = []string{"~/dev/keep"}

	c := newCLI(h.app)
	s, err := c.newSetup(nil)
	if err != nil {
		t.Fatal(err)
	}
	// "~/Dev" (case-insensitive twin of ~/dev) and the ~/code symlink are deduplicated.
	want := []string{dev, filepath.Join(h.home, "Projects")}
	if !reflect.DeepEqual(s.env.Roots, want) || s.rootsSource != "auto-detected" {
		t.Fatalf("roots %v (%s), want %v", s.env.Roots, s.rootsSource, want)
	}
	if s.env.MaxDepth != 8 || s.staleAfter != 14*day || s.minSize != 1e6 {
		t.Fatalf("setup %+v", s)
	}
	if err := s.guard.Check(dev, safetyOpts()); err == nil {
		t.Error("a root itself must never be removable")
	}
	if err := s.guard.Check(nm, safetyOpts()); err != nil {
		t.Errorf("node_modules inside a root must be removable: %v", err)
	}
	for p, want := range map[string]bool{
		dev:                                 true,  // a root
		h.home:                              true,  // contains roots
		nm:                                  false, // inside a root
		filepath.Join(h.home, ".ssh", "id"): true,  // built-in protection
		filepath.Join(dev, "keep", "x"):     true,  // config protect
		filepath.Join(h.home, "Library/Caches/foo"): false,
	} {
		if got := s.env.IsProtected(p); got != want {
			t.Errorf("Protected(%s) = %v, want %v", p, got, want)
		}
	}

	// --root: relative to the cwd, overrides the config / auto-detection.
	t.Chdir(h.home)
	c.f.roots = []string{"dev", "dev/app"}
	if s, err = c.newSetup(nil); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.env.Roots, []string{dev}) || s.rootsSource != "--root" {
		t.Fatalf("--root: %v (%s)", s.env.Roots, s.rootsSource)
	}
	c.f.roots = []string{"missing"}
	if _, err := c.newSetup(nil); err == nil {
		t.Fatal("a missing --root must be an error")
	}
}

func TestBuildFilter(t *testing.T) {
	h := newHarness(t)
	h.cfg.DisabledCategories = []string{"system"}
	c := newCLI(h.app)
	s, err := c.newSetup(nil)
	if err != nil {
		t.Fatal(err)
	}

	f, err := c.buildFilter(s, modeDisplay, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if f.MinSize != 1e6 || f.MaxRisk != core.RiskNever || len(f.Categories) != len(core.Categories)-1 || hasCat(f.Categories, core.CatSystem) {
		t.Fatalf("display defaults %+v", f)
	}
	f, _ = c.buildFilter(s, modeCleanYes, nil, nil)
	if f.MinSize != 0 || f.MaxRisk != core.RiskModerate {
		t.Fatalf("clean --yes defaults %+v", f)
	}

	c.f.categories = []string{"wt,sim", "ai"}
	c.f.kinds = []string{"node_modules, pods"}
	c.f.minSize, c.f.olderThan, c.f.risk = "2GB", "3w", "safe"
	f, err = c.buildFilter(s, modeCleanYes, nil, []string{"android-build"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.Categories, []core.Category{core.CatWorktrees, core.CatSimulators, core.CatAI}) ||
		!reflect.DeepEqual(f.Kinds, []string{"node_modules", "pods", "android-build", "ios-pods", "gradle-build"}) ||
		f.MinSize != 2e9 || f.OlderThan != 21*day || f.MaxRisk != core.RiskSafe {
		t.Fatalf("flags %+v", f)
	}

	// Forced categories intersect with --category.
	f, err = c.buildFilter(s, modeDisplay, []core.Category{core.CatSimulators, core.CatAndroid}, nil)
	if err != nil || !reflect.DeepEqual(f.Categories, []core.Category{core.CatSimulators}) {
		t.Fatalf("forced: %v %v", f.Categories, err)
	}

	var ee *exitError
	c.f = globalFlags{categories: []string{"system"}}
	if _, err := c.buildFilter(s, modeDisplay, nil, nil); !errors.As(err, &ee) || ee.code != ExitUsage {
		t.Fatalf("disabled category: %v", err)
	}
	c.f = globalFlags{risk: "extreme"}
	if _, err := c.buildFilter(s, modeDisplay, nil, nil); !errors.As(err, &ee) || ee.code != ExitUsage {
		t.Fatalf("bad risk: %v", err)
	}
}

func TestProvidersFor(t *testing.T) {
	all := []core.Provider{
		&fakeProvider{id: "a", cats: []core.Category{core.CatArtifacts}},
		&fakeProvider{id: "b", cats: []core.Category{core.CatSimulators, core.CatXcode}},
		&fakeProvider{id: "c", cats: []core.Category{core.CatAI, core.CatXcode}},
	}
	ids := func(ps []core.Provider) (out []string) {
		for _, p := range ps {
			out = append(out, p.ID())
		}
		return out
	}
	if got := ids(providersFor(all, nil)); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Fatal(got)
	}
	if got := ids(providersFor(all, []core.Category{core.CatXcode})); !reflect.DeepEqual(got, []string{"b", "c"}) {
		t.Fatal(got)
	}
	if got := providersFor(all, []core.Category{core.CatJS}); len(got) != 0 {
		t.Fatal(got)
	}
}

func safetyOpts() safety.Options { return safety.Options{} }

// slowProvider emits after a delay so the progress line gets drawn.
type slowProvider struct{ fakeProvider }

func (p *slowProvider) Scan(ctx context.Context, env *core.Env, emit core.Emit) error {
	time.Sleep(250 * time.Millisecond)
	env.Logf("slow provider scanning")
	return p.fakeProvider.Scan(ctx, env, emit)
}

func TestProgressLineOnTerminal(t *testing.T) {
	h := fixture(t)
	h.provs = append(h.provs, &slowProvider{fakeProvider{id: "slow", cats: []core.Category{core.CatJS}, items: []*core.Item{
		h.mkItem("npm-cache", core.CatJS, "npm-cache", 3*gb, core.RiskSafe, 0),
	}}})
	h.app.StderrTTY = true
	if code := h.run("scan", "-v"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	errOut := h.errOut.String()
	for _, want := range []string{"scanning… ", "providers · ", clearLine, "debug: slow provider scanning", "debug: provider slow done"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr lacks %q: %q", want, errOut)
		}
	}
	if !strings.HasSuffix(errOut, clearLine) && !strings.HasSuffix(errOut, "\n") {
		t.Errorf("progress line not cleared at the end: %q", errOut[max(0, len(errOut)-80):])
	}
	if strings.Contains(h.out.String(), "scanning") {
		t.Error("progress must stay on stderr")
	}
}

func TestScanShowsReportOnlyItems(t *testing.T) {
	h := fixture(t)
	dl := h.mkItem("Downloads", core.CatSystem, "downloads", 40*gb, core.RiskNever, 5*day)
	dl.Method = core.MethodReport
	h.provs = append(h.provs, &fakeProvider{id: "system", cats: []core.Category{core.CatSystem}, items: []*core.Item{dl}})

	h.run("scan", "-c", "system")
	out := h.out.String()
	if !strings.Contains(out, "Downloads") || !strings.Contains(out, "info") {
		t.Fatalf("report item not shown:\n%s", out)
	}
	if !strings.Contains(out, "Total 0 B in 0 items") {
		t.Fatalf("report-only items must not count in totals:\n%s", out)
	}
	h.run("scan", "-c", "system", "--risk", "caution")
	if strings.Contains(h.out.String(), "Downloads") {
		t.Fatalf("--risk caution hides report-only items:\n%s", h.out.String())
	}
	// "never" is not a maximum risk (core.ParseRisk refuses it): usage error.
	if code := h.run("clean", "-y", "-c", "system", "--risk", "never"); code != ExitUsage || len(h.cleaned) != 0 {
		t.Fatalf("--risk never: exit %d, %d calls", code, len(h.cleaned))
	}
	if code := h.run("clean", "-y", "-c", "system", "--risk", "caution"); code != 0 || len(h.cleaned) != 0 {
		t.Fatalf("report-only items are never cleaned: exit %d, %d calls", code, len(h.cleaned))
	}
}

func TestPickerCommandsWithYes(t *testing.T) {
	h := fixture(t)
	if code := h.run("artifacts", "-y", "-t", "node_modules", "-n"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut.String())
	}
	expectNames(t, h.lastClean(), "app-stale", "app-fresh")
	if !h.cleanOpt[len(h.cleanOpt)-1].DryRun {
		t.Fatal("--dry-run lost")
	}
	// The command's category is a narrowing filter; caution stays excluded.
	if code := h.run("artifacts", "--yes"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	expectNames(t, h.lastClean(), "app-safe", "app-stale", "app-fresh", "tiny")
	n := len(h.cleaned)
	if code := h.run("worktrees", "-y"); code != 0 || len(h.cleaned) != n {
		t.Fatalf("dirty (caution) worktrees need --risk caution: exit %d, calls %d", code, len(h.cleaned))
	}
	if code := h.run("worktrees", "-y", "--risk", "caution"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	expectNames(t, h.lastClean(), "feature-x")
}

func TestPickerSummaryAndExitCode(t *testing.T) {
	h := fixture(t).tty()
	it := h.mkItem("app-stale", core.CatArtifacts, "node_modules", 2*gb, core.RiskModerate, 60*day)
	h.app.Picker = func(context.Context, tui.PickerOptions) (*clean.Summary, error) {
		return &clean.Summary{Results: []clean.Result{
			{Item: it, Status: clean.StatusDone, Freed: 2 * gb},
			{Item: h.mkItem("busy", core.CatArtifacts, "pods", gb, core.RiskModerate, 0), Status: clean.StatusFailed, Error: "permission denied"},
		}, Estimated: 2 * gb}, nil
	}
	if code := h.run("clean"); code != ExitFailure {
		t.Fatalf("exit %d, want %d", code, ExitFailure)
	}
	out := h.out.String()
	if !strings.Contains(out, "1 item cleaned") || !strings.Contains(out, "2.00 GB freed") || !strings.Contains(out, "busy: permission denied") {
		t.Fatalf("summary:\n%s", out)
	}
}
