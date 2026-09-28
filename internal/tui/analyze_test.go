package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ludwig-pro/lu-cleaner/internal/clean"
	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
)

type anFixture struct {
	home, root string
}

func newAnFixture(t *testing.T) *anFixture {
	home := tempHome(t)
	root := filepath.Join(home, "proj")
	mkdirFiles(t, root, map[string]int{
		"node_modules/a.js": 6000, "node_modules/b.js": 4000,
		"src/main.go":     100,
		"big.bin":         50000,
		".hidden/x":       10,
		"wt/file.txt":     5,
		"repo2/.git/HEAD": 10,
	})
	fakeWorktree(t, filepath.Join(home, "repo"), filepath.Join(root, "wt"))
	return &anFixture{home: home, root: root}
}

func newTestAnalyzer(t *testing.T, root, home string) *analyzeModel {
	t.Helper()
	m := newAnalyzer(context.Background(), AnalyzeOptions{Env: testEnv(home), Root: root, Clean: testCleanOpts(home)})
	t.Cleanup(m.cancel)
	m.diskFn = fakeDisk
	m.revealFn = func(string) error { return nil }
	m.w, m.h = 120, 40
	return m
}

func settled(m *analyzeModel) func() bool {
	return func() bool {
		d := m.cur()
		if d == nil || !d.loaded || len(m.pending) > 0 {
			return false
		}
		for _, e := range d.entries {
			if !e.sized {
				return false
			}
		}
		return true
	}
}

func (m *analyzeModel) focusName(t *testing.T, name string) *anEntry {
	t.Helper()
	d := m.cur()
	m.refreshView(d)
	for i, e := range d.view {
		if e.name == name {
			d.cursor, d.curName = i, name
			return e
		}
	}
	t.Fatalf("no entry %q in %s", name, d.path)
	return nil
}

func viewNames(m *analyzeModel) string {
	var out []string
	for _, e := range m.cur().view {
		out = append(out, e.name)
	}
	return strings.Join(out, ",")
}

func TestAnalyzerListsSizesAndTags(t *testing.T) {
	f := newAnFixture(t)
	m := newTestAnalyzer(t, f.root, f.home)
	d := newDriver(t, m)
	d.until("sizes", settled(m))

	dir := m.cur()
	if len(dir.entries) != 6 {
		t.Fatalf("entries = %d", len(dir.entries))
	}
	if first := dir.view[0].name; first != "big.bin" {
		t.Fatalf("largest first, got %s (%s)", first, viewNames(m))
	}
	for i := 1; i < len(dir.view); i++ {
		if dir.view[i].size > dir.view[i-1].size {
			t.Fatalf("not sorted by size: %s", viewNames(m))
		}
	}
	byName := dir.byName
	if !strings.Contains(byName["node_modules"].tag, "regenerable") {
		t.Fatalf("node_modules tag = %q", byName["node_modules"].tag)
	}
	if wt := byName["wt"]; !wt.isWorktree() || wt.git.main != filepath.Join(f.home, "repo") ||
		wt.git.common != filepath.Join(f.home, "repo", ".git") || !strings.Contains(wt.tag, "worktree") {
		t.Fatalf("wt = %+v", wt)
	}
	if byName["repo2"].git.kind != gitRepo || byName["repo2"].tag != "git repo" {
		t.Fatalf("repo2 = %+v", byName["repo2"])
	}
	nm, err := fsx.Size(context.Background(), filepath.Join(f.root, "node_modules"), nil)
	if err != nil || byName["node_modules"].size != nm.Bytes || byName["node_modules"].files != 2 {
		t.Fatalf("node_modules size %d files %d, want %d", byName["node_modules"].size, byName["node_modules"].files, nm.Bytes)
	}
	sum, _, complete, _ := m.total(dir)
	var want int64
	for _, e := range dir.entries {
		want += e.size
	}
	if !complete || sum != want {
		t.Fatalf("total %d complete=%v want %d", sum, complete, want)
	}
	v := m.View()
	for _, s := range []string{"Total", "node_modules/", "linked worktree", "big.bin", "%"} {
		if !strings.Contains(v, s) {
			t.Fatalf("view lacks %q:\n%s", s, v)
		}
	}
	// sort cycle and hidden files
	d.keys("s")
	if viewNames(m) != ".hidden,big.bin,node_modules,repo2,src,wt" {
		t.Fatalf("name sort: %s", viewNames(m))
	}
	d.keys(".")
	if strings.Contains(viewNames(m), ".hidden") {
		t.Fatal(". should hide dotfiles")
	}
	d.keys(".", "s")
	if m.sort != anByAge {
		t.Fatal("s should cycle to age")
	}
}

func TestAnalyzerNavigationUsesCache(t *testing.T) {
	f := newAnFixture(t)
	m := newTestAnalyzer(t, f.root, f.home)
	var lists atomic.Int32
	m.listFn = func(p string) tea.Msg { lists.Add(1); return listDir(p) }
	d := newDriver(t, m)
	d.until("sizes", settled(m))

	m.focusName(t, "node_modules")
	d.keys("enter")
	d.until("node_modules listed", settled(m))
	if m.cwd != filepath.Join(f.root, "node_modules") || viewNames(m) != "a.js,b.js" && viewNames(m) != "b.js,a.js" {
		t.Fatalf("cwd %s view %s", m.cwd, viewNames(m))
	}
	// header of the child reuses the size computed for it as a child
	d.keys("backspace")
	if m.cwd != f.root {
		t.Fatalf("cwd = %s", m.cwd)
	}
	if e := m.curEntry(); e == nil || e.name != "node_modules" {
		t.Fatalf("cursor should come back on node_modules, got %+v", e)
	}
	if n := lists.Load(); n != 2 {
		t.Fatalf("listed %d times, want 2 (going back must use the cache)", n)
	}
	// above the start directory: proj's aggregated size is reused
	d.keys("left")
	if m.cwd != f.home {
		t.Fatalf("cwd = %s", m.cwd)
	}
	d.until("home listed", func() bool { return m.cur().loaded })
	if m.pending[f.root] {
		t.Fatal("proj should not be measured again")
	}
	if e := m.cur().byName["proj"]; e == nil || !e.sized {
		t.Fatalf("proj entry = %+v", e)
	}
	if e := m.curEntry(); e == nil || e.name != "proj" {
		t.Fatalf("cursor should be on proj, got %+v", e)
	}
	// files cannot be entered
	d.keys("enter")
	d.until("proj", func() bool { return m.cwd == f.root })
	m.focusName(t, "big.bin")
	d.keys("enter")
	if m.cwd != f.root || !strings.Contains(m.status, "is a file") {
		t.Fatalf("entering a file: cwd %s status %q", m.cwd, m.status)
	}
}

func TestAnalyzerNotAboveRoot(t *testing.T) {
	m := newTestAnalyzer(t, "/", t.TempDir())
	// never touch the real filesystem here
	m.listFn = func(p string) tea.Msg {
		return dirListedMsg{path: p, entries: []*anEntry{{name: "Users", path: "/Users", isDir: true}, {name: "f", path: "/f", sized: true, size: 10}}}
	}
	m.pool.sizeFn = func(context.Context, string) (fsx.Stats, error) {
		return fsx.Stats{Bytes: 1234, Files: 3, Newest: testNow.Add(-3 * day)}, nil
	}
	d := newDriver(t, m)
	d.until("sized", settled(m))
	d.keys("left")
	if m.cwd != "/" || !strings.Contains(m.status, "Already at /") {
		t.Fatalf("cwd %s status %q", m.cwd, m.status)
	}
	if e := m.cur().byName["Users"]; e.size != 1234 || e.files != 3 {
		t.Fatalf("Users = %+v", e)
	}
}

func TestAnalyzerDeleteRequiresYes(t *testing.T) {
	f := newAnFixture(t)
	m := newTestAnalyzer(t, f.root, f.home)
	d := newDriver(t, m)
	d.until("sizes", settled(m))
	before, _, _, _ := m.total(m.cur())

	m.focusName(t, "node_modules")
	d.keys("space")
	m.focusName(t, "big.bin")
	d.keys("space")
	if len(m.marked) != 2 {
		t.Fatalf("marked = %d", len(m.marked))
	}
	d.keys("d")
	if m.mode != anConfirm || len(m.confirm) != 2 {
		t.Fatalf("mode %v confirm %d", m.mode, len(m.confirm))
	}
	if !strings.Contains(m.View(), "Type yes") {
		t.Fatalf("confirm view:\n%s", m.View())
	}
	d.keys("y", "enter")
	if m.mode != anConfirm || !strings.Contains(m.hint, "type yes") {
		t.Fatalf("y+enter must not confirm: mode %v hint %q", m.mode, m.hint)
	}
	d.keys("ctrl+u")
	d.typeText("YES")
	d.keys("enter")
	d.until("result", func() bool { return m.mode == anResult })
	if m.result.Count(clean.StatusDone) != 2 {
		t.Fatalf("results %+v", m.result.Results)
	}
	for _, p := range []string{"node_modules", "big.bin"} {
		if _, err := os.Lstat(filepath.Join(f.root, p)); !os.IsNotExist(err) {
			t.Fatalf("%s still exists", p)
		}
	}
	if !strings.Contains(m.View(), "2 deleted") {
		t.Fatalf("result view:\n%s", m.View())
	}
	d.keys("x")
	if m.mode != anBrowse || len(m.marked) != 0 {
		t.Fatalf("mode %v marked %d", m.mode, len(m.marked))
	}
	if strings.Contains(viewNames(m), "node_modules") || strings.Contains(viewNames(m), "big.bin") {
		t.Fatalf("deleted entries still listed: %s", viewNames(m))
	}
	after, _, _, _ := m.total(m.cur())
	if after >= before {
		t.Fatalf("total %d -> %d", before, after)
	}
}

func TestAnalyzerGuardBlocksProtectedPaths(t *testing.T) {
	home := tempHome(t)
	mkdirFiles(t, filepath.Join(home, ".ssh"), map[string]int{"id_ed25519": 100})
	mkdirFiles(t, filepath.Join(home, "junk"), map[string]int{"a": 100})
	m := newTestAnalyzer(t, home, home)
	d := newDriver(t, m)
	d.until("sizes", settled(m))

	// esc cancels
	m.focusName(t, "junk")
	d.keys("d", "esc")
	if m.mode != anBrowse || !fsx.Exists(filepath.Join(home, "junk")) {
		t.Fatal("esc should cancel without deleting")
	}
	m.focusName(t, ".ssh")
	d.keys("d")
	d.typeText("yes")
	d.keys("enter")
	d.until("result", func() bool { return m.mode == anResult })
	if m.result.Count(clean.StatusSkipped) != 1 {
		t.Fatalf("results %+v", m.result.Results)
	}
	if !strings.Contains(m.result.Results[0].Message, "blocked by safety guard") {
		t.Fatalf("message = %q", m.result.Results[0].Message)
	}
	if !fsx.Exists(filepath.Join(home, ".ssh", "id_ed25519")) {
		t.Fatal(".ssh was deleted!")
	}
	if !strings.Contains(m.View(), "blocked by safety guard") {
		t.Fatalf("result view:\n%s", m.View())
	}
}

func TestAnalyzerDeletingCurrentDirAncestor(t *testing.T) {
	f := newAnFixture(t)
	m := newTestAnalyzer(t, f.root, f.home)
	d := newDriver(t, m)
	d.until("sizes", settled(m))
	m.focusName(t, "src")
	d.keys("space") // mark src, then go inside it and delete the marks
	m.focusName(t, "src")
	d.keys("enter")
	d.until("src listed", settled(m))
	d.keys("d")
	if len(m.confirm) != 1 || m.confirm[0].name != "src" {
		t.Fatalf("confirm = %+v", m.confirm)
	}
	d.typeText("yes")
	d.keys("enter")
	d.until("result", func() bool { return m.mode == anResult })
	d.until("moved up", func() bool { return m.cwd == f.root && m.cur().loaded })
	if strings.Contains(viewNames(m), "src") {
		t.Fatalf("src still listed: %s", viewNames(m))
	}
}

func TestAnalyzerRescanAndWorktreeItems(t *testing.T) {
	f := newAnFixture(t)
	m := newTestAnalyzer(t, f.root, f.home)
	d := newDriver(t, m)
	d.until("sizes", settled(m))
	old := m.cur().byName["src"].size
	mkdirFiles(t, filepath.Join(f.root, "src"), map[string]int{"new.bin": 200000})
	mkdirFiles(t, f.root, map[string]int{"added.txt": 10})
	d.keys("r")
	d.until("rescanned", func() bool { return settled(m)() && m.cur().byName["added.txt"] != nil })
	if nw := m.cur().byName["src"].size; nw <= old {
		t.Fatalf("src size %d -> %d", old, nw)
	}
	items := m.itemsFor([]*anEntry{m.cur().byName["wt"], m.cur().byName["src"]})
	if items[0].Method != core.MethodWorktree || items[0].Project != filepath.Join(f.home, "repo", ".git") {
		t.Fatalf("worktree item = %+v", items[0])
	}
	if items[1].Method != core.MethodDelete || items[1].Risk != core.RiskCaution || !items[1].Selectable {
		t.Fatalf("dir item = %+v", items[1])
	}
}

func TestAnalyzerRenderSizes(t *testing.T) {
	f := newAnFixture(t)
	mkdirFiles(t, filepath.Join(f.root, strings.Repeat("very-long-directory-name-", 6)), map[string]int{"x": 10})
	for _, sz := range [][2]int{{80, 24}, {200, 60}, {60, 15}, {45, 12}, {30, 8}} {
		t.Run(fmt.Sprintf("%dx%d", sz[0], sz[1]), func(t *testing.T) {
			m := newTestAnalyzer(t, f.root, f.home)
			m.cleanFn = func(ctx context.Context, items []*core.Item, opt clean.Options, progress func(clean.Result)) *clean.Summary {
				sum := &clean.Summary{DryRun: false}
				for _, it := range items {
					sum.Results = append(sum.Results, clean.Result{Item: it, Status: clean.StatusSkipped, Message: strings.Repeat("reason ", 40)})
				}
				return sum
			}
			d := newDriver(t, m)
			w, h := sz[0], sz[1]
			d.send(teaSize(w, h))
			frame := func(name string) { checkFrame(t, name, m.View(), w, h) }
			frame("loading")
			d.until("sizes", settled(m))
			frame("list")
			d.keys("?")
			frame("help")
			d.keys("x", "space", "space", "d")
			frame("confirm")
			d.typeText("yes")
			d.keys("enter")
			d.until("result", func() bool { return m.mode == anResult })
			frame("result")
			d.keys("x", "end")
			frame("end")
		})
	}
}
