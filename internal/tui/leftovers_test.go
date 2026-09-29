package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ludwig-pro/lu-cleaner/internal/clean"
	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
	"github.com/ludwig-pro/lu-cleaner/internal/safety"
)

// ------------------------------------------------------------------ picker: wording and details (T1, T3)

// Reduced "really freed" sizes come from hardlinks and APFS clones alike.
func TestPickerReclaimWording(t *testing.T) {
	it := mkItem("store", core.CatJS, 900e6, core.RiskSafe, 30*day)
	it.SetReclaim(100e6)
	m := newTestPicker(t, PickerOptions{Providers: []core.Provider{&fakeProvider{id: "p", cats: []core.Category{core.CatJS}, items: []*core.Item{it}}}, Flat: true})
	d := newDriver(t, m)
	d.until("scan done", scanDone(m))
	d.send(teaSize(180, 50))
	v := m.View()
	if !strings.Contains(v, "shared with other files (hardlinks or APFS clones)") || strings.Contains(v, "hardlinked elsewhere") {
		t.Fatalf("details:\n%s", v)
	}
}

// The Meta keys explaining a report-only or partly kept item get their own
// line (sanitized), the generic Meta line keeps the others; badges show the
// worktree state (dirty is a count, "?" when unknown).
func TestPickerMetaDetailsAndBadges(t *testing.T) {
	it := mkItem("wt", core.CatWorktrees, 5e6, core.RiskCaution, 30*day)
	it.Meta = map[string]string{
		"unreadable":  "git metadata unreadable (permission denied)\x1b]0;x\x07",
		"repair_main": "/nonexistent/moved\u202erepo",
		"nested":      "/nonexistent/wt/vendor/lib",
		"kept":        "cur123",
		"kept_reason": "in use\x1b[2J",
		"dirty":       "3",
		"branch":      "feat",
	}
	it.Warn = "warn\u009b31m"
	it.Note = "note\x07"
	m := newTestPicker(t, PickerOptions{Providers: []core.Provider{&fakeProvider{id: "p", cats: []core.Category{core.CatWorktrees}, items: []*core.Item{it}}}, Flat: true})
	d := newDriver(t, m)
	d.until("scan done", scanDone(m))
	d.send(teaSize(220, 60))
	lines := strings.Split(m.viewDetails(220, 20), "\n")
	find := func(label string) string {
		for _, l := range lines {
			if strings.HasPrefix(strings.TrimSpace(ansiStrip(l)), label) {
				return ansiStrip(l)
			}
		}
		t.Fatalf("no %q line in details:\n%s", label, strings.Join(lines, "\n"))
		return ""
	}
	if l := find("Access"); !strings.Contains(l, `permission denied)\x1b]0;x\a`) {
		t.Fatalf("Access line %q", l)
	}
	if l := find("Repair"); !strings.Contains(l, `moved\u202erepo`) {
		t.Fatalf("Repair line %q", l)
	}
	if l := find("Nested"); !strings.Contains(l, "/nonexistent/wt/vendor/lib") {
		t.Fatalf("Nested line %q", l)
	}
	if l := find("Kept"); !strings.Contains(l, `cur123 (in use\x1b[2J)`) {
		t.Fatalf("Kept line %q", l)
	}
	if l := find("Meta"); strings.Contains(l, "kept") || strings.Contains(l, "nested") || !strings.Contains(l, "branch=feat") {
		t.Fatalf("Meta line %q", l)
	}
	if v := m.View(); hasRawControl(v) {
		t.Fatalf("raw control sequence: %q", v)
	}
	badges := strings.Join(metaBadges(it), ",")
	for _, want := range []string{"unreadable", "moved repo", "nested", "dirty 3", "⎇ feat"} {
		if !strings.Contains(badges, want) {
			t.Errorf("badges %q lack %q", badges, want)
		}
	}
	unknown := &core.Item{Meta: map[string]string{"dirty": "?"}, RequireForce: true}
	if got := strings.Join(metaBadges(unknown), ","); got != "force,dirty?" {
		t.Errorf("badges = %q", got)
	}
	if got := metaBadges(&core.Item{Meta: map[string]string{"dirty": "0"}}); len(got) != 0 {
		t.Errorf("clean worktree badges = %v", got)
	}
}

// ansiStrip removes SGR sequences (lipgloss styles) from a rendered line.
func ansiStrip(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
				j++
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// ------------------------------------------------------------------ picker: RequireForce (T4)

func forceFixture(t *testing.T, force bool) (*pickerModel, *driver, *[]string) {
	t.Helper()
	orphan := mkItem("orphan", core.CatWorktrees, 800e6, core.RiskModerate, 90*day)
	orphan.RequireForce = true
	orphan.Recommended = true // even a provider recommendation must not preselect it
	orphan.Warn = "orphaned worktree: its git data is gone"
	junk := mkItem("junk", core.CatWorktrees, 100e6, core.RiskSafe, 90*day)
	prov := &fakeProvider{id: "p", cats: []core.Category{core.CatWorktrees}, items: []*core.Item{orphan, junk}}
	env := testEnv(tempHome(t))
	opts := testCleanOpts(env.Home)
	opts.Force = force
	m := newTestPicker(t, PickerOptions{Env: env, Providers: []core.Provider{prov}, Flat: true, Smart: true, Clean: opts})
	var got []string
	m.cleanFn = func(ctx context.Context, items []*core.Item, opt clean.Options, progress func(clean.Result)) *clean.Summary {
		got = ids(items)
		return &clean.Summary{DryRun: opt.DryRun, Trash: opt.Trash}
	}
	d := newDriver(t, m)
	d.until("scan done", scanDone(m))
	return m, d, &got
}

func (m *pickerModel) focusItem(t *testing.T, id string) *core.Item {
	t.Helper()
	m.refresh()
	for i, it := range m.list {
		if it.ID == id {
			m.cursor, m.cursorID = i, id
			return it
		}
	}
	t.Fatalf("no item %q in the list", id)
	return nil
}

// Without --force an item that needs it is shown but cannot be selected nor
// counted, with the reason in the details; Trash mode lets it through.
func TestPickerRequireForceNotCleanable(t *testing.T) {
	m, d, got := forceFixture(t, false)
	if s := selectedIDs(m); s != "junk" {
		t.Fatalf("smart selection = %q", s)
	}
	if m.visTotal != 100e6 {
		t.Fatalf("reclaimable total %d counts the item needing --force", m.visTotal)
	}
	m.focusItem(t, "orphan")
	d.keys("space")
	if m.selected["orphan"] || !strings.Contains(m.status, "needs --force") || !strings.Contains(m.status, "its git data is gone") {
		t.Fatalf("space on a --force item: selected=%v status %q", m.selected["orphan"], m.status)
	}
	d.keys("A")
	if s := selectedIDs(m); s != "junk" {
		t.Fatalf("select all = %q", s)
	}
	m.focusItem(t, "orphan")
	cols := m.itemCols(120)
	if l := ansiStrip(m.itemLine(m.list[m.cursor], true, cols)); !strings.Contains(l, "[·]") {
		t.Fatalf("item line %q", l)
	}
	if det := ansiStrip(m.viewDetails(160, 20)); !strings.Contains(det, "Blocked") || !strings.Contains(det, "needs --force: orphaned worktree") {
		t.Fatalf("details:\n%s", det)
	}
	d.keys("d", "y")
	d.until("summary", func() bool { return m.mode == modeSummary })
	if strings.Join(*got, ",") != "junk" {
		t.Fatalf("executor received %v", *got)
	}
	d.keys("x")

	// Trash mode: a recoverable move is allowed, and typing yes is required.
	d.keys("t")
	m.focusItem(t, "orphan")
	d.keys("space")
	if !m.selected["orphan"] || selectedIDs(m) != "orphan,junk" && selectedIDs(m) != "junk,orphan" {
		t.Fatalf("Trash mode selection = %q (status %q)", selectedIDs(m), m.status)
	}
	if det := ansiStrip(m.viewDetails(160, 20)); !strings.Contains(det, "Trash mode only") {
		t.Fatalf("details:\n%s", det)
	}
	// back to delete mode: the item is held back, and the status says so
	d.keys("t")
	if s := selectedIDs(m); s != "junk" || !strings.Contains(m.status, "need --force") {
		t.Fatalf("delete mode: selection %q status %q", s, m.status)
	}
	d.keys("t", "d")
	if m.confirm == nil || m.confirm.caution == 0 || len(m.confirm.forced) != 1 || !strings.Contains(ansiStrip(m.View()), "moved to the Trash only") {
		t.Fatalf("Trash confirm = %+v\n%s", m.confirm, m.View())
	}
	d.keys("y", "enter")
	if m.mode != modeConfirm {
		t.Fatal("an item needing --force was confirmed without typing yes")
	}
	d.keys("ctrl+u")
	d.typeText("yes")
	d.keys("enter")
	d.until("summary", func() bool { return m.mode == modeSummary })
	if strings.Join(*got, ",") != "orphan,junk" && strings.Join(*got, ",") != "junk,orphan" {
		t.Fatalf("executor received %v", *got)
	}
}

// With --force the item can be picked by hand (never preselected), and the
// dialog says it is cleaned only because of --force.
func TestPickerRequireForceWithForce(t *testing.T) {
	m, d, got := forceFixture(t, true)
	if s := selectedIDs(m); s != "junk" {
		t.Fatalf("smart selection = %q (an item needing --force is never preselected)", s)
	}
	d.keys("a")
	if s := selectedIDs(m); s != "junk" {
		t.Fatalf("smart select key = %q", s)
	}
	if m.visTotal != 900e6 {
		t.Fatalf("reclaimable total = %d", m.visTotal)
	}
	m.focusItem(t, "orphan")
	if det := ansiStrip(m.viewDetails(160, 20)); !strings.Contains(det, "cleaned only because of --force") {
		t.Fatalf("details:\n%s", det)
	}
	d.keys("space", "d")
	if m.confirm == nil || m.confirm.caution == 0 || !strings.Contains(ansiStrip(m.View()), "cleaned only because of --force") {
		t.Fatalf("confirm = %+v\n%s", m.confirm, m.View())
	}
	d.typeText("yes")
	d.keys("enter")
	d.until("summary", func() bool { return m.mode == modeSummary })
	if len(*got) != 2 {
		t.Fatalf("executor received %v", *got)
	}
}

// ------------------------------------------------------------------ analyzer: git data and nested repositories (T2)

// Nothing inside a .git directory or a bare repository, and no checkout's
// .git file, is ever deleted by the analyzer.
func TestAnalyzerNeverDeletesGitData(t *testing.T) {
	home := tempHome(t)
	repo := mkdirFiles(t, filepath.Join(home, "code", "proj"), map[string]int{
		".git/HEAD": 10, ".git/objects/pack/p.pack": 4000, ".git/refs/heads/main": 10, "src/a.go": 10,
	})
	fakeWorktree(t, filepath.Join(home, "code", "main"), filepath.Join(home, "code", "wt"))
	bare := mkdirFiles(t, filepath.Join(home, "repos", "app.git"), map[string]int{"HEAD": 10, "objects/x": 4000, "refs/heads/main": 10, "hooks/pre-commit": 10})

	for _, c := range []struct{ root, entry string }{
		{filepath.Join(repo, ".git"), "objects"},
		{filepath.Join(repo, ".git"), "HEAD"},
		{filepath.Join(repo, ".git", "objects"), "pack"},
		{bare, "hooks"},
		{bare, "objects"},
		{filepath.Join(home, "code", "wt"), ".git"},
	} {
		m := newTestAnalyzer(t, c.root, home)
		d := newDriver(t, m)
		d.until("sizes", settled(m))
		e := m.focusName(t, c.entry)
		if e.refusal() == "" || e.tag == "" {
			t.Fatalf("%s/%s: refusal %q tag %q", c.root, c.entry, e.refusal(), e.tag)
		}
		d.keys("space")
		if len(m.marked) != 0 || !strings.Contains(m.status, "never deletes") {
			t.Fatalf("%s/%s: marks %d status %q", c.root, c.entry, len(m.marked), m.status)
		}
		m.focusName(t, c.entry)
		d.keys("d")
		if m.mode != anBrowse || !strings.Contains(m.status, "never deletes") {
			t.Fatalf("%s/%s: mode %v status %q", c.root, c.entry, m.mode, m.status)
		}
		// handed to the executor anyway, it is report-only
		if it := m.itemsFor([]*anEntry{e})[0]; it.CanClean() {
			t.Fatalf("%s/%s: item is cleanable", c.root, c.entry)
		}
		mustExist(t, e.path)
	}
	// the working tree of a repository stays deletable
	m := newTestAnalyzer(t, repo, home)
	d := newDriver(t, m)
	d.until("sizes", settled(m))
	if e := m.focusName(t, "src"); e.refusal() != "" {
		t.Fatalf("src refused: %q", e.refusal())
	}
	deleteEntry(t, d, m, "src")
	if m.result.Count(clean.StatusDone) != 1 {
		t.Fatalf("results %+v", m.result.Results)
	}
	mustExist(t, filepath.Join(repo, ".git", "HEAD"))
}

// A plain folder holding a repository or a linked worktree is left out of
// the dialog after the background search; the other entries are deleted.
func TestAnalyzerRefusesFolderHoldingRepositories(t *testing.T) {
	home := tempHome(t)
	root := filepath.Join(home, "stuff")
	mkdirFiles(t, root, map[string]int{
		"old/projA/.git/HEAD": 10, "old/projA/main.go": 4000,
		"junk/a.bin":                      8000,
		"deps/node_modules/pkg/.git/HEAD": 10, "deps/node_modules/pkg/i.js": 10, // regenerable folders are not searched
	})
	fakeWorktree(t, filepath.Join(home, "main"), filepath.Join(root, "wts", "feature"))
	m := newTestAnalyzer(t, root, home)
	d := newDriver(t, m)
	d.until("sizes", settled(m))

	// one folder holding a repository: refused, nothing deleted
	m.focusName(t, "old")
	d.keys("d")
	if m.mode != anConfirm || !m.confChecking {
		t.Fatalf("mode %v checking %v", m.mode, m.confChecking)
	}
	d.until("check", func() bool { return m.mode != anConfirm || !m.confChecking })
	if m.mode != anBrowse || !strings.Contains(m.status, "contains the git repository") || !strings.Contains(m.status, "projA") {
		t.Fatalf("mode %v status %q", m.mode, m.status)
	}
	mustExist(t, filepath.Join(root, "old", "projA", "main.go"))

	// a folder holding a registered worktree too; the others go
	for _, n := range []string{"old", "wts", "junk", "deps"} {
		m.focusName(t, n)
		d.keys("space")
	}
	d.keys("d")
	d.until("check", func() bool { return !m.confChecking })
	if m.mode != anConfirm || len(m.confirm) != 2 || len(m.confRefused) != 2 {
		t.Fatalf("mode %v confirm %d refused %d", m.mode, len(m.confirm), len(m.confRefused))
	}
	v := ansiStrip(m.View())
	for _, s := range []string{"2 marked entries left out", "Delete 2 entries", "old: contains the git repository"} {
		if !strings.Contains(v, s) {
			t.Fatalf("dialog lacks %q:\n%s", s, v)
		}
	}
	if m.marked[filepath.Join(root, "old")] != nil || m.marked[filepath.Join(root, "wts")] != nil {
		t.Fatal("refused entries stay marked")
	}
	d.typeText("yes")
	d.keys("enter")
	d.until("result", func() bool { return m.mode == anResult })
	if m.result.Count(clean.StatusDone) != 2 {
		t.Fatalf("results %+v", m.result.Results)
	}
	mustExist(t, filepath.Join(root, "old", "projA", ".git", "HEAD"))
	mustExist(t, filepath.Join(root, "wts", "feature", ".git"))
	for _, n := range []string{"junk", "deps"} {
		if fsx.Exists(filepath.Join(root, n)) {
			t.Fatalf("%s not deleted", n)
		}
	}
}

// A "yes" typed while the search runs starts the deletion once it ends,
// unless an entry was left out: the dialog then asks again.
func TestAnalyzerYesDuringNestedCheck(t *testing.T) {
	home := tempHome(t)
	root := mkdirFiles(t, filepath.Join(home, "stuff"), map[string]int{"a/x": 4000, "b/x": 4000})
	m := newTestAnalyzer(t, root, home)
	gate := make(chan struct{})
	repoIn := ""
	m.nestedFn = func(dir string) (string, error) {
		<-gate
		if filepath.Base(dir) == repoIn {
			return filepath.Join(dir, "repo"), nil
		}
		return "", nil
	}
	d := newDriver(t, m)
	d.until("sizes", settled(m))

	// the search finds a repository in b: no deletion, yes asked again
	repoIn = "b"
	m.focusName(t, "a")
	d.keys("space")
	m.focusName(t, "b")
	d.keys("space", "d")
	d.typeText("yes")
	d.keys("enter")
	if m.mode != anConfirm || !m.confYes || !strings.Contains(m.hint, "starts when the check ends") {
		t.Fatalf("mode %v yes %v hint %q", m.mode, m.confYes, m.hint)
	}
	gate <- struct{}{}
	gate <- struct{}{}
	d.until("check", func() bool { return !m.confChecking })
	if m.mode != anConfirm || m.confYes || m.input.String() != "" || !strings.Contains(m.hint, "type yes again") || len(m.confirm) != 1 {
		t.Fatalf("mode %v yes %v input %q hint %q confirm %d", m.mode, m.confYes, m.input.String(), m.hint, len(m.confirm))
	}
	mustExist(t, filepath.Join(root, "a", "x"))
	d.keys("esc")

	// nothing found: the deletion starts by itself
	repoIn = ""
	close(gate)
	m.focusName(t, "a")
	d.keys("d")
	d.typeText("yes")
	d.keys("enter")
	d.until("result", func() bool { return m.mode == anResult })
	if m.result.Count(clean.StatusDone) != 1 || fsx.Exists(filepath.Join(root, "a")) {
		t.Fatalf("results %+v", m.result.Results)
	}
	mustExist(t, filepath.Join(root, "b", "x"))
}

// The executor re-checks each plain deletion right before it happens: a
// repository cloned into the folder after the dialog, git data, or a folder
// that cannot be inspected (unless --force) is refused.
func TestAnalyzerRecheckBeforeDeletion(t *testing.T) {
	home := tempHome(t)
	root := mkdirFiles(t, filepath.Join(home, "stuff"), map[string]int{"late/x": 4000, "locked/in/x": 10, "fine/x": 10})
	m := newTestAnalyzer(t, root, home)
	d := newDriver(t, m)
	d.until("sizes", settled(m))
	byName := m.cur().byName
	items := m.itemsFor([]*anEntry{byName["late"], byName["locked"], byName["fine"]})
	for _, it := range items {
		if it.Recheck == nil {
			t.Fatalf("%s: no recheck", it.Name)
		}
	}
	mkdirFiles(t, filepath.Join(root, "late", "clone", ".git"), map[string]int{"HEAD": 10})
	lockedIn := filepath.Join(root, "locked", "in")
	if err := os.Chmod(lockedIn, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(lockedIn, 0o755) })
	if _, err := os.ReadDir(lockedIn); err == nil {
		t.Skip("running with privileges that ignore permissions")
	}
	sum := clean.Run(context.Background(), items, m.opt.Clean, nil)
	msgs := map[string]string{}
	for _, r := range sum.Results {
		msgs[r.Item.Name] = r.Status.String() + ": " + r.Message
	}
	if !strings.Contains(msgs["late"], "contains the git repository") || !strings.Contains(msgs["locked"], "cannot check it for git repositories") ||
		!strings.HasPrefix(msgs["fine"], clean.StatusDone.String()) {
		t.Fatalf("results %v", msgs)
	}
	mustExist(t, filepath.Join(root, "late", "clone", ".git", "HEAD"))
	mustExist(t, lockedIn)

	// --force skips a check that cannot complete, never a repository found
	force := func(dir string) error { return recheckPlain(dir, true, true, nil) }
	if err := force(filepath.Join(root, "locked")); err != nil {
		t.Fatalf("--force: %v", err)
	}
	if err := force(filepath.Join(root, "late")); err == nil {
		t.Fatal("--force let a folder holding a repository through")
	}
	if err := recheckPlain(filepath.Join(root, "late", "clone", ".git", "HEAD"), false, true, nil); err == nil || !strings.Contains(err.Error(), "git data") {
		t.Fatalf("file inside .git: %v", err)
	}
}

// Without --force a folder that cannot be inspected is left out of the
// dialog; with --force it stays, flagged as not checked.
func TestAnalyzerNestedCheckErrors(t *testing.T) {
	for _, force := range []bool{false, true} {
		home := tempHome(t)
		root := mkdirFiles(t, filepath.Join(home, "stuff"), map[string]int{"big/x": 10})
		opts := testCleanOpts(home)
		opts.Force = force
		opts.DryRun = true
		m := newAnalyzer(context.Background(), AnalyzeOptions{Env: testEnv(home), Root: root, Clean: opts})
		t.Cleanup(m.cancel)
		m.diskFn = fakeDisk
		m.w, m.h = 120, 40
		m.nestedFn = func(string) (string, error) { return "", safety.ErrTooLarge }
		d := newDriver(t, m)
		d.until("sizes", settled(m))
		m.focusName(t, "big")
		d.keys("d")
		d.until("check", func() bool { return !m.confChecking })
		if !force {
			if m.mode != anBrowse || !strings.Contains(m.status, "--force skips this check") {
				t.Fatalf("mode %v status %q", m.mode, m.status)
			}
			continue
		}
		if m.mode != anConfirm || !strings.Contains(ansiStrip(m.View()), "not checked for git repositories inside (--force)") {
			t.Fatalf("--force dialog:\n%s", m.View())
		}
		d.typeText("yes")
		d.keys("enter")
		d.until("result", func() bool { return m.mode == anResult })
		if m.result.Count(clean.StatusDryRun) != 1 {
			t.Fatalf("results %+v", m.result.Results)
		}
	}
}

func TestNestedRepoIn(t *testing.T) {
	home := tempHome(t)
	root := mkdirFiles(t, filepath.Join(home, "d"), map[string]int{
		"node_modules/p/.git/HEAD": 1, ".build/checkouts/x/.git/HEAD": 1, "a/b/c.txt": 1,
	})
	if got, err := nestedRepoIn(root); got != "" || err != nil {
		t.Fatalf("got %q, %v", got, err)
	}
	fakeWorktree(t, filepath.Join(home, "main"), filepath.Join(root, "a", "b", "wt"))
	if got, err := nestedRepoIn(root); got != filepath.Join(root, "a", "b", "wt") || err != nil {
		t.Fatalf("worktree: got %q, %v", got, err)
	}
	if _, err := nestedRepoIn(filepath.Join(home, "missing")); err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing dir: %v", err)
	}
}
