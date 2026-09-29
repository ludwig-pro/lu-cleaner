package cli

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ludwig-pro/lu-cleaner/internal/clean"
	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/providers/artifacts"
	"github.com/ludwig-pro/lu-cleaner/internal/providers/catalog"
)

// ---------------------------------------------- L1 disabled_categories

func TestDisabledCategoriesAcceptToolsAndWarnOnUnknown(t *testing.T) {
	h := fixture(t)
	h.cfg.DisabledCategories = []string{"docker", "nope", "ai", " ", "claude"}
	c := newCLI(h.app)
	s, err := c.newSetup(nil)
	if err != nil {
		t.Fatalf("an unknown disabled category must not be fatal: %v", err)
	}
	// docker disables its whole category, claude is ai (not repeated).
	if want := []core.Category{core.CatContainers, core.CatAI}; !reflect.DeepEqual(s.disabled, want) {
		t.Fatalf("disabled = %v, want %v", s.disabled, want)
	}
	if e := h.errOut.String(); !strings.Contains(e, `warning: config disabled_categories: unknown category "nope"`) ||
		strings.Contains(e, "docker") || strings.Contains(e, "claude") {
		t.Fatalf("stderr %q", e)
	}

	// Commands keep working, with the warning, and honour the tool alias.
	if code := h.run("clean", "-y", "--smart", "-n"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut.String())
	}
	expectNames(t, h.lastClean(), "app-safe", "app-stale")
	if !strings.Contains(h.errOut.String(), `"nope"`) {
		t.Fatalf("stderr %q", h.errOut.String())
	}
	if code := h.run("clean", "-y", "-c", "containers"); code != ExitUsage || !strings.Contains(h.errOut.String(), "disabled") {
		t.Fatalf("disabled through a tool name: exit %d, %q", code, h.errOut.String())
	}
	if code := h.run("config", "show"); code != 0 || !strings.Contains(h.out.String(), "Disabled categories: containers, ai") {
		t.Fatalf("config show: exit %d\n%s", code, h.out.String())
	}
}

// ---------------------------------------------- L2 broad -k with --yes

func TestCleanYesKindNamingMultiCategoryProviderIsNotNarrowing(t *testing.T) {
	h := newHarness(t)
	js := h.mkItem("npm-cache", core.CatJS, "npm-cache", gb, core.RiskSafe, 30*day)
	js.Provider = "catalog"
	ai := h.mkItem("claude-logs", core.CatAI, "ai-claude-logs", gb, core.RiskSafe, 30*day)
	ai.Provider = "catalog"
	nm := h.mkItem("app", core.CatArtifacts, "node_modules", gb, core.RiskModerate, 60*day)
	nm.Provider = "artifacts"
	h.provs = []core.Provider{
		&fakeProvider{id: "catalog", cats: []core.Category{core.CatJS, core.CatAI}, items: []*core.Item{js, ai}},
		&fakeProvider{id: "artifacts", cats: []core.Category{core.CatArtifacts}, items: []*core.Item{nm}},
	}
	for _, args := range [][]string{
		{"clean", "-y", "-k", "catalog"},
		{"clean", "-y", "-k", "CATALOG", "-n"},
		{"clean", "-y", "-k", "node_modules,catalog"},
		{"-y", "-k", "catalog"},
		{"clean", "-y", "--smart", "--no-smart", "-k", "catalog"},
	} {
		if code := h.run(args...); code != ExitUsage {
			t.Errorf("%v: exit %d, want %d", args, code, ExitUsage)
		}
		if e := h.errOut.String(); !strings.Contains(e, "spans several categories (js, ai)") || !strings.Contains(e, "-c js") {
			t.Errorf("%v: stderr %q does not explain the refusal", args, e)
		}
	}
	if len(h.cleaned) != 0 {
		t.Fatalf("clean must not run, got %d calls", len(h.cleaned))
	}

	// Narrowed by something else: accepted.
	if code := h.run("clean", "-y", "-k", "catalog", "-c", "js", "-n"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut.String())
	}
	expectNames(t, h.lastClean(), "npm-cache")
	if code := h.run("clean", "-y", "-k", "catalog", "--smart", "-n"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut.String())
	}
	expectNames(t, h.lastClean(), "npm-cache", "claude-logs")
	// A single-category provider id is as narrow as its category.
	if code := h.run("clean", "-y", "-k", "artifacts", "-n"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut.String())
	}
	expectNames(t, h.lastClean(), "app")
	// Plain kinds are unchanged.
	if code := h.run("clean", "-y", "-k", "npm-cache", "-n"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut.String())
	}
	expectNames(t, h.lastClean(), "npm-cache")
}

// ---------------------------------------------- L5 / L6 wording

func TestReclaimWordingMentionsClones(t *testing.T) {
	h := newHarness(t)
	it := h.mkItem("bun-app", core.CatArtifacts, "node_modules", 2*gb, core.RiskModerate, 60*day)
	it.SetReclaim(gb)
	h.provs = []core.Provider{&fakeProvider{id: "artifacts", cats: []core.Category{core.CatArtifacts}, items: []*core.Item{it}}}
	h.run("scan")
	out := h.out.String()
	if !strings.Contains(out, "2.00 GB*") || !strings.Contains(out, "hardlinks or APFS clones") || strings.Contains(out, "shares hardlinks with") {
		t.Fatalf("scan:\n%s", out)
	}
	h.run("doctor", "--no-scan")
	if out := h.out.String(); !strings.Contains(out, "lu-cleaner detects both") || strings.Contains(out, "clones cannot be detected") {
		t.Fatalf("doctor:\n%s", out)
	}
}

func TestCatalogPageKeepLatestWording(t *testing.T) {
	page := catalogPage([]catalog.Entry{{
		ID: "ide-versions", Category: core.CatIDE, Name: "Old versions", Paths: []string{"~/x/*"},
		Risk: core.RiskModerate, Mode: catalog.Each, KeepLatest: 2,
	}})
	if !strings.Contains(page, "keeps at least the 2 newest (raised by keep_latest)") {
		t.Fatalf("catalog page:\n%s", page)
	}
}

// ---------------------------------------------- L7 details / sanitizing

func TestItemDetailsAreShownAndSanitized(t *testing.T) {
	h := newHarness(t)
	nested := []string{"a", "b", "c", "d"}
	for i, n := range nested {
		nested[i] = filepath.Join(h.home, "wt", "big", n)
	}
	big := h.mkItem("big", core.CatWorktrees, "codex-worktree", 3*gb, core.RiskCaution, 30*day)
	big.Method = core.MethodWorktree
	big.Warn = "contains another git repository or worktree: ~/wt/big/a, ~/wt/big/b, ~/wt/big/c"
	big.Meta = map[string]string{"tool": "codex", "branch": "main", "status": "clean", "nested": strings.Join(nested, ", ")}

	unread := h.mkItem("unread", core.CatWorktrees, "codex-worktree", 2*gb, core.RiskCaution, 30*day)
	unread.Method = core.MethodReport
	unread.Warn = "main repository unreadable (permission denied) — status unknown"
	unread.Meta = map[string]string{"tool": "codex", "status": "unknown", "unreadable": "main repository unreadable (permission denied)"}

	moved := h.mkItem("moved", core.CatWorktrees, "codex-worktree", 2*gb, core.RiskCaution, 30*day)
	moved.Method = core.MethodReport
	moved.Meta = map[string]string{"tool": "codex", "status": "unknown", "repair_main": filepath.Join(h.home, "new\x1b[2Jplace")}

	ver := h.mkItem("cursor-old", core.CatAI, "ai-versions", gb, core.RiskModerate, 30*day)
	ver.Meta = map[string]string{"kept": filepath.Join(h.home, ".cursor", "1.2\x1b]0;x\a"), "kept_reason": "newest"}
	ver.Warn = "in use\u202e by " + filepath.Join(h.home, "x")

	h.provs = []core.Provider{
		&fakeProvider{id: "worktrees", cats: []core.Category{core.CatWorktrees}, items: []*core.Item{big, unread, moved}},
		&fakeProvider{id: "aitools", cats: []core.Category{core.CatAI}, items: []*core.Item{ver}},
	}
	for _, args := range [][]string{{"scan"}, {"worktrees", "--list"}} {
		h.run(args...)
		out := h.out.String()
		assertNoControls(t, strings.Join(args, " "), out)
		for _, want := range []string{
			"contains: ~/wt/big/a, ~/wt/big/b, ~/wt/big/c, ~/wt/big/d", // the warning lists 3 of 4
			`main repository moved to ~/new\x1b[2Jplace`,
		} {
			if !strings.Contains(out, want) {
				t.Errorf("%v lacks %q:\n%s", args, want, out)
			}
		}
		// Already said by the warning: not repeated.
		if strings.Contains(out, "unreadable: ") {
			t.Errorf("%v repeats the warning:\n%s", args, out)
		}
		if !strings.Contains(out, "! main repository unreadable (permission denied)") {
			t.Errorf("%v lacks the warning:\n%s", args, out)
		}
	}
	h.run("scan")
	out := h.out.String()
	for _, want := range []string{`kept: ~/.cursor/1.2\x1b]0;x\a (newest)`, `! in use\u202e by ~/x`} {
		if !strings.Contains(out, want) {
			t.Errorf("scan lacks %q:\n%s", want, out)
		}
	}
}

func TestDetailLinesHomeless(t *testing.T) {
	it := &core.Item{Meta: map[string]string{"kept": "/a/b"}}
	if got := detailLines(&core.Env{}, it); !reflect.DeepEqual(got, []string{"kept: /a/b"}) {
		t.Fatalf("detailLines without a home = %q", got)
	}
	if got := prettyText(&core.Env{Home: "/Users/lu"}, "/Users/lu/x /Users/lucas/y"); got != "~/x /Users/lucas/y" {
		t.Fatalf("prettyText = %q", got)
	}
}

// ---------------------------------------------- L8 unknown sizes

func TestUnknownSizesShowQuestionMark(t *testing.T) {
	h := newHarness(t)
	prune := &core.Item{
		ID: "worktrees:prune:r", Provider: "worktrees", Category: core.CatWorktrees, Kind: "worktree-prune",
		Name: "repo · prune 2 stale worktree entries", Location: filepath.Join(h.home, "dev", "repo"),
		Risk: core.RiskSafe, Method: core.MethodCommand, Command: []string{"git", "worktree", "prune"},
		Selectable: true, AlwaysShow: true, Meta: map[string]string{"status": "prunable"},
	}
	pnpm := &core.Item{
		ID: "jsdev:pnpm-store-prune", Provider: "jsdev", Category: core.CatJS, Kind: "pnpm-store-prune",
		Name: "pnpm store prune", Location: filepath.Join(h.home, "Library", "pnpm", "store"),
		Risk: core.RiskSafe, Method: core.MethodCommand, Command: []string{"pnpm", "store", "prune"},
		Selectable: true, AlwaysShow: true, Meta: map[string]string{"size": "unknown: prune frees only unreferenced packages"},
	}
	real := h.mkItem("cache", core.CatJS, "npm-cache", 2*gb, core.RiskSafe, 30*day)
	h.provs = []core.Provider{
		&fakeProvider{id: "worktrees", cats: []core.Category{core.CatWorktrees}, items: []*core.Item{prune}},
		&fakeProvider{id: "jsdev", cats: []core.Category{core.CatJS}, items: []*core.Item{pnpm, real}},
	}
	for _, args := range [][]string{{"scan"}, {"worktrees", "--list"}, {"clean", "-y", "-c", "js", "-n"}} {
		h.run(args...)
		out := h.out.String()
		for _, l := range strings.Split(out, "\n") {
			if !strings.Contains(l, "prune") || !strings.Contains(l, "~/") { // table rows only
				continue
			}
			if strings.Contains(l, "0 B") || !strings.Contains(l, " ? ") {
				t.Errorf("%v: unknown size not shown as ?: %q\n%s", args, l, out)
			}
		}
	}
	h.run("scan")
	if out := h.out.String(); !strings.Contains(out, "2.00 GB") {
		t.Fatalf("measured sizes are unchanged:\n%s", out)
	}

	for _, tc := range []struct {
		it   *core.Item
		want string
	}{
		{prune, "?"},
		{pnpm, "?"},
		{&core.Item{Size: 5e6, Method: core.MethodCommand}, "5.00 MB"},
		{&core.Item{Method: core.MethodDelete}, "0 B"},
		{&core.Item{Method: core.MethodCommand, Sizing: true}, "…"},
		{&core.Item{Method: core.MethodDelete, Meta: map[string]string{"size": "unknown (timeout)"}}, "?"},
		{&core.Item{Size: 3e9, Reclaim: 1e9, Method: core.MethodDelete}, "3.00 GB*"},
	} {
		if got := sizeCell(tc.it); got != tc.want {
			t.Errorf("sizeCell(%+v) = %q, want %q", tc.it, got, tc.want)
		}
	}
}

// ---------------------------------------------- L3 history

func TestHistoryTotalsFreedAndShowsDetails(t *testing.T) {
	h := newHarness(t)
	h.history = []clean.HistoryEntry{
		// Shared with other files: only 1 GB of 3 GB freed.
		{Time: testNow, Name: "bun-app", Path: filepath.Join(h.home, "a"), Method: "delete", Status: "done", Size: 3 * gb, Freed: 1 * gb},
		// Moved to the Trash: not freed, totalled apart.
		{Time: testNow, Name: "binned", Path: filepath.Join(h.home, "b"), Method: "trash", Status: "done", Size: 5 * gb},
		// Failed after freeing part of it.
		{Time: testNow, Name: "half", Path: filepath.Join(h.home, "c"), Method: "delete", Status: "failed", Size: 4 * gb, Freed: 500 * mb,
			Error: "permission denied", Message: "partially removed: 500 MB freed"},
		// Failed with a message only, skipped with a message.
		{Time: testNow, Name: "busy", Path: filepath.Join(h.home, "d"), Method: "delete", Status: "failed", Size: gb,
			Message: "refused: Xcode is running"},
		{Time: testNow, Name: "wt", Path: filepath.Join(h.home, "wt"), Method: "worktree", Status: "skipped", Size: gb,
			Message: "needs --force: orphaned worktree"},
		// Group item without Location nor Paths, command of unknown gain.
		{Time: testNow, Name: "group", Count: 7, Method: "delete", Status: "done", Size: 10 * mb, Freed: 10 * mb},
		{Time: testNow, Name: "prune", Location: filepath.Join(h.home, "repo"), Method: "command", Status: "done"},
	}
	h.run("history")
	out := h.out.String()
	for _, want := range []string{
		"FREED",
		"Total freed: 1.51 GB over 3 cleaned items",
		"Moved to the Trash: 5.00 GB over 1 item",
		"! permission denied", "! partially removed: 500 MB freed",
		"! refused: Xcode is running",
		"! needs --force: orphaned worktree",
		"7 paths", "~/repo",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("history lacks %q:\n%s", want, out)
		}
	}
	for _, l := range strings.Split(out, "\n") {
		switch {
		case strings.Contains(l, "bun-app") && !strings.Contains(l, "3.00 GB  1.00 GB"):
			t.Errorf("SIZE and FREED of a shared deletion: %q", l)
		case strings.Contains(l, "binned") && !strings.Contains(l, "Trash"):
			t.Errorf("Trash move: %q", l)
		case strings.Contains(l, " prune ") && (!strings.Contains(l, " ? ") || strings.Contains(l, "0 B")):
			t.Errorf("unknown command gain: %q", l)
		}
	}

	h.run("history", "--json")
	var r struct{ Freed, Trashed int64 }
	if err := json.Unmarshal(h.out.Bytes(), &r); err != nil || r.Freed != 1*gb+500*mb+10*mb || r.Trashed != 5*gb {
		t.Fatalf("history --json: %v %+v", err, r)
	}
}

// ---------------------------------------------- L4 artifacts -t

func TestArtifactsRejectsUnknownTargets(t *testing.T) {
	h := fixture(t)
	for _, args := range [][]string{
		{"artifacts", "-t", "node_module"},
		{"artifacts", "-y", "-n", "-t", "node_modules,nope"},
		{"artifacts", "--list", "-t", "catalog"},
	} {
		if code := h.run(args...); code != ExitUsage {
			t.Errorf("%v: exit %d, want %d", args, code, ExitUsage)
		}
		if e := h.errOut.String(); !strings.Contains(e, "unknown artifact kind") || !strings.Contains(e, "ios-pods") {
			t.Errorf("%v: stderr %q", args, e)
		}
	}
	if len(h.cleaned) != 0 || len(h.pickers) != 0 {
		t.Fatal("nothing may run with an unknown kind")
	}
	// Kinds (any case) and aliases are accepted.
	for _, args := range [][]string{
		{"artifacts", "--list", "-t", "NODE_MODULES"},
		{"artifacts", "--list", "-t", "pods,cocoapods", "-t", "gradle-build", "-t", "xcode-build"},
		{"artifacts", "--list", "-t", "extra-artifact"},
	} {
		if code := h.run(args...); code != 0 {
			t.Errorf("%v: exit %d: %s", args, code, h.errOut.String())
		}
	}
	for _, k := range artifacts.Kinds() {
		if err := checkTargets([]string{k}); err != nil {
			t.Errorf("kind %q rejected: %v", k, err)
		}
	}
	for a := range kindAliases {
		if err := checkTargets([]string{a}); err != nil {
			t.Errorf("alias %q rejected: %v", a, err)
		}
	}
}
