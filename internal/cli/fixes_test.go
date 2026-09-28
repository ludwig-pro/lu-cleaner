package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/clean"
	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/safety"
	"github.com/ludwig-pro/lu-cleaner/internal/tui"
)

// Regression tests of the review findings of the cli group.

func mkdirs(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// ---------------------------------------------- unpushed-worktree-removed-contract

func TestWorktreeHelpMatchesRemovalRules(t *testing.T) {
	h := newHarness(t)
	h.run("worktrees", "--help")
	help := strings.Join(strings.Fields(h.out.String()), " ")
	if strings.Contains(help, "refuses dirty, unpushed or locked") {
		t.Errorf("help still promises to refuse unpushed worktrees:\n%s", help)
	}
	for _, want := range []string{"keeps the branch", "commits on no branch", "locked", "uncommitted", "contains another worktree"} {
		if !strings.Contains(help, want) {
			t.Errorf("worktrees help lacks %q:\n%s", want, help)
		}
	}
	h.run("--help")
	if help := h.out.String(); strings.Contains(help, "dirty/unpushed") {
		t.Errorf("--force help still mentions unpushed worktrees:\n%s", help)
	}
	arch, err := os.ReadFile(filepath.Join("..", "..", "docs", "ARCHITECTURE.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(arch), "refuses dirty/unpushed/locked") {
		t.Error("docs/ARCHITECTURE.md still promises to refuse unpushed worktrees")
	}
}

// ---------------------------------------------- trash-summary-misleading

func TestTrashModePlanAndSummary(t *testing.T) {
	h := fixture(t)
	code := h.run("clean", "-y", "-c", "artifacts,worktrees", "--risk", "caution", "--trash")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut.String())
	}
	out := h.out.String()
	for _, want := range []string{
		"Trash mode: 1 item will be skipped",
		"feature-x: not possible in Trash mode",
		"moved to the Trash",
		"Space is only freed once the Trash is emptied",
		"Skipped: 1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	// 500 MB + 2 GB + 1 GB + 3 GB + 10 kB of artifacts moved, the 4 GB
	// worktree skipped: it must not be counted as moved (nor freed).
	if !strings.Contains(out, "6.50 GB moved to the Trash") || strings.Contains(out, "10.50 GB") {
		t.Errorf("wrong Trash total:\n%s", out)
	}
	if strings.Contains(out, " freed (estimated)") {
		t.Errorf("Trash moves reported as freed:\n%s", out)
	}
	if !strings.Contains(out, "Plan: 5 items · 6.50 GB") {
		t.Errorf("the plan total must leave out the skipped worktree:\n%s", out)
	}
}

func TestTrashModeSummarySplitsPermanentRemovals(t *testing.T) {
	h := fixture(t).tty()
	moved := h.mkItem("app-stale", core.CatArtifacts, "node_modules", 1*gb, core.RiskModerate, 60*day)
	sims := h.mkItem("sims", core.CatSimulators, "simctl", 2*gb, core.RiskSafe, 0)
	sims.Method, sims.Path, sims.Command = core.MethodCommand, "", []string{"xcrun", "simctl", "delete", "unavailable"}
	h.app.Picker = func(context.Context, tui.PickerOptions) (*clean.Summary, error) {
		return &clean.Summary{Trash: true, Estimated: 2 * gb, Trashed: 1 * gb, Results: []clean.Result{
			{Item: moved, Status: clean.StatusDone, Method: core.MethodTrash, Trashed: 1 * gb},
			{Item: sims, Status: clean.StatusDone, Method: core.MethodCommand, Freed: 2 * gb},
		}}, nil
	}
	if code := h.run("clean"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	out := h.out.String()
	if !strings.Contains(out, "1.00 GB moved to the Trash · 2.00 GB freed") {
		t.Fatalf("summary must split Trash moves and permanent removals:\n%s", out)
	}
	if strings.Contains(out, "3.00 GB moved to the Trash") {
		t.Fatalf("a command reported as moved to the Trash:\n%s", out)
	}
}

func TestPlanCleanTrashModeKeepsItemsInsideSkippedWorktrees(t *testing.T) {
	h := newHarness(t)
	wt := h.mkItem("wt", core.CatWorktrees, "codex-worktree", 4*gb, core.RiskModerate, 30*day)
	wt.Method = core.MethodWorktree
	nm := h.mkItem("nm", core.CatArtifacts, "node_modules", 1*gb, core.RiskModerate, 30*day)
	nm.Path = filepath.Join(wt.Path, "node_modules")
	f := core.Filter{MaxRisk: core.RiskModerate}
	if plan, _ := planClean([]*core.Item{wt, nm}, f, false, false, testNow, 14*day); len(plan) != 1 || plan[0] != wt {
		t.Fatalf("plan %v", plan)
	}
	// The worktree will be skipped in Trash mode: its node_modules can still go to the Trash.
	if plan, _ := planClean([]*core.Item{wt, nm}, f, false, true, testNow, 14*day); len(plan) != 2 || plan[0] != nm || plan[1] != wt {
		t.Fatalf("trash plan %v", plan)
	}
}

func TestTrashModeDryRun(t *testing.T) {
	h := fixture(t)
	if code := h.run("clean", "-y", "-c", "artifacts", "--trash", "-n"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if out := h.out.String(); !strings.Contains(out, "would move 3.50 GB to the Trash.") {
		t.Fatalf("dry run in Trash mode:\n%s", out)
	}
}

// ---------------------------------------------- roots-override-not-scoped

// scopeFixture: artifacts inside ~/work and in the implicit roots (AI
// worktrees, Codex chats) that an explicit root must leave alone.
func scopeFixture(t *testing.T) (*harness, string) {
	h := fixture(t)
	root := filepath.Join(h.home, "work")
	mkdirs(t, root)
	in := h.mkItem("inside", core.CatArtifacts, "node_modules", 1*gb, core.RiskModerate, 60*day)
	in.Path = filepath.Join(root, "app", "node_modules")
	wt := h.mkItem("codex-wt", core.CatArtifacts, "node_modules", 2*gb, core.RiskModerate, 60*day)
	wt.Path = filepath.Join(h.home, ".codex", "worktrees", "30ce", "app", "node_modules")
	chat := h.mkItem("codex-chat", core.CatArtifacts, "js-build", 3*gb, core.RiskSafe, 60*day)
	chat.Path = filepath.Join(h.home, "Documents", "Codex", "x", "build")
	h.provs = []core.Provider{
		&fakeProvider{id: "artifacts", cats: []core.Category{core.CatArtifacts}, items: []*core.Item{in, wt, chat}},
		h.provs[1], // worktrees
	}
	return h, root
}

func TestExplicitRootsRestrictArtifacts(t *testing.T) {
	h, root := scopeFixture(t)
	if code := h.run("artifacts", root, "-y", "-n"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut.String())
	}
	expectNames(t, h.lastClean(), "inside")

	if code := h.run("clean", "-y", "-c", "artifacts", "--root", root, "-n"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut.String())
	}
	expectNames(t, h.lastClean(), "inside")

	// APFS is case-insensitive: a root typed with another case still matches.
	if code := h.run("artifacts", filepath.Join(h.home, "WORK"), "-y", "-n"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut.String())
	}
	expectNames(t, h.lastClean(), "inside")

	// The report and the picker get the same scope.
	h.run("artifacts", "--list", root)
	if out := h.out.String(); !strings.Contains(out, "inside") || strings.Contains(out, "codex") {
		t.Fatalf("report outside the root:\n%s", out)
	}
	h.tty()
	h.run("artifacts", root)
	if !h.pickers[0].Env.ExplicitRoots {
		t.Fatal("env.ExplicitRoots not set")
	}

	// Without explicit roots nothing is restricted.
	h.app.StdinTTY, h.app.StdoutTTY = false, false
	if code := h.run("artifacts", "-y", "-n"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	expectNames(t, h.lastClean(), "inside", "codex-wt", "codex-chat")

	// --root does not restrict the other categories.
	if code := h.run("clean", "-y", "-c", "worktrees", "--risk", "caution", "--root", root, "-n"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	expectNames(t, h.lastClean(), "feature-x")
}

func TestSlashIsNotARoot(t *testing.T) {
	h, _ := scopeFixture(t)
	n := len(h.cleaned)
	for _, args := range [][]string{{"artifacts", "/", "-y", "-n"}, {"clean", "-y", "-c", "artifacts", "--root", "/"}} {
		if code := h.run(args...); code != ExitUsage {
			t.Errorf("%v: exit %d, want %d", args, code, ExitUsage)
		}
		if !strings.Contains(h.errOut.String(), "whole disk") {
			t.Errorf("%v: stderr %q", args, h.errOut.String())
		}
	}
	if len(h.cleaned) != n {
		t.Fatal("clean must not run")
	}
}

// ---------------------------------------------- case-mismatch-inuse-bypass (CLI side)

func TestRootsTakeTheirOnDiskSpelling(t *testing.T) {
	h := newHarness(t)
	code := filepath.Join(h.home, "Code")
	nfc := filepath.Join(h.home, "proj\u00e9") // "é" stored precomposed
	mkdirs(t, code, nfc)
	h.cfg.Roots = []string{"~/code"}
	c := newCLI(h.app)
	s, err := c.newSetup(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.env.Roots) != 1 || s.env.Roots[0] != code {
		t.Fatalf("config root: %q, want %q", s.env.Roots, code)
	}
	nfd := filepath.Join(h.home, "proje\u0301") // typed decomposed
	s, err = c.newSetup([]string{filepath.Join(h.home, "CODE"), nfd})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.env.Roots) != 2 || s.env.Roots[0] != code || s.env.Roots[1] != nfc {
		t.Fatalf("explicit roots: %q", s.env.Roots)
	}
	if got := onDiskCase(filepath.Join(h.home, "code", "missing", "Deeper")); got != filepath.Join(code, "missing", "Deeper") {
		t.Fatalf("onDiskCase: %q", got)
	}
}

// ---------------------------------------------- yes-ignores-warn

func TestYesHoldsBackWarnedItems(t *testing.T) {
	h := fixture(t)
	busy := h.mkItem("busy-app", core.CatArtifacts, "node_modules", 1*gb, core.RiskModerate, 60*day)
	busy.Warn = "in use: 3 processes run inside the project (dev server)"
	h.provs[0].(*fakeProvider).items = append(h.provs[0].(*fakeProvider).items, busy)

	if code := h.run("clean", "-y", "-c", "artifacts"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	expectNames(t, h.lastClean(), "app-safe", "app-stale", "app-fresh", "tiny")
	out := h.out.String()
	if !strings.Contains(out, "Held back:") || !strings.Contains(out, "busy-app: in use: 3 processes") ||
		!strings.Contains(out, "--risk caution") {
		t.Fatalf("held items not reported:\n%s", out)
	}

	if code := h.run("artifacts", "-y", "-t", "node_modules", "--json"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	expectNames(t, h.lastClean(), "app-stale", "app-fresh")
	if !strings.Contains(h.errOut.String(), "with a warning left out") {
		t.Fatalf("--json: no warning on stderr: %q", h.errOut.String())
	}

	if code := h.run("clean", "-y", "-c", "artifacts", "--risk", "caution"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	expectNames(t, h.lastClean(), "app-safe", "app-stale", "app-fresh", "app-caution", "tiny", "busy-app")

	// Only held items: nothing to clean, still reported.
	h.provs = []core.Provider{&fakeProvider{id: "artifacts", cats: []core.Category{core.CatArtifacts}, items: []*core.Item{busy}}}
	n := len(h.cleaned)
	if code := h.run("clean", "-y", "-c", "artifacts"); code != 0 || len(h.cleaned) != n {
		t.Fatalf("exit %d, %d clean calls", code, len(h.cleaned)-n)
	}
	if out := h.out.String(); !strings.Contains(out, "Nothing to clean") || !strings.Contains(out, "busy-app: in use") {
		t.Fatalf("held items not reported:\n%s", out)
	}
}

func TestPlanCleanHeldNotCoveredByPlan(t *testing.T) {
	h := newHarness(t)
	outer := h.mkItem("outer", core.CatArtifacts, "node_modules", 2*gb, core.RiskModerate, 30*day)
	inner := h.mkItem("inner", core.CatArtifacts, "cache", 1*gb, core.RiskSafe, 30*day)
	inner.Path = filepath.Join(outer.Path, ".cache")
	inner.Warn = "in use"
	other := h.mkItem("other", core.CatArtifacts, "dist", 1*gb, core.RiskSafe, 30*day)
	other.Warn = "in use"
	plan, held := planClean([]*core.Item{outer, inner, other}, core.Filter{MaxRisk: core.RiskModerate}, false, false, testNow, 14*day)
	if len(plan) != 1 || plan[0] != outer || len(held) != 1 || held[0] != other {
		t.Fatalf("plan %v held %v", plan, held)
	}
	// Smart select already leaves warned items out: nothing is "held".
	if _, held := planClean([]*core.Item{other}, core.Filter{MaxRisk: core.RiskModerate}, true, false, testNow, 14*day); len(held) != 0 {
		t.Fatalf("smart: held %v", held)
	}
}

// ---------------------------------------------- exclude-protect-symlink

func TestExcludeAndProtectThroughSymlinks(t *testing.T) {
	h := newHarness(t)
	real := filepath.Join(h.home, "real")
	keep := filepath.Join(real, "keep", "node_modules")
	other := filepath.Join(real, "other", "node_modules")
	mkdirs(t, keep, other)
	if err := os.Symlink(real, filepath.Join(h.home, "code")); err != nil {
		t.Fatal(err)
	}
	a := h.mkItem("keep", core.CatArtifacts, "node_modules", gb, core.RiskModerate, 60*day)
	a.Path = keep
	b := h.mkItem("other", core.CatArtifacts, "node_modules", gb, core.RiskModerate, 60*day)
	b.Path = other
	h.provs = []core.Provider{&fakeProvider{id: "artifacts", cats: []core.Category{core.CatArtifacts}, items: []*core.Item{a, b}}}
	h.cfg.Roots = []string{"~/code"}

	for _, excl := range []string{"~/code/keep", "$HOME/real/keep", "${HOME}/code/keep", "~/Real/KEEP"} {
		h.cfg.Exclude = []string{excl}
		if code := h.run("clean", "-y", "-c", "artifacts", "-n"); code != 0 {
			t.Fatalf("exclude %s: exit %d: %s", excl, code, h.errOut.String())
		}
		expectNames(t, h.lastClean(), "other")
		c := newCLI(h.app)
		s, err := c.newSetup(nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.guard.Check(keep, safety.Options{}); err == nil && !strings.Contains(excl, "Real") {
			t.Errorf("exclude %s: the guard lets %s through", excl, keep)
		}
	}

	h.cfg.Exclude = nil
	h.cfg.Protect = []string{"~/code/keep"}
	c := newCLI(h.app)
	s, err := c.newSetup(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.guard.Check(keep, safety.Options{}); err == nil {
		t.Errorf("protect through a symlink: the guard lets %s through", keep)
	}
	if !s.env.IsProtected(keep) || s.env.IsProtected(other) {
		t.Error("wrong env.Protected")
	}

	// An undefined variable is an error, not a silently useless entry.
	h.cfg.Protect = []string{"$NOPE/keep"}
	if code := h.run("scan"); code == 0 || !strings.Contains(h.errOut.String(), "undefined variable $NOPE") {
		t.Fatalf("undefined variable: exit %d, stderr %q", code, h.errOut.String())
	}

	// config show flags entries that do not exist.
	h.cfg.Protect = []string{"~/code/keep", "~/typo"}
	if code := h.run("config", "show", "--json"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut.String())
	}
	var v configView
	if err := json.Unmarshal(h.out.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Missing) != 1 || v.Missing[0] != filepath.Join(h.home, "typo") {
		t.Fatalf("missing %v", v.Missing)
	}
	h.run("config", "show")
	if !strings.Contains(h.out.String(), "~/typo (does not exist)") {
		t.Fatalf("config show:\n%s", h.out.String())
	}
}

// ---------------------------------------------- target-pods-no-match

func TestKindAliasesAndUnmatchedKinds(t *testing.T) {
	h := newHarness(t)
	pods := h.mkItem("app", core.CatArtifacts, "ios-pods", gb, core.RiskModerate, 60*day)
	gradle := h.mkItem("lib", core.CatArtifacts, "gradle-build", gb, core.RiskSafe, 60*day)
	h.provs = []core.Provider{&fakeProvider{id: "artifacts", cats: []core.Category{core.CatArtifacts}, items: []*core.Item{pods, gradle}}}

	if code := h.run("artifacts", "-y", "-n", "-t", "node_modules", "-t", "pods"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	expectNames(t, h.lastClean(), "app")
	if !strings.Contains(h.errOut.String(), `kind "node_modules" matched nothing`) || strings.Contains(h.errOut.String(), `"pods"`) {
		t.Fatalf("stderr %q", h.errOut.String())
	}
	if code := h.run("clean", "-y", "-n", "-k", "android-build"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	expectNames(t, h.lastClean(), "lib")
	h.run("artifacts", "--list", "-t", "pod")
	if !strings.Contains(h.out.String(), "app") || h.errOut.Len() != 0 {
		t.Fatalf("pod alias: %s / %q", h.out.String(), h.errOut.String())
	}
	h.run("artifacts", "--help")
	if help := h.out.String(); strings.Contains(help, "-t pods") || !strings.Contains(help, "ios-pods") {
		t.Fatalf("help still uses the 'pods' kind:\n%s", help)
	}
}

// ---------------------------------------------- history-freed-counts-trash

func TestHistoryDoesNotCountTrashAsFreed(t *testing.T) {
	h := newHarness(t)
	h.history = []clean.HistoryEntry{
		{Time: testNow, Name: "a", Path: filepath.Join(h.home, "a"), Method: "delete", Status: "done", Size: 2 * gb},
		{Time: testNow, Name: "b", Path: filepath.Join(h.home, "b"), Method: "trash", Status: "done", Size: 5 * gb},
	}
	h.run("history")
	out := h.out.String()
	if !strings.Contains(out, "Total freed: 2.00 GB over 1 cleaned item") || !strings.Contains(out, "Moved to the Trash: 5.00 GB") {
		t.Fatalf("history:\n%s", out)
	}
	h.run("history", "--json")
	var r struct{ Freed, Trashed int64 }
	if err := json.Unmarshal(h.out.Bytes(), &r); err != nil || r.Freed != 2*gb || r.Trashed != 5*gb {
		t.Fatalf("history --json: %v %+v", err, r)
	}
}

func TestHistoryGroupItemsAndSkipReasons(t *testing.T) {
	h := newHarness(t)
	h.history = []clean.HistoryEntry{
		{Time: testNow, Name: "Metro caches", Location: filepath.Join(h.home, "tmp", "T"), Count: 23,
			Paths: []string{filepath.Join(h.home, "tmp", "T", "metro-1")}, Method: "delete", Status: "done", Size: gb},
		{Time: testNow, Name: "transcripts", Paths: []string{filepath.Join(h.home, "a"), filepath.Join(h.home, "b")}, Count: 2,
			Method: "delete", Status: "done", Size: gb},
		{Time: testNow, Name: "wt", Path: filepath.Join(h.home, "wt"), Method: "worktree", Status: "skipped",
			Message: "not possible in Trash mode (it would delete permanently)"},
	}
	h.run("history")
	out := h.out.String()
	for _, want := range []string{"~/tmp/T (23 paths)", "~/a (+1 more)", "! not possible in Trash mode"} {
		if !strings.Contains(out, want) {
			t.Errorf("history lacks %q:\n%s", want, out)
		}
	}
}

// ---------------------------------------------- all-categories-disabled

func TestAllCategoriesDisabled(t *testing.T) {
	h := fixture(t)
	for _, ci := range core.Categories {
		h.cfg.DisabledCategories = append(h.cfg.DisabledCategories, string(ci.ID))
	}
	for _, args := range [][]string{{"scan"}, {"clean", "-y", "--smart", "-n"}} {
		if code := h.run(args...); code != ExitUsage || !strings.Contains(h.errOut.String(), "all categories are disabled") {
			t.Errorf("%v: exit %d, stderr %q", args, code, h.errOut.String())
		}
	}
	if len(h.cleaned) != 0 {
		t.Fatal("nothing may be cleaned")
	}
}

// ---------------------------------------------- root-yes-ignored

func TestRootYesRunsCleanYes(t *testing.T) {
	h := fixture(t)
	if code := h.run("--yes", "--smart", "-n"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut.String())
	}
	expectNames(t, h.lastClean(), "app-safe", "app-stale", "claude-cache")
	if code := h.run("--yes"); code != ExitUsage || !strings.Contains(h.errOut.String(), "narrowing filter") {
		t.Fatalf("root --yes without filter: exit %d, %q", code, h.errOut.String())
	}
	h.tty()
	if code := h.run("-y", "-c", "artifacts", "-n"); code != 0 || len(h.pickers) != 0 {
		t.Fatalf("root --yes on a terminal must not open the picker: exit %d", code)
	}
}

// ---------------------------------------------- terminal-escape-injection

const evil = "evil\x1b]0;PWNED\a\x1b[2Jx\u009b31m\u202e"

func evilFixture(t *testing.T) *harness {
	h := newHarness(t)
	it := h.mkItem(evil, core.CatArtifacts, "node_modules", 2*gb, core.RiskModerate, 60*day)
	it.Path = filepath.Join(h.home, "dev", evil, "node_modules")
	it.Warn = "in use by\x1b[1A\x1b[2K fake line"
	wt := h.mkItem("wt"+evil, core.CatWorktrees, "codex-worktree", gb, core.RiskModerate, 60*day)
	wt.Method = core.MethodWorktree
	wt.Meta = map[string]string{"tool": "codex", "branch": "b\x1b]2;x\a", "status": "clean"}
	h.provs = []core.Provider{
		&fakeProvider{id: "artifacts", cats: []core.Category{core.CatArtifacts}, items: []*core.Item{it}},
		&fakeProvider{id: "worktrees", cats: []core.Category{core.CatWorktrees}, items: []*core.Item{wt}},
		&fakeProvider{id: "bad\x1b[2J", cats: []core.Category{core.CatAI}, err: errors.New("cannot read " + evil)},
	}
	h.failNames[evil] = true
	h.history = []clean.HistoryEntry{{Time: testNow, Name: evil, Path: it.Path, Method: "delete", Status: "failed", Error: "rm " + evil}}
	return h
}

func assertNoControls(t *testing.T, what, s string) {
	t.Helper()
	for _, bad := range []string{"\x1b]", "\x1b[2J", "\x1b[1A", "\a", "\u009b", "\u202e"} {
		if strings.Contains(s, bad) {
			t.Errorf("%s: raw %q reaches the terminal:\n%q", what, bad, s)
		}
	}
}

func TestTerminalEscapesAreSanitized(t *testing.T) {
	for _, tty := range []bool{false, true} {
		h := evilFixture(t)
		if tty {
			h.tty()
			h.app.Width = func() int { return 400 }
		}
		for _, args := range [][]string{
			{"scan"}, {"scan", "--summary"}, {"artifacts", "--list"}, {"worktrees", "--list"},
			{"clean", "-y", "-c", "artifacts", "--risk", "caution"}, {"history"}, {"scan", "-v", "--json"},
			{"artifacts", filepath.Join(h.home, "missing"+evil)},
		} {
			h.run(args...)
			what := fmt.Sprintf("tty=%v %v", tty, args)
			assertNoControls(t, what+" stdout", h.out.String())
			assertNoControls(t, what+" stderr", h.errOut.String())
			if !tty && strings.Contains(h.out.String(), "\x1b") {
				t.Errorf("%s: ESC on a non-terminal:\n%q", what, h.out.String())
			}
		}
	}

	h := evilFixture(t)
	h.run("scan")
	if !strings.Contains(h.out.String(), `evil\x1b]0;PWNED\a\x1b[2Jx\u009b31m\u202e`) {
		t.Errorf("names must stay readable (escaped):\n%s", h.out.String())
	}
	// JSON keeps the exact value (decodable), with C1 / bidi escaped too.
	h.run("scan", "--json")
	if strings.Contains(h.out.String(), "\u009b") || !strings.Contains(h.out.String(), `\u009b`) {
		t.Errorf("JSON: C1 control not escaped")
	}
	var r jsonReport
	if err := json.Unmarshal(h.out.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, it := range r.Items {
		found = found || it.Name == evil
	}
	if !found {
		t.Error("JSON must round-trip the exact name")
	}
}

func TestSanitize(t *testing.T) {
	for in, want := range map[string]string{
		"plain/path é 日本":  "plain/path é 日本",
		"a\x1b[2Jb":        `a\x1b[2Jb`,
		"bell\a\n\t\r\x7f": `bell\a\n\t\r\x7f`,
		"c1\u009b":         `c1\u009b`,
		"rtl\u202egnp.exe": `rtl\u202egnp.exe`,
		"bad\xffutf8":      `bad\xffutf8`,
	} {
		if got := sanitize(in); got != want {
			t.Errorf("sanitize(%q) = %q, want %q", in, got, want)
		}
	}
	if got := sanitizeLines("a\nb\x1b", "  "); got != "a\n  b\\x1b" {
		t.Errorf("sanitizeLines: %q", got)
	}
}

// ---------------------------------------------- second-ctrl-c-swallowed

// TestSignalHelperProcess is run as a subprocess by TestSignals.
func TestSignalHelperProcess(t *testing.T) {
	mode := os.Getenv("LU_CLEANER_SIGNAL_HELPER")
	if mode == "" {
		t.Skip("helper process for TestSignals")
	}
	code := runWithSignals(func(ctx context.Context) int {
		fmt.Println("ready")
		if mode == "stuck" {
			time.Sleep(10 * time.Second) // ignores ctx, like a huge RemoveAll
			return ExitOK
		}
		<-ctx.Done()
		return ExitInterrupted
	})
	os.Exit(code)
}

func startSignalHelper(t *testing.T, mode string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestSignalHelperProcess$")
	cmd.Env = append(os.Environ(), "LU_CLEANER_SIGNAL_HELPER="+mode)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	if line, err := bufio.NewReader(out).ReadString('\n'); err != nil || strings.TrimSpace(line) != "ready" {
		t.Fatalf("helper: %q %v", line, err)
	}
	return cmd
}

func waitExit(t *testing.T, cmd *exec.Cmd, timeout time.Duration) syscall.WaitStatus {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(timeout):
		t.Fatalf("process still running after %s", timeout)
	}
	return cmd.ProcessState.Sys().(syscall.WaitStatus)
}

func TestSignals(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns processes")
	}
	t.Run("second Ctrl-C kills a stuck run", func(t *testing.T) {
		cmd := startSignalHelper(t, "stuck")
		_ = cmd.Process.Signal(os.Interrupt)
		time.Sleep(300 * time.Millisecond)
		_ = cmd.Process.Signal(os.Interrupt)
		ws := waitExit(t, cmd, 5*time.Second)
		if !ws.Signaled() || ws.Signal() != syscall.SIGINT {
			t.Fatalf("want death by SIGINT, got %v", ws)
		}
	})
	t.Run("SIGTERM exits 143", func(t *testing.T) {
		cmd := startSignalHelper(t, "cancel")
		_ = cmd.Process.Signal(syscall.SIGTERM)
		if ws := waitExit(t, cmd, 5*time.Second); ws.ExitStatus() != ExitTerminated {
			t.Fatalf("exit status %d, want %d", ws.ExitStatus(), ExitTerminated)
		}
	})
	t.Run("SIGINT exits 130", func(t *testing.T) {
		cmd := startSignalHelper(t, "cancel")
		_ = cmd.Process.Signal(os.Interrupt)
		if ws := waitExit(t, cmd, 5*time.Second); ws.ExitStatus() != ExitInterrupted {
			t.Fatalf("exit status %d, want %d", ws.ExitStatus(), ExitInterrupted)
		}
	})
}

// ---------------------------------------------- category-totals-overlap

func TestCategoryTotalsOverlap(t *testing.T) {
	h := newHarness(t)
	wt := h.mkItem("wt", core.CatWorktrees, "codex-worktree", 4*gb, core.RiskModerate, 60*day)
	wt.Method = core.MethodWorktree
	wt.Path = filepath.Join(h.home, ".codex", "worktrees", "1", "app")
	nested := h.mkItem("wt-nm", core.CatArtifacts, "node_modules", 1*gb, core.RiskModerate, 60*day)
	nested.Path = filepath.Join(wt.Path, "node_modules")
	alone := h.mkItem("app", core.CatArtifacts, "node_modules", 2*gb, core.RiskModerate, 60*day)
	h.provs = []core.Provider{
		&fakeProvider{id: "worktrees", cats: []core.Category{core.CatWorktrees}, items: []*core.Item{wt}},
		&fakeProvider{id: "artifacts", cats: []core.Category{core.CatArtifacts}, items: []*core.Item{nested, alone}},
	}
	h.run("scan", "--json")
	var r struct {
		Totals struct {
			Size       int64            `json:"size"`
			ByCategory map[string]int64 `json:"by_category"`
			Shared     map[string]int64 `json:"shared_by_category"`
		} `json:"totals"`
	}
	if err := json.Unmarshal(h.out.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	tot := r.Totals
	if tot.ByCategory["artifacts"] != 3*gb || tot.ByCategory["worktrees"] != 4*gb || tot.Shared["artifacts"] != 1*gb ||
		len(tot.Shared) != 1 || tot.Size != 6*gb {
		t.Fatalf("totals %+v", tot)
	}
	var sum int64
	for id, n := range tot.ByCategory {
		sum += n - tot.Shared[id]
	}
	if sum != tot.Size {
		t.Fatalf("categories minus shared = %d, total %d", sum, tot.Size)
	}

	h.run("scan")
	if out := h.out.String(); !strings.Contains(out, "1.00 GB inside Worktrees") || !strings.Contains(out, "Category sizes overlap") {
		t.Fatalf("scan:\n%s", out)
	}
	h.run("scan", "--summary")
	if out := h.out.String(); !strings.Contains(out, "SHARED") || !strings.Contains(out, "1.00 GB inside Worktrees") {
		t.Fatalf("scan --summary:\n%s", out)
	}
	h.run("doctor", "--json")
	var d doctorReport
	if err := json.Unmarshal(h.out.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	for _, c := range d.Categories {
		if c.ID == "artifacts" && c.Shared != 1*gb {
			t.Fatalf("doctor category %+v", c)
		}
	}
}
