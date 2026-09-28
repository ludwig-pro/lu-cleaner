package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/clean"
	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/engine"
)

func ids(items []*core.Item) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.ID
	}
	return out
}

func selectedIDs(m *pickerModel) string {
	return strings.Join(ids(m.selItems), ",")
}

func scanDone(m *pickerModel) func() bool { return func() bool { return !m.scanning } }

// standardProviders: artifacts (3 items, one nested hidden), AI tools (2), plus a report-only item.
func standardProviders() []core.Provider {
	nm := mkItem("nm", core.CatArtifacts, 300e6, core.RiskModerate, 60*day)
	pods := mkItem("pods", core.CatArtifacts, 200e6, core.RiskModerate, 2*day)
	build := mkItem("build", core.CatArtifacts, 500e6, core.RiskSafe, 5*day)
	empty := mkItem("empty", core.CatArtifacts, 0, core.RiskSafe, 0)
	empty.Selectable = false
	sessions := mkItem("sessions", core.CatAI, 50e6, core.RiskCaution, 90*day)
	sessions.Warn = "contains conversation history"
	aicache := mkItem("aicache", core.CatAI, 20e6, core.RiskSafe, 1*day)
	snap := mkItem("snapshots", core.CatSystem, 10e9, core.RiskNever, 0)
	snap.Method = core.MethodReport
	return []core.Provider{
		&fakeProvider{id: "artifacts", cats: []core.Category{core.CatArtifacts}, items: []*core.Item{nm, pods, build, empty}},
		&fakeProvider{id: "ai", cats: []core.Category{core.CatAI}, items: []*core.Item{sessions, aicache}},
		&fakeProvider{id: "system", cats: []core.Category{core.CatSystem}, items: []*core.Item{snap}},
	}
}

func TestPickerCategoriesAndTotals(t *testing.T) {
	m := newTestPicker(t, PickerOptions{Providers: standardProviders()})
	d := newDriver(t, m)
	d.until("scan done", scanDone(m))

	if len(m.cats) != 3 {
		t.Fatalf("cats = %d, want 3", len(m.cats))
	}
	// default order follows core.Categories: worktrees, artifacts, ..., ai, ..., system
	if m.cats[0].info.ID != core.CatArtifacts || m.cats[1].info.ID != core.CatAI || m.cats[2].info.ID != core.CatSystem {
		t.Fatalf("order = %v %v %v", m.cats[0].info.ID, m.cats[1].info.ID, m.cats[2].info.ID)
	}
	art := m.cats[0]
	if len(art.items) != 3 { // "empty" is hidden (size 0, not selectable)
		t.Fatalf("artifacts visible items = %v", ids(art.items))
	}
	if art.total != 1000e6 {
		t.Fatalf("artifacts total = %d", art.total)
	}
	// s sorts categories by size: report-only items (system snapshot) count
	// for nothing, so artifacts (1 GB) come first and system last.
	d.keys("s")
	if m.cats[0].info.ID != core.CatArtifacts || m.cats[2].info.ID != core.CatSystem {
		t.Fatalf("by size, first = %v", m.cats[0].info.ID)
	}
	// H shows everything
	d.keys("s", "H")
	if len(m.cats[0].items) != 4 {
		t.Fatalf("show all: artifacts items = %v", ids(m.cats[0].items))
	}
	if !strings.Contains(m.View(), "scan done") {
		t.Fatalf("header does not show scan done:\n%s", m.View())
	}
}

func TestPickerFilterAndSizing(t *testing.T) {
	sizing := mkItem("sizing", core.CatArtifacts, 0, core.RiskSafe, 0)
	sizing.Sizing = true
	small := mkItem("small", core.CatArtifacts, 10, core.RiskSafe, 0)
	big := mkItem("big", core.CatArtifacts, 5e6, core.RiskSafe, 0)
	other := mkItem("other", core.CatAI, 5e6, core.RiskSafe, 0)
	prov := &fakeProvider{id: "p", cats: []core.Category{core.CatArtifacts, core.CatAI}, items: []*core.Item{sizing, small, big, other}}
	m := newTestPicker(t, PickerOptions{
		Providers: []core.Provider{prov},
		Filter:    core.Filter{Categories: []core.Category{core.CatArtifacts}, MinSize: 1e6, MaxRisk: core.RiskNever},
		Flat:      true,
	})
	d := newDriver(t, m)
	d.until("scan done", scanDone(m))
	got := strings.Join(ids(m.list), ",")
	// sizing item is shown with "…" despite MinSize; small and other category are filtered
	if got != "big,sizing" {
		t.Fatalf("list = %s", got)
	}
	if !strings.Contains(m.View(), "…") {
		t.Fatal("sizing item should render …")
	}
	// the measured version below MinSize disappears
	done := sizing.Clone()
	done.Sizing, done.Size = false, 10
	d.send(scanBatchMsg{events: []engine.Event{{Item: done, Provider: "p"}}, closed: true})
	if got := strings.Join(ids(m.list), ","); got != "big" {
		t.Fatalf("after sizing: list = %s", got)
	}
}

func TestPickerSmartPreselectRespectsUserChoices(t *testing.T) {
	gate := make(chan struct{})
	a := mkItem("a", core.CatArtifacts, 100e6, core.RiskSafe, 1*day)      // recommended (safe, big)
	b := mkItem("b", core.CatArtifacts, 200e6, core.RiskSafe, 1*day)      // recommended, but user unselects it
	c := mkItem("c", core.CatArtifacts, 300e6, core.RiskModerate, 1*day)  // not stale: not recommended
	e := mkItem("e", core.CatArtifacts, 400e6, core.RiskModerate, 30*day) // stale: recommended
	f := mkItem("f", core.CatArtifacts, 500e6, core.RiskCaution, 300*day) // caution: never automatic
	g := mkItem("g", core.CatArtifacts, 600e6, core.RiskModerate, 1*day)  // user selects it during scan
	prov := &fakeProvider{id: "p", cats: []core.Category{core.CatArtifacts}, items: []*core.Item{a, b, c, e, f, g}, gate: gate}
	m := newTestPicker(t, PickerOptions{Providers: []core.Provider{prov}, Smart: true, StaleAfter: 14 * day, Flat: true})
	d := newDriver(t, m)
	d.until("items listed", func() bool { return len(m.list) == 6 })
	if !m.scanning {
		t.Fatal("should still be scanning")
	}
	// sort by name for predictable cursor positions: a b c e f g
	d.keys("s", "s")
	if got := strings.Join(ids(m.list), ","); got != "a,b,c,e,f,g" {
		t.Fatalf("name order = %s", got)
	}
	d.keys("home", "j", "space", "space") // b: select then unselect -> touched
	d.keys("end", "space")                // g: selected by the user
	close(gate)
	d.until("scan done", scanDone(m))
	if got := selectedIDs(m); got != "a,e,g" {
		t.Fatalf("selected after smart = %q, want a,e,g", got)
	}
	// smart selection happens once only
	d.keys("home", "space") // unselect a
	m.scanFinished()
	if m.selected["a"] {
		t.Fatal("smart selection re-applied")
	}
}

func TestPickerSelectionKeys(t *testing.T) {
	m := newTestPicker(t, PickerOptions{Providers: standardProviders(), StaleAfter: 14 * day})
	d := newDriver(t, m)
	d.until("scan done", scanDone(m))

	// category overview: space selects all but caution items of the category
	d.keys("j", "space") // AI tools: sessions (caution) + aicache (safe)
	if got := selectedIDs(m); got != "aicache" {
		t.Fatalf("category space: %q", got)
	}
	if !strings.Contains(m.status, "caution") {
		t.Fatalf("status should mention skipped caution items: %q", m.status)
	}
	d.keys("space")
	if len(m.selItems) != 0 {
		t.Fatalf("second space should clear the category: %q", selectedIDs(m))
	}

	// report-only system item cannot be selected
	d.keys("G", "enter")
	if m.screen != scrItems || m.cat != core.CatSystem {
		t.Fatalf("did not open System: %v %v", m.screen, m.cat)
	}
	d.keys("space")
	if len(m.selItems) != 0 || !strings.Contains(m.status, "cannot be cleaned") {
		t.Fatalf("report-only selected? %q status=%q", selectedIDs(m), m.status)
	}

	// tab cycles to the first category (artifacts)
	d.keys("tab")
	if m.cat != core.CatArtifacts {
		t.Fatalf("tab -> %v", m.cat)
	}
	// list sorted by size: build(500) nm(300) pods(200)
	if got := strings.Join(ids(m.list), ","); got != "build,nm,pods" {
		t.Fatalf("list = %s", got)
	}
	d.keys("A")
	if got := selectedIDs(m); got != "nm,pods,build" {
		t.Fatalf("A: %q", got)
	}
	if m.selTotal != 1000e6 {
		t.Fatalf("selTotal = %d", m.selTotal)
	}
	d.keys("n")
	if len(m.selItems) != 0 {
		t.Fatalf("n: %q", selectedIDs(m))
	}
	d.keys("a") // smart: build (safe) + nm (moderate, 60d old); pods is recent
	if got := selectedIDs(m); got != "nm,build" {
		t.Fatalf("a: %q", got)
	}
	d.keys("i")
	if got := selectedIDs(m); got != "pods" {
		t.Fatalf("i: %q", got)
	}
	// shift+tab wraps to System, tab twice reaches AI tools
	d.keys("shift+tab")
	if m.cat != core.CatSystem {
		t.Fatalf("shift+tab -> %v", m.cat)
	}
	d.keys("tab", "tab")
	if m.cat != core.CatAI {
		t.Fatalf("tab tab -> %v", m.cat)
	}
	d.keys("home") // sessions (50 MB) is first by size
	if m.currentItem().ID != "sessions" {
		t.Fatalf("cursor on %s", m.currentItem().ID)
	}
	d.keys("space")
	if !strings.Contains(m.status, "contains conversation history") {
		t.Fatalf("caution status = %q", m.status)
	}
	// back to the overview keeps the selection, and per category totals are right
	d.keys("esc")
	if m.screen != scrCategories {
		t.Fatal("esc should go back")
	}
	for _, r := range m.cats {
		switch r.info.ID {
		case core.CatArtifacts:
			if r.selN != 1 || r.selTotal != 200e6 {
				t.Fatalf("artifacts sel %d %d", r.selN, r.selTotal)
			}
		case core.CatAI:
			if r.selN != 1 || r.selTotal != 50e6 {
				t.Fatalf("ai sel %d %d", r.selN, r.selTotal)
			}
		}
	}
}

func TestPickerSortAndTextFilter(t *testing.T) {
	a := mkItem("alpha", core.CatArtifacts, 100, core.RiskSafe, 10*day)
	b := mkItem("bravo", core.CatArtifacts, 300, core.RiskSafe, 1*day)
	c := mkItem("charlie", core.CatArtifacts, 200, core.RiskSafe, 50*day)
	c.Project = "/src/react-app"
	m := newTestPicker(t, PickerOptions{Providers: []core.Provider{&fakeProvider{id: "p", items: []*core.Item{a, b, c}}}, Flat: true})
	d := newDriver(t, m)
	d.until("scan done", scanDone(m))
	order := func() string { return strings.Join(ids(m.list), ",") }
	if order() != "bravo,charlie,alpha" {
		t.Fatalf("size order %s", order())
	}
	d.keys("s")
	if order() != "charlie,alpha,bravo" {
		t.Fatalf("age order %s", order())
	}
	d.keys("s")
	if order() != "alpha,bravo,charlie" {
		t.Fatalf("name order %s", order())
	}
	// cursor follows the item across re-sorts
	d.keys("home", "j") // bravo
	d.keys("s")         // size: bravo first
	if m.currentItem().ID != "bravo" || m.cursor != 0 {
		t.Fatalf("cursor on %s at %d", m.currentItem().ID, m.cursor)
	}
	d.keys("/")
	d.typeText("react")
	if order() != "charlie" {
		t.Fatalf("filter react: %s", order())
	}
	// j is typed into the filter, not a movement
	d.typeText("j")
	if m.filter.String() != "reactj" || len(m.list) != 0 {
		t.Fatalf("filter = %q list=%s", m.filter.String(), order())
	}
	d.keys("backspace", "enter")
	if m.filter.active || order() != "charlie" {
		t.Fatalf("after enter: active=%v %s", m.filter.active, order())
	}
	if !strings.Contains(m.View(), `filter "react"`) {
		t.Fatal("breadcrumb should show the filter")
	}
	d.keys("/", "esc")
	if m.filter.String() != "" || len(m.list) != 3 {
		t.Fatalf("esc should clear the filter: %q %s", m.filter.String(), order())
	}
}

// cleanFixture creates real directories in a temp home.
type cleanFixture struct {
	home               string
	nm, nested, repo   string
	nmItem, nestedItem *core.Item
	repoItem, cmdItem  *core.Item
}

func newCleanFixture(t *testing.T) *cleanFixture {
	home := tempHome(t)
	f := &cleanFixture{home: home}
	app := filepath.Join(home, "src", "app")
	mkdirFiles(t, app, map[string]int{"package.json": 2})
	f.nm = mkdirFiles(t, filepath.Join(app, "node_modules"), map[string]int{"react/index.js": 5000, "lodash/index.js": 3000})
	f.nested = filepath.Join(f.nm, "react")
	f.repo = mkdirFiles(t, filepath.Join(home, "src", "repo"), map[string]int{".git/HEAD": 10, "main.go": 100})

	f.nmItem = &core.Item{ID: "nm", Category: core.CatArtifacts, Kind: "node_modules", Name: "node_modules", Path: f.nm,
		Size: 8192, Risk: core.RiskModerate, Method: core.MethodDelete, Selectable: true, RequireSibling: []string{"package.json"}}
	f.nestedItem = &core.Item{ID: "nested", Category: core.CatArtifacts, Kind: "pkg", Name: "react", Path: f.nested,
		Size: 4096, Risk: core.RiskSafe, Method: core.MethodDelete, Selectable: true}
	f.repoItem = &core.Item{ID: "repo", Category: core.CatArtifacts, Kind: "weird", Name: "repo", Path: f.repo,
		Size: 4096, Risk: core.RiskCaution, Method: core.MethodDelete, Selectable: true, Warn: "this is a repo"}
	return f
}

func (f *cleanFixture) picker(t *testing.T, items ...*core.Item) (*pickerModel, *driver) {
	env := testEnv(f.home)
	m := newTestPicker(t, PickerOptions{Env: env, Providers: []core.Provider{&fakeProvider{id: "p", items: items}}, Flat: true,
		Clean: testCleanOpts(f.home)})
	d := newDriver(t, m)
	d.until("scan done", scanDone(m))
	return m, d
}

func TestPickerConfirmGatingForCaution(t *testing.T) {
	f := newCleanFixture(t)
	m, d := f.picker(t, f.nmItem, f.repoItem)
	d.keys("A")
	if len(m.selItems) != 2 {
		t.Fatalf("selected %q", selectedIDs(m))
	}
	d.keys("d")
	if m.mode != modeConfirm || m.confirm.caution != 1 {
		t.Fatalf("mode=%v confirm=%+v", m.mode, m.confirm)
	}
	view := m.View()
	for _, want := range []string{"Clean 2 items", "caution", "Type yes", "delete permanently"} {
		if !strings.Contains(view, want) {
			t.Fatalf("confirm view lacks %q:\n%s", want, view)
		}
	}
	// y / enter alone do not confirm
	d.keys("y", "enter")
	if m.mode != modeConfirm || m.run != nil {
		t.Fatal("caution confirmation accepted without typing yes")
	}
	if !strings.Contains(m.confirm.hint, "type yes") {
		t.Fatalf("hint = %q", m.confirm.hint)
	}
	// esc cancels, nothing touched
	d.keys("esc")
	if m.mode != modeBrowse {
		t.Fatal("esc should cancel")
	}
	if _, err := os.Stat(f.nm); err != nil {
		t.Fatal("node_modules removed after cancel")
	}
	// without caution items: y confirms
	d.keys("G", "space") // unselect repo (sorted by size: nm 8192 first, repo second)
	if got := selectedIDs(m); got != "nm" {
		t.Fatalf("selected %q", got)
	}
	d.keys("d")
	if m.confirm == nil || m.confirm.caution != 0 || m.confirm.input.active {
		t.Fatalf("confirm = %+v", m.confirm)
	}
	d.keys("n")
	if m.mode != modeBrowse {
		t.Fatal("n should cancel")
	}
	// with the caution item again: typing yes works
	d.keys("space", "d")
	d.typeText("yes")
	d.keys("enter")
	if m.mode != modeCleaning && m.mode != modeSummary {
		t.Fatalf("mode after yes = %v", m.mode)
	}
	d.until("summary", func() bool { return m.mode == modeSummary })
}

func TestPickerCleanRemovesDoneItems(t *testing.T) {
	f := newCleanFixture(t)
	m, d := f.picker(t, f.nmItem, f.nestedItem, f.repoItem)
	d.keys("A", "d")
	d.typeText("yes")
	d.keys("enter")
	d.until("summary", func() bool { return m.mode == modeSummary })

	sum := m.lastSummary
	if sum == nil {
		t.Fatal("no summary")
	}
	// nested item is skipped by TopLevel: 2 results
	if len(sum.Results) != 2 || sum.Count(clean.StatusDone) != 1 || sum.Count(clean.StatusSkipped) != 1 {
		t.Fatalf("results = %+v", sum.Results)
	}
	if _, err := os.Stat(f.nm); !os.IsNotExist(err) {
		t.Fatal("node_modules still exists")
	}
	if _, err := os.Stat(filepath.Join(f.repo, "main.go")); err != nil {
		t.Fatal("the git repository was touched!")
	}
	view := m.View()
	for _, want := range []string{"Cleaning finished", "1 done", "1 skipped", "Estimated freed", "Measured freed", "blocked by safety guard"} {
		if !strings.Contains(view, want) {
			t.Fatalf("summary lacks %q:\n%s", want, view)
		}
	}
	d.keys("x") // any key returns to the list
	if m.mode != modeBrowse {
		t.Fatalf("mode = %v", m.mode)
	}
	if got := strings.Join(ids(m.list), ","); got != "repo" {
		t.Fatalf("remaining list = %s (nested and done items must disappear)", got)
	}
	if got := selectedIDs(m); got != "repo" {
		t.Fatalf("skipped item should stay selected: %q", got)
	}
	// a late upsert of a cleaned item is ignored
	d.send(scanBatchMsg{events: []engine.Event{{Item: f.nmItem.Clone(), Provider: "p"}}, closed: true})
	if m.items["nm"] != nil {
		t.Fatal("cleaned item came back")
	}
}

func TestPickerDryRunKeepsItemsAndTrashToggle(t *testing.T) {
	f := newCleanFixture(t)
	env := testEnv(f.home)
	opts := testCleanOpts(f.home)
	opts.DryRun = true
	m := newTestPicker(t, PickerOptions{Env: env, Providers: []core.Provider{&fakeProvider{id: "p", items: []*core.Item{f.nmItem}}}, Flat: true, Clean: opts})
	d := newDriver(t, m)
	d.until("scan done", scanDone(m))

	d.keys("t")
	if !m.trash || !m.cleanOptions().Trash || !strings.Contains(m.status, "NOT freed") {
		t.Fatalf("trash toggle: %v %q", m.trash, m.status)
	}
	if !strings.Contains(m.View(), "dry-run") {
		t.Fatal("footer should show dry-run")
	}
	d.keys("t")
	if m.trash {
		t.Fatal("t should toggle back")
	}
	d.keys("space", "d", "y")
	d.until("summary", func() bool { return m.mode == modeSummary })
	if m.lastSummary.Count(clean.StatusDryRun) != 1 {
		t.Fatalf("results = %+v", m.lastSummary.Results)
	}
	if !strings.Contains(m.View(), "nothing was touched") {
		t.Fatalf("dry-run summary:\n%s", m.View())
	}
	d.keys("enter")
	if len(m.list) != 1 {
		t.Fatal("dry-run item must stay listed")
	}
	if _, err := os.Stat(f.nm); err != nil {
		t.Fatal("dry-run deleted something")
	}
}

func TestPickerCtrlCCancelsCleaning(t *testing.T) {
	m := newTestPicker(t, PickerOptions{Providers: standardProviders(), Flat: true})
	started := make(chan struct{})
	m.cleanFn = func(ctx context.Context, items []*core.Item, opt clean.Options, progress func(clean.Result)) *clean.Summary {
		close(started)
		<-ctx.Done()
		sum := &clean.Summary{}
		for _, it := range items {
			r := clean.Result{Item: it, Status: clean.StatusSkipped, Message: "cancelled"}
			progress(r)
			sum.Results = append(sum.Results, r)
		}
		return sum
	}
	d := newDriver(t, m)
	d.until("scan done", scanDone(m))
	d.keys("home", "j", "space", "d", "y") // build (the first row is report-only)
	<-started
	if m.mode != modeCleaning {
		t.Fatalf("mode = %v", m.mode)
	}
	d.keys("q") // ignored while cleaning
	if d.quit || m.quitting {
		t.Fatal("q must not quit while cleaning")
	}
	d.keys("ctrl+c")
	if !m.run.cancelled {
		t.Fatal("ctrl+c should cancel")
	}
	d.until("summary", func() bool { return m.mode == modeSummary })
	if m.lastSummary.Count(clean.StatusSkipped) != 1 {
		t.Fatalf("summary %+v", m.lastSummary.Results)
	}
}

func TestPickerMeasuredHintAndTrashReminder(t *testing.T) {
	m := newTestPicker(t, PickerOptions{Providers: standardProviders(), Flat: true})
	m.cleanFn = func(ctx context.Context, items []*core.Item, opt clean.Options, progress func(clean.Result)) *clean.Summary {
		sum := &clean.Summary{Trash: opt.Trash, Took: time.Second}
		for _, it := range items {
			r := clean.Result{Item: it, Status: clean.StatusDone, Freed: it.Freed()}
			progress(r)
			sum.Results = append(sum.Results, r)
			sum.Estimated += r.Freed
		}
		sum.DiskBefore.Total, sum.DiskAfter.Total = 500e9, 500e9
		sum.DiskBefore.Free, sum.DiskAfter.Free = 10e9, 10e9+sum.Estimated/10
		sum.Measured = sum.Estimated / 10
		return sum
	}
	d := newDriver(t, m)
	d.until("scan done", scanDone(m))
	d.keys("home", "j", "space", "d", "y")
	d.until("summary", func() bool { return m.mode == modeSummary })
	if !strings.Contains(m.View(), "lu-cleaner doctor") {
		t.Fatalf("missing doctor hint:\n%s", m.View())
	}
	d.keys("enter", "t", "home", "j", "space", "d", "y")
	d.until("summary", func() bool { return m.mode == modeSummary })
	v := m.View()
	if !strings.Contains(v, "empty it") || strings.Contains(v, "lu-cleaner doctor") {
		t.Fatalf("trash summary:\n%s", v)
	}
}

func TestPickerProviderErrorsAndRunningCheck(t *testing.T) {
	it := mkItem("xcode", core.CatXcode, 1e9, core.RiskSafe, 0)
	it.ProcessGuard = []string{"Xcode"}
	it.Method = core.MethodCommand
	it.Command = []string{"xcrun", "simctl", "delete", "unavailable"}
	it.Path, it.Location = "", "simulators"
	p := &fakeProvider{id: "p", items: []*core.Item{it}}
	bad := &fakeProvider{id: "bad", err: fmt.Errorf("boom\nstack trace")}
	m := newTestPicker(t, PickerOptions{Providers: []core.Provider{p, bad}, Flat: true})
	m.runningFn = func(names ...string) []string { return names }
	d := newDriver(t, m)
	d.until("scan done", scanDone(m))
	if len(m.provErrs) != 1 || m.provErrs[0] != "bad: boom" {
		t.Fatalf("errors = %q", m.provErrs)
	}
	d.keys("space", "d")
	d.until("running check", func() bool { return !m.confirm.checking })
	v := m.View()
	for _, want := range []string{"xcrun simctl delete unavailable", "Xcode", "skipped"} {
		if !strings.Contains(v, want) {
			t.Fatalf("confirm lacks %q:\n%s", want, v)
		}
	}
	d.keys("esc", "?")
	if !strings.Contains(m.View(), "bad: boom") {
		t.Fatal("help should list provider errors")
	}
}

func TestPickerManyItems(t *testing.T) {
	var items []*core.Item
	for i := 0; i < 5000; i++ {
		cat := core.Categories[i%len(core.Categories)].ID
		items = append(items, mkItem(fmt.Sprintf("item-%05d", i), cat, int64(i+1)*1e6, core.Risk(i%3), time.Duration(i%400)*day))
	}
	m := newTestPicker(t, PickerOptions{Providers: []core.Provider{&fakeProvider{id: "p", items: items}}, Smart: true})
	d := newDriver(t, m)
	start := time.Now()
	d.until("scan done", scanDone(m))
	if len(m.visible) != 5000 {
		t.Fatalf("visible = %d", len(m.visible))
	}
	d.keys("enter")
	for i := 0; i < 300; i++ {
		d.keys("j")
	}
	d.keys("space", "pgdown", "G", "i", "tab", "A", "s")
	if el := time.Since(start); el > 5*time.Second {
		t.Fatalf("too slow: %v", el)
	}
	if d.views < 300 {
		t.Fatalf("views = %d", d.views)
	}
}

func TestPickerRenderSizes(t *testing.T) {
	long := mkItem(strings.Repeat("very-long-name-", 10), core.CatArtifacts, 123456789, core.RiskCaution, 400*day)
	long.Path = "/Users/someone/" + strings.Repeat("deep/", 30) + "node_modules"
	long.Warn = strings.Repeat("warning ", 20)
	long.Note = "note"
	long.Meta = map[string]string{"branch": "feature/x", "dirty": "true", "unpushed": "3"}
	long.Reclaim = 1000
	group := mkItem("group", core.CatJS, 1e9, core.RiskSafe, 3*day)
	group.Path = ""
	for i := 0; i < 20; i++ {
		group.Paths = append(group.Paths, fmt.Sprintf("/tmp/metro-%d", i))
	}
	provs := append(standardProviders(), &fakeProvider{id: "x", items: []*core.Item{long, group}})
	sizes := [][2]int{{80, 24}, {200, 60}, {60, 15}, {45, 12}, {30, 8}, {120, 33}}
	for _, sz := range sizes {
		t.Run(fmt.Sprintf("%dx%d", sz[0], sz[1]), func(t *testing.T) {
			m := newTestPicker(t, PickerOptions{Providers: provs, Smart: true})
			m.cleanFn = func(ctx context.Context, items []*core.Item, opt clean.Options, progress func(clean.Result)) *clean.Summary {
				sum := &clean.Summary{}
				for i, it := range items {
					st := clean.Status(i % 4)
					r := clean.Result{Item: it, Status: st, Message: strings.Repeat("msg ", 30), Error: "err"}
					progress(r)
					sum.Results = append(sum.Results, r)
				}
				return sum
			}
			d := newDriver(t, m)
			d.send(teaSize(sz[0], sz[1]))
			w, h := sz[0], sz[1]
			frame := func(name string) { checkFrame(t, name, m.View(), w, h) }
			frame("scanning")
			d.until("scan done", scanDone(m))
			frame("categories")
			d.keys("?")
			frame("help")
			d.keys("x", "enter")
			frame("items")
			d.keys("enter")
			frame("details")
			d.keys("/")
			d.typeText("very")
			frame("filter")
			d.keys("esc", "A", "d")
			frame("confirm")
			d.typeText("yes")
			d.keys("enter")
			d.until("summary", func() bool { return m.mode == modeSummary })
			frame("summary")
			d.keys("enter")
			frame("after")
		})
	}
}

func TestPickerKeyBurstsAndReveal(t *testing.T) {
	home := tempHome(t)
	real := mkdirFiles(t, filepath.Join(home, "cache"), map[string]int{"a": 10})
	var items []*core.Item
	for i := 0; i < 5; i++ {
		it := mkItem(fmt.Sprintf("i%d", i), core.CatJS, int64(100-i), core.RiskSafe, 0)
		it.Path = real
		items = append(items, it)
	}
	group := mkItem("group", core.CatJS, 1, core.RiskSafe, 0)
	group.Path, group.Paths = "", []string{real, "/nonexistent"}
	loc := mkItem("loc", core.CatJS, 0, core.RiskSafe, 0)
	loc.Path, loc.Location, loc.Method, loc.Command = "", "~/cache/…", core.MethodCommand, []string{"true"}
	items = append(items, group, loc)
	m := newTestPicker(t, PickerOptions{Env: testEnv(home), Providers: []core.Provider{&fakeProvider{id: "p", items: items}}, Flat: true})
	var revealed []string
	m.revealFn = func(p string) error { revealed = append(revealed, p); return nil }
	d := newDriver(t, m)
	d.until("scan done", scanDone(m))

	// a burst of runes delivered as one message is replayed key by key
	d.send(keyMsg("jjj"))
	if m.cursor != 3 {
		t.Fatalf("cursor = %d after jjj", m.cursor)
	}
	d.send(keyMsg("k o"))
	if m.cursor != 2 || len(m.selItems) != 1 || len(revealed) != 1 || revealed[0] != real {
		t.Fatalf("cursor %d sel %q revealed %q", m.cursor, selectedIDs(m), revealed)
	}
	d.keys("G", "o", "k", "o")
	if len(revealed) != 3 || revealed[1] != real || revealed[2] != real {
		t.Fatalf("revealed %q", revealed)
	}
}

func TestSafeCleanRecoversPanics(t *testing.T) {
	items := []*core.Item{mkItem("a", core.CatJS, 1, core.RiskSafe, 0), mkItem("b", core.CatJS, 1, core.RiskSafe, 0)}
	var got []clean.Result
	sum := safeClean(func(ctx context.Context, its []*core.Item, o clean.Options, progress func(clean.Result)) *clean.Summary {
		progress(clean.Result{Item: its[0], Status: clean.StatusDone})
		panic("boom")
	}, context.Background(), items, clean.Options{}, func(r clean.Result) { got = append(got, r) })
	if len(got) != 2 || got[1].Status != clean.StatusFailed || !strings.Contains(got[1].Error, "boom") {
		t.Fatalf("progress = %+v", got)
	}
	if sum == nil || sum.Count(clean.StatusFailed) != 2 {
		t.Fatalf("summary = %+v", sum)
	}
}
