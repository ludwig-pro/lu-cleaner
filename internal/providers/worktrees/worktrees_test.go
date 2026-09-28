package worktrees

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
	"github.com/ludwig-pro/lu-cleaner/internal/safety"
)

// ---------------------------------------------------------------- helpers

// fakeRunner runs git for real and fakes sqlite3 / lsof.
type fakeRunner struct {
	real   core.ExecRunner
	sqlite map[string]string // query substring -> JSON rows
	lsof   string
}

func (f *fakeRunner) Output(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	switch filepath.Base(name) {
	case "sqlite3":
		q := args[len(args)-1]
		for k, v := range f.sqlite {
			if strings.Contains(q, k) {
				return []byte(v), nil
			}
		}
		return nil, nil
	case "lsof":
		return []byte(f.lsof), nil
	}
	return f.real.Output(ctx, dir, name, args...)
}

func (f *fakeRunner) LookPath(name string) (string, error) {
	if name == "sqlite3" || name == "lsof" {
		return "/fake/" + name, nil
	}
	return f.real.LookPath(name)
}

// gitEnv makes git hermetic for the test (and for the provider it runs).
func gitEnv(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "Test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.com")
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newHome returns a real (symlink-free) fake home directory.
func newHome(t *testing.T) string {
	t.Helper()
	h := filepath.Join(realPath(t.TempDir()), "home")
	if err := os.MkdirAll(h, 0o755); err != nil {
		t.Fatal(err)
	}
	return h
}

// newMain creates <home>/src/<name> with one old commit pushed to a bare origin.
func newMain(t *testing.T, home, name string) string {
	t.Helper()
	origin := filepath.Join(home, "remotes", name+".git")
	main := filepath.Join(home, "src", name)
	os.MkdirAll(filepath.Dir(origin), 0o755)
	os.MkdirAll(main, 0o755)
	git(t, filepath.Dir(origin), "init", "-q", "--bare", "-b", "main", origin)
	git(t, main, "init", "-q", "-b", "main")
	write(t, filepath.Join(main, "README.md"), "hello\n")
	write(t, filepath.Join(main, ".gitignore"), "node_modules/\nios/Pods/\nandroid/app/build/\n.env.local\n")
	git(t, main, "add", ".")
	old := time.Now().Add(-30 * 24 * time.Hour).Format(time.RFC3339)
	cmd := exec.Command("git", "-C", main, "commit", "-q", "-m", "init")
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE="+old, "GIT_COMMITTER_DATE="+old)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("commit: %v %s", err, out)
	}
	git(t, main, "remote", "add", "origin", origin)
	git(t, main, "push", "-q", "-u", "origin", "main")
	git(t, main, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	return main
}

// addWT runs `git worktree add` and returns the (real) worktree path.
func addWT(t *testing.T, main, path string, args ...string) string {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	git(t, main, append([]string{"worktree", "add", "-q"}, append(args, path)...)...)
	return path
}

// adminOf returns the admin dir of a worktree.
func adminOf(t *testing.T, wt string) string {
	t.Helper()
	g, ok := readGitFile(wt)
	if !ok {
		t.Fatalf("%s: no .git file", wt)
	}
	return g
}

// idle ages every activity signal of the worktree.
func idle(t *testing.T, wt string, d time.Duration) {
	t.Helper()
	ts := time.Now().Add(-d)
	paths := []string{wt, filepath.Join(wt, ".git")}
	if a, ok := readGitFile(wt); ok {
		paths = append(paths, filepath.Join(a, "index"), filepath.Join(a, "HEAD"), filepath.Join(a, "logs", "HEAD"))
	}
	for _, p := range paths {
		os.Chtimes(p, ts, ts)
	}
}

// collector records every emitted item.
type collector struct {
	mu     sync.Mutex
	events []*core.Item
	final  map[string]*core.Item
}

func (c *collector) emit(it *core.Item) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.final == nil {
		c.final = map[string]*core.Item{}
	}
	c.events = append(c.events, it)
	c.final[it.ID] = it
}

func (c *collector) byPath(p string) *core.Item {
	for _, it := range c.final {
		if it.Path == p || it.Location == p {
			return it
		}
	}
	return nil
}

func scanWith(t *testing.T, env *core.Env, p *Provider) *collector {
	t.Helper()
	c := &collector{}
	if err := p.Scan(context.Background(), env, c.emit); err != nil {
		t.Fatal(err)
	}
	return c
}

func newEnv(home string, r core.Runner) *core.Env {
	return &core.Env{
		Home:     home,
		Now:      time.Now(),
		MaxDepth: 6,
		Runner:   r,
		Logf:     func(string, ...any) {},
		Roots:    []string{filepath.Join(home, "src")},
		WorktreeRoots: []string{
			filepath.Join(home, ".codex", "worktrees"),
			filepath.Join(home, ".cursor", "worktrees"),
			filepath.Join(home, "conductor", "workspaces"),
		},
	}
}

func fixedCwd(p string) *Provider { return &Provider{getwd: func() (string, error) { return p, nil }} }

// ---------------------------------------------------------------- scan

type fixture struct {
	home, main, gone                   string
	merged, dirty, unpushed, locked    string
	orphanPruned, orphanMain, prunable string
	hotfix, pushed, offline, submodule string
}

func buildFixture(t *testing.T) fixture {
	gitEnv(t)
	home := newHome(t)
	f := fixture{home: home}
	f.main = newMain(t, home, "app")

	// Codex, clean & merged, idle 10 days, with artifacts.
	f.merged = addWT(t, f.main, filepath.Join(home, ".codex", "worktrees", "ab12", "app"), "--detach")
	write(t, filepath.Join(f.merged, "node_modules", "pkg", "index.js"), strings.Repeat("x", 10000))
	write(t, filepath.Join(f.merged, "ios", "Pods", "Pod", "a.m"), strings.Repeat("y", 20000))
	write(t, filepath.Join(f.merged, "android", "app", "build", "out.apk"), strings.Repeat("z", 30000))
	idle(t, f.merged, 10*24*time.Hour)

	// Cursor, dirty (untracked file).
	f.dirty = addWT(t, f.main, filepath.Join(home, ".cursor", "worktrees", "app", "x1y2"), "-b", "feat-dirty")
	write(t, filepath.Join(f.dirty, "notes.txt"), "wip\n")
	idle(t, f.dirty, 10*24*time.Hour)

	// Claude Code subagent worktree inside the repo, one commit never pushed.
	f.unpushed = addWT(t, f.main, filepath.Join(f.main, ".claude", "worktrees", "agent-abc123"), "-b", "feat-unpushed")
	write(t, filepath.Join(f.unpushed, "feature.txt"), "new\n")
	git(t, f.unpushed, "add", ".")
	git(t, f.unpushed, "commit", "-q", "-m", "feature")
	idle(t, f.unpushed, 10*24*time.Hour)

	// Conductor, locked (and dirty: locked wins the status).
	f.locked = addWT(t, f.main, filepath.Join(home, "conductor", "workspaces", "app", "tokyo"), "-b", "tokyo")
	git(t, f.main, "worktree", "lock", "--reason", "agent running", f.locked)
	write(t, filepath.Join(f.locked, "scratch.txt"), "x\n")

	// Orphan: admin entry pruned from the main repository.
	f.orphanPruned = addWT(t, f.main, filepath.Join(home, ".codex", "worktrees", "cd34", "app"), "--detach")
	if err := os.RemoveAll(adminOf(t, f.orphanPruned)); err != nil {
		t.Fatal(err)
	}

	// Orphan: main repository deleted.
	f.gone = newMain(t, home, "gone")
	f.orphanMain = addWT(t, f.gone, filepath.Join(home, ".codex", "worktrees", "gone-1"), "-b", "gone-branch")
	os.RemoveAll(f.gone)

	// Prunable: checkout deleted with rm.
	f.prunable = addWT(t, f.main, filepath.Join(home, "src", "app-feature"), "-b", "feature")
	os.RemoveAll(f.prunable)

	// Manual sibling, merged (at main), idle 2 days, relative gitdir.
	f.hotfix = addWT(t, f.main, filepath.Join(home, "src", "app-hotfix"), "-b", "hotfix")
	rel, _ := filepath.Rel(f.hotfix, adminOf(t, f.hotfix))
	write(t, filepath.Join(f.hotfix, ".git"), "gitdir: "+rel+"\n")
	idle(t, f.hotfix, 2*24*time.Hour)

	// Manual sibling, pushed but not merged, idle 3 days, with an ignored .env.local.
	f.pushed = addWT(t, f.main, filepath.Join(home, "src", "app-pushed"), "-b", "pushed")
	write(t, filepath.Join(f.pushed, "p.txt"), "p\n")
	git(t, f.pushed, "add", ".")
	git(t, f.pushed, "commit", "-q", "-m", "p")
	git(t, f.pushed, "push", "-q", "-u", "origin", "pushed")
	write(t, filepath.Join(f.pushed, ".env.local"), "SECRET=1\n")
	idle(t, f.pushed, 3*24*time.Hour)

	// Offline: main repository on an unmounted volume.
	f.offline = filepath.Join(home, ".codex", "worktrees", "off1", "app")
	write(t, filepath.Join(f.offline, ".git"), "gitdir: /Volumes/lu-cleaner-test-missing-volume/repo/.git/worktrees/app\n")
	write(t, filepath.Join(f.offline, "file.txt"), "x")

	// Submodule-like checkout: never a worktree.
	f.submodule = filepath.Join(home, "src", "app2", "sub")
	write(t, filepath.Join(f.submodule, ".git"), "gitdir: ../.git/modules/sub\n")
	return f
}

func TestScanStatuses(t *testing.T) {
	f := buildFixture(t)
	env := newEnv(f.home, &fakeRunner{})
	c := scanWith(t, env, fixedCwd("/"))

	type want struct {
		kind, status string
		risk         core.Risk
		method       core.Method
		recommended  bool
		warn         string // substring ("" = must be empty)
		selectable   bool
	}
	cases := map[string]want{
		f.merged:       {"codex-worktree", statusMerged, core.RiskModerate, core.MethodWorktree, true, "", true},
		f.dirty:        {"cursor-worktree", statusDirty, core.RiskCaution, core.MethodWorktree, false, "1 uncommitted change", true},
		f.unpushed:     {"claude-worktree", statusUnpushed, core.RiskModerate, core.MethodWorktree, false, "1 unpushed commit", true},
		f.locked:       {"conductor-worktree", statusLocked, core.RiskCaution, core.MethodWorktree, false, "locked: agent running", true},
		f.orphanPruned: {"codex-worktree", statusOrphan, core.RiskCaution, core.MethodDelete, false, "orphaned: git no longer tracks it", true},
		f.orphanMain:   {"codex-worktree", statusOrphan, core.RiskCaution, core.MethodDelete, false, "orphaned: git no longer tracks it", true},
		f.hotfix:       {"manual-worktree", statusMerged, core.RiskModerate, core.MethodWorktree, true, "", true},
		f.pushed:       {"manual-worktree", statusClean, core.RiskCaution, core.MethodWorktree, false, "ignored secret/local files would be lost: .env.local", true},
		f.offline:      {"codex-worktree", statusUnknown, core.RiskCaution, core.MethodReport, false, "unmounted volume lu-cleaner-test-missing-volume", false},
	}
	for path, w := range cases {
		t.Run(filepath.Base(filepath.Dir(path))+"/"+filepath.Base(path), func(t *testing.T) {
			it := c.byPath(path)
			if it == nil {
				t.Fatalf("no item for %s", path)
			}
			if it.Sizing {
				t.Errorf("final item still sizing")
			}
			if it.Kind != w.kind || it.Meta["status"] != w.status || it.Risk != w.risk || it.Method != w.method {
				t.Errorf("got kind=%s status=%s risk=%s method=%s, want %s %s %s %s",
					it.Kind, it.Meta["status"], it.Risk, it.Method, w.kind, w.status, w.risk, w.method)
			}
			if it.Recommended != w.recommended {
				t.Errorf("recommended=%v want %v (warn=%q age=%s)", it.Recommended, w.recommended, it.Warn, it.Age(env.Now))
			}
			if w.warn == "" && it.Warn != "" || w.warn != "" && !strings.Contains(it.Warn, w.warn) {
				t.Errorf("warn=%q want %q", it.Warn, w.warn)
			}
			if it.Selectable != w.selectable {
				t.Errorf("selectable=%v want %v", it.Selectable, w.selectable)
			}
			if it.Category != core.CatWorktrees || it.Note == "" || it.Name == "" {
				t.Errorf("category/note/name missing: %+v", it)
			}
			if it.ID != itemID(path) {
				t.Errorf("id=%s", it.ID)
			}
		})
	}

	// Main repository detection.
	if it := c.byPath(f.merged); it.Project != f.main || it.Meta["main"] != f.main {
		t.Errorf("project=%s main=%s, want %s", it.Project, it.Meta["main"], f.main)
	}
	if it := c.byPath(f.hotfix); it.Project != f.main {
		t.Errorf("relative gitdir: project=%s want %s", it.Project, f.main)
	}
	if it := c.byPath(f.orphanMain); it.Project != f.gone || it.Meta["orphan"] != orphanMainGone {
		t.Errorf("orphan main: project=%s orphan=%q", it.Project, it.Meta["orphan"])
	}
	// git worktree repair cannot restore a pruned entry ("unable to locate
	// repository"): the note points to a new worktree instead.
	if it := c.byPath(f.orphanPruned); strings.Contains(it.Note, "worktree repair") || !strings.Contains(it.Note, "worktree add") {
		t.Errorf("pruned orphan note: %s", it.Note)
	}
	// Details.
	if it := c.byPath(f.merged); it.Name != "app · ab12" || it.Meta["branch"] != "(detached)" || it.Meta["merged"] != "true" || it.Meta["default_branch"] != "origin/main" {
		t.Errorf("merged item: name=%q meta=%v", it.Name, it.Meta)
	}
	if it := c.byPath(f.unpushed); it.Name != "app · feat-unpushed" || it.Meta["unpushed"] != "1" || it.Meta["dirty"] != "0" {
		t.Errorf("unpushed item: name=%q meta=%v", it.Name, it.Meta)
	}
	if it := c.byPath(f.locked); it.Meta["locked"] != "true" || !strings.Contains(it.Warn, "1 uncommitted change") {
		t.Errorf("locked item: warn=%q meta=%v", it.Warn, it.Meta)
	}
	if it := c.byPath(f.pushed); it.Meta["upstream"] != "origin/pushed" || it.Meta["unpushed"] != "0" || it.Meta["merged"] != "false" {
		t.Errorf("pushed item meta=%v", it.Meta)
	}

	// Never emitted: main working trees, submodules, deleted checkouts.
	for _, p := range []string{f.main, f.submodule, f.prunable, filepath.Join(f.home, "src", "app2")} {
		if it := c.byPath(p); it != nil && it.Kind != "worktree-prune" {
			t.Errorf("%s must not be emitted (kind %s)", p, it.Kind)
		}
	}

	// Stale metadata: one prune command for the main repository.
	pr := c.final["worktrees:prune:"+f.main]
	if pr == nil {
		t.Fatalf("no prune item; items: %v", keys(c.final))
	}
	if pr.Kind != "worktree-prune" || pr.Method != core.MethodCommand || pr.Risk != core.RiskSafe || !pr.Recommended || !pr.Selectable ||
		strings.Join(pr.Command, " ") != "git -C "+f.main+" worktree prune" || pr.Meta["count"] != "1" {
		t.Errorf("prune item: %+v", pr)
	}
	if !strings.Contains(pr.Meta["entries"], "app-feature") {
		t.Errorf("prune entries: %q", pr.Meta["entries"])
	}

	// Everything proposed passes the clean-time safety guard (the cleaner lets
	// git remove worktrees, and orphans opt in: they hold a .git file).
	g := safety.New(f.home, "", append(append([]string{}, env.Roots...), env.WorktreeRoots...), nil)
	for _, it := range c.final {
		if !it.CanClean() || it.Method == core.MethodCommand {
			continue
		}
		if it.Method == core.MethodDelete && it.Meta["status"] == statusOrphan && (!it.AllowGitRepo || it.Recheck == nil || !it.NoRecommend) {
			t.Errorf("%s: orphan must opt in to AllowGitRepo, with a Recheck and NoRecommend", it.Path)
		}
		for _, p := range it.Targets() {
			if err := g.Check(p, safety.Options{AllowGitRepo: it.AllowGitRepo || it.Method == core.MethodWorktree}); err != nil {
				t.Errorf("%s: guard refuses %s: %v", it.Kind, p, err)
			}
		}
	}
	if n := len(c.final); n != len(cases)+1 {
		t.Errorf("got %d items, want %d: %v", n, len(cases)+1, keys(c.final))
	}
}

func keys(m map[string]*core.Item) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestSizeBreakdown(t *testing.T) {
	f := buildFixture(t)
	env := newEnv(f.home, &fakeRunner{})
	c := scanWith(t, env, fixedCwd("/"))
	it := c.byPath(f.merged)
	whole, err := fsx.Size(context.Background(), f.merged, nil)
	if err != nil {
		t.Fatal(err)
	}
	if it.Size != whole.Bytes || it.Files != whole.Files {
		t.Errorf("size=%d files=%d, single walk says %d / %d", it.Size, it.Files, whole.Bytes, whole.Files)
	}
	var sum int64
	for _, k := range []string{"node_modules", "ios/Pods", "android/app/build"} {
		st, _ := fsx.Size(context.Background(), filepath.Join(f.merged, k), nil)
		if it.Meta[k] != fsx.Bytes(st.Bytes) || st.Bytes == 0 {
			t.Errorf("meta[%s]=%q want %s", k, it.Meta[k], fsx.Bytes(st.Bytes))
		}
		sum += st.Bytes
	}
	if it.Meta["artifacts"] != fsx.Bytes(sum) || it.Meta["artifacts_bytes"] != fmt.Sprint(sum) {
		t.Errorf("artifacts=%q (%s) want %s", it.Meta["artifacts"], it.Meta["artifacts_bytes"], fsx.Bytes(sum))
	}
	if it.Meta["checkout"] != fsx.Bytes(whole.Bytes-sum) {
		t.Errorf("checkout=%q", it.Meta["checkout"])
	}
}

func TestIDStabilityAndPlaceholders(t *testing.T) {
	f := buildFixture(t)
	env := newEnv(f.home, &fakeRunner{})
	c1 := scanWith(t, env, fixedCwd("/"))
	c2 := scanWith(t, env, fixedCwd("/"))
	if len(c1.final) != len(c2.final) {
		t.Fatalf("%d vs %d items", len(c1.final), len(c2.final))
	}
	for id, it := range c1.final {
		if c2.final[id] == nil {
			t.Errorf("id %s not stable", id)
		}
		if it.Kind != "worktree-prune" && id != itemID(it.Path) {
			t.Errorf("id %s does not match path %s", id, it.Path)
		}
	}
	// Every worktree is first shown as a Sizing placeholder, then replaced.
	sizing := map[string]bool{}
	for _, ev := range c1.events {
		if ev.Sizing {
			sizing[ev.ID] = true
		} else if ev.Method != core.MethodCommand && ev.Method != core.MethodReport && !sizing[ev.ID] {
			t.Errorf("%s emitted without placeholder first", ev.ID)
		}
	}
	for id := range sizing {
		if c1.final[id].Sizing {
			t.Errorf("%s never measured", id)
		}
	}
}

func TestCurrentDirAndProtected(t *testing.T) {
	f := buildFixture(t)
	env := newEnv(f.home, &fakeRunner{})
	env.Protected = func(p string) bool { return p == f.hotfix }
	env.Exclude = []string{f.pushed}
	c := scanWith(t, env, fixedCwd(filepath.Join(f.merged, "node_modules", "pkg")))
	it := c.byPath(f.merged)
	if it.Selectable || !strings.Contains(it.Warn, "current directory") || it.Recommended {
		t.Errorf("cwd worktree: selectable=%v warn=%q rec=%v", it.Selectable, it.Warn, it.Recommended)
	}
	if c.byPath(f.hotfix) != nil {
		t.Errorf("protected worktree emitted")
	}
	if c.byPath(f.pushed) != nil {
		t.Errorf("excluded worktree emitted")
	}
}

// A moved checkout keeps using its admin entry: the prune command would
// orphan it, so only the other stale entries are removed.
func TestMovedWorktreeProtectsPrune(t *testing.T) {
	gitEnv(t)
	home := newHome(t)
	main := newMain(t, home, "app")
	moved := addWT(t, main, filepath.Join(home, "src", "app-old"), "-b", "moving")
	dest := filepath.Join(home, "src", "app-new")
	if err := os.Rename(moved, dest); err != nil {
		t.Fatal(err)
	}
	gone := addWT(t, main, filepath.Join(home, "src", "app-gone"), "-b", "gone")
	goneAdmin := adminOf(t, gone)
	os.RemoveAll(gone)

	env := newEnv(home, &fakeRunner{})
	c := scanWith(t, env, fixedCwd("/"))
	it := c.byPath(dest)
	if it == nil || it.Risk != core.RiskCaution || !strings.Contains(it.Warn, "moved: git records it at") || it.Meta["moved_from"] != moved {
		t.Fatalf("moved item: %+v", it)
	}
	pr := c.final["worktrees:prune:"+main]
	if pr == nil {
		t.Fatal("no prune item")
	}
	if pr.Method != core.MethodDelete || len(pr.Paths) != 1 || pr.Paths[0] != goneAdmin || len(pr.Command) != 0 {
		t.Errorf("prune must delete only %s: method=%s paths=%v cmd=%v", goneAdmin, pr.Method, pr.Paths, pr.Command)
	}
	g := safety.New(home, "", env.Roots, nil)
	for _, p := range pr.Paths {
		if err := g.Check(p, safety.Options{}); err != nil {
			t.Errorf("guard refuses admin dir: %v", err)
		}
	}
}

func TestToolState(t *testing.T) {
	gitEnv(t)
	home := newHome(t)
	main := newMain(t, home, "app")
	codex := addWT(t, main, filepath.Join(home, ".codex", "worktrees", "c1", "app"), "--detach")
	conductor := addWT(t, main, filepath.Join(home, "conductor", "workspaces", "app", "lima"), "-b", "lima")
	desktopDone := addWT(t, main, filepath.Join(main, ".claude", "worktrees", "feature-a1b2c3"), "-b", "claude/feature-a1b2c3")
	desktopLeased := addWT(t, main, filepath.Join(main, ".claude", "worktrees", "other-d4e5f6"), "--detach")
	busy := addWT(t, main, filepath.Join(home, "src", "app-busy"), "-b", "busy")
	for _, w := range []string{codex, conductor, desktopDone, desktopLeased, busy} {
		idle(t, w, 10*24*time.Hour)
	}

	// Tool databases (contents faked by the runner).
	write(t, filepath.Join(home, ".codex", "state_5.sqlite"), "")
	write(t, filepath.Join(home, "Library", "Application Support", "com.conductor.app", "conductor.db"), "")
	q, _ := json.Marshal(map[string]any{"electron-managed-worktree-archives": []map[string]any{{"cwd": codex, "phase": "queued"}}})
	write(t, filepath.Join(home, ".codex", ".codex-global-state.json"), string(q))
	claude := filepath.Join(home, "Library", "Application Support", "Claude")
	old := time.Now().Add(-10 * 24 * time.Hour).UnixMilli()
	sess, _ := json.Marshal(map[string]any{"sessionId": "local_1", "worktreePath": desktopDone, "isArchived": true, "lastActivityAt": old})
	write(t, filepath.Join(claude, "claude-code-sessions", "acct", "org", "local_1.json"), string(sess))
	pool, _ := json.Marshal(map[string]any{"worktrees": map[string]any{
		"other-d4e5f6": map[string]any{"path": desktopLeased, "leasedBy": "local_2"},
	}})
	write(t, filepath.Join(claude, "git-worktrees.json"), string(pool))

	r := &fakeRunner{
		sqlite: map[string]string{
			"from threads":    fmt.Sprintf(`[{"cwd":%q,"archived":0,"n":2,"u":%d}]`, filepath.Join(codex, "packages"), time.Now().Add(-30*24*time.Hour).Unix()),
			"from workspaces": fmt.Sprintf(`[{"p":%q,"s":"active"}]`, conductor),
		},
		lsof: fmt.Sprintf("p1\ncnode\nfcwd\nn%s\np%d\ncself\nfcwd\nn%s\n", filepath.Join(busy, "src"), os.Getpid(), codex),
	}
	env := newEnv(home, r)
	c := scanWith(t, env, fixedCwd("/"))

	cases := []struct {
		path, kind, warn, meta, metaVal string
		risk                            core.Risk
		rec                             bool
	}{
		{codex, "codex-worktree", "2 active Codex threads", "codex_archive", "queued", core.RiskCaution, false},
		{conductor, "conductor-worktree", "active Conductor workspace", "conductor", "active", core.RiskCaution, false},
		{desktopDone, "claude-desktop-worktree", "", "claude_session", "archived", core.RiskModerate, true},
		{desktopLeased, "claude-desktop-worktree", "active Claude desktop session", "claude_session", "active", core.RiskCaution, false},
		{busy, "manual-worktree", "in use by node", "in_use", "node", core.RiskCaution, false},
	}
	for _, tc := range cases {
		it := c.byPath(tc.path)
		if it == nil {
			t.Errorf("%s: missing", tc.path)
			continue
		}
		if it.Kind != tc.kind || it.Risk != tc.risk || it.Recommended != tc.rec || it.Meta[tc.meta] != tc.metaVal ||
			(tc.warn == "" && it.Warn != "") || !strings.Contains(it.Warn, tc.warn) {
			t.Errorf("%s: kind=%s risk=%s rec=%v warn=%q meta=%v", filepath.Base(tc.path), it.Kind, it.Risk, it.Recommended, it.Warn, it.Meta)
		}
	}
	if it := c.byPath(codex); it.Meta["codex_threads"] != "2 active, 0 archived" || !strings.Contains(it.Note, "queued it for archival") {
		t.Errorf("codex meta=%v note=%s", it.Meta, it.Note)
	}
	if it := c.byPath(conductor); !strings.Contains(it.Note, "Conductor will show the workspace as missing") {
		t.Errorf("conductor note: %s", it.Note)
	}
	// Agent activity counts as use: an archived session 10 days ago keeps LastUsed there.
	if it := c.byPath(desktopDone); it.Age(env.Now) < 9*24*time.Hour {
		t.Errorf("claude desktop age=%s", it.Age(env.Now))
	}
}

func TestClassify(t *testing.T) {
	home := "/Users/u"
	s := &scan{home: home, tools: &toolState{claudeWT: map[string]*claudeWT{"/r/app/.claude/worktrees/known": {known: true}}}}
	cases := []struct {
		path, branch, main, want string
	}{
		{home + "/.codex/worktrees/ab12/app", "", "/r/app", toolCodex},
		{home + "/.cursor/worktrees/app/x1y2", "feat", "/r/app", toolCursor},
		{home + "/conductor/workspaces/app/lima", "lima", "/r/app", toolConductor},
		{home + "/.superset/worktrees/app/t1", "t1", "/r/app", toolSuperset},
		{home + "/Library/Application Support/Claude/worktrees/x", "", "/r/app", toolClaudeDesktop},
		{"/r/app/.claude/worktrees/known", "", "/r/app", toolClaudeDesktop},
		{"/r/app/.claude/worktrees/fix-login-a1b2c3", "claude/fix-login-a1b2c3", "/r/app", toolClaudeDesktop},
		{"/r/app/.claude/worktrees/fix-login-a1b2c3", "", "/r/app", toolClaudeDesktop},
		{"/r/app/.claude/worktrees/cafe12", "worktree-cafe12", "/r/app", toolClaude},
		{"/r/app/.claude/worktrees/agent-abcdef123456", "", "/r/app", toolClaude},
		{"/r/app/.claude/worktrees/my-feature", "worktree-my-feature", "/r/app", toolClaude},
		{"/var/folders/x/T/vibe-kanban/worktrees/1-task", "vk/1", "/r/app", toolVibeKanban},
		{"/r/tasks/wt", "agent/x", home + "/multica_workspaces_acme/.repos/ws/github.com+o+r.git", toolMultica},
		{"/r/app-hotfix", "hotfix", "/r/app", toolManual},
	}
	for _, tc := range cases {
		w := &worktree{path: tc.path, branch: tc.branch, main: tc.main}
		if got := s.classify(w); got != tc.want {
			t.Errorf("%s (%s): %s want %s", tc.path, tc.branch, got, tc.want)
		}
	}
}

func TestRecommendRules(t *testing.T) {
	now := time.Now()
	s := &scan{now: now}
	cases := []struct {
		name   string
		merged bool
		idle   time.Duration
		risk   core.Risk
		warn   string
		want   bool
	}{
		{"clean week", false, 8 * 24 * time.Hour, core.RiskModerate, "", true},
		{"clean 3 days", false, 3 * 24 * time.Hour, core.RiskModerate, "", false},
		{"merged 2 days", true, 2 * 24 * time.Hour, core.RiskModerate, "", true},
		{"merged 2 hours", true, 2 * time.Hour, core.RiskModerate, "", false},
		{"warned", true, 30 * 24 * time.Hour, core.RiskModerate, "ignored .env files", false},
		{"caution", true, 30 * 24 * time.Hour, core.RiskCaution, "", false},
	}
	for _, tc := range cases {
		w := &worktree{merged: tc.merged, activity: now.Add(-tc.idle)}
		it := &core.Item{Risk: tc.risk, Warn: tc.warn, Method: core.MethodWorktree, Selectable: true}
		s.recommend(w, it)
		if it.Recommended != tc.want {
			t.Errorf("%s: got %v", tc.name, it.Recommended)
		}
	}
}

func TestStatusPriority(t *testing.T) {
	cases := []struct {
		w    worktree
		want string
	}{
		{worktree{orphan: "x", locked: true}, statusOrphan},
		{worktree{gitOK: true, locked: true, dirty: 2, unpushedOK: true, unpushed: 1}, statusLocked},
		{worktree{gitOK: true, dirty: 2, unpushedOK: true, unpushed: 1}, statusDirty},
		{worktree{gitOK: true, unpushedOK: true, unpushed: 1, merged: true}, statusUnpushed},
		{worktree{gitOK: true, unpushedOK: false}, statusUnpushed},
		{worktree{gitOK: true, unpushedOK: true, merged: true}, statusMerged},
		{worktree{gitOK: true, unpushedOK: true}, statusClean},
		{worktree{offline: "SSD"}, statusUnknown},
		{worktree{}, statusUnknown},
	}
	for i, tc := range cases {
		if got := tc.w.status(); got != tc.want {
			t.Errorf("%d: %s want %s", i, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------- parsers

func TestArtifactKey(t *testing.T) {
	cases := map[string]string{
		"node_modules":                       "node_modules",
		"apps/web/node_modules":              "node_modules",
		"ios/Pods":                           "ios/Pods",
		"example/ios/Pods":                   "ios/Pods",
		"Pods":                               "",
		"ios/build":                          "ios/build",
		"android/build":                      "android/build",
		"android/app/build":                  "android/app/build",
		"android/lib/build":                  "android/build",
		"apps/app/android/app/build":         "android/app/build",
		"android/.gradle":                    "android/.gradle",
		"android/app/.cxx":                   "android/.cxx",
		".expo":                              ".expo",
		"apps/app/.next":                     ".next",
		"dist":                               "dist",
		"build":                              "",
		"src/components":                     "",
		"packages/ui/android/src/main/build": "",
	}
	for rel, want := range cases {
		if got := artifactKey(rel); got != want {
			t.Errorf("%s: %q want %q", rel, got, want)
		}
	}
}

func TestParseStatus(t *testing.T) {
	out := strings.Join([]string{
		"# branch.oid 0123456789abcdef",
		"# branch.head feat/x",
		"# branch.upstream origin/feat/x",
		"# branch.ab +3 -1",
		"1 .M N... 100644 100644 100644 aaa aaa src/a file.ts",
		"2 R. N... 100644 100644 100644 aaa bbb R100 new.ts", "old.ts",
		"u UU N... 100644 100644 100644 100644 a b c conflict.ts",
		"? untracked dir/",
		"",
	}, "\x00")
	st := parseStatus(out)
	if st.head != "feat/x" || st.upstream != "origin/feat/x" || !st.abOK || st.ahead != 3 || st.behind != 1 {
		t.Errorf("branch info: %+v", st)
	}
	want := []string{"src/a file.ts", "new.ts", "conflict.ts", "untracked dir/"}
	if strings.Join(st.paths, "|") != strings.Join(want, "|") {
		t.Errorf("paths=%q", st.paths)
	}
	st = parseStatus("# branch.oid (initial)\x00# branch.head (detached)\x00")
	if st.head != "(detached)" || st.abOK || len(st.paths) != 0 {
		t.Errorf("detached: %+v", st)
	}
}

func TestParseWorktreeList(t *testing.T) {
	out := "worktree /r/main\x00HEAD aaa\x00branch refs/heads/main\x00\x00" +
		"worktree /r/wt1\x00HEAD bbb\x00detached\x00locked agent busy\x00\x00" +
		"worktree /r/gone\x00HEAD ccc\x00branch refs/heads/x\x00prunable gitdir file points to non-existent location\x00\x00"
	l := parseWorktreeList(out)
	if len(l) != 3 {
		t.Fatalf("%d entries", len(l))
	}
	if l[0].branch != "main" || !l[1].detached || !l[1].locked || l[1].reason != "agent busy" || !l[2].prunable || l[2].branch != "x" {
		t.Errorf("%+v", l)
	}
	bare := parseWorktreeList("worktree /r/x.git\x00bare\x00\x00")
	if len(bare) != 1 || !bare[0].bare {
		t.Errorf("bare: %+v", bare)
	}
}

func TestCommonDirAndMain(t *testing.T) {
	cases := []struct {
		gitdir, common, main string
		ok, bare             bool
	}{
		{"/u/repo/.git/worktrees/wt", "/u/repo/.git", "/u/repo", true, false},
		{"/u/cache/proj.git/worktrees/wt", "/u/cache/proj.git", "/u/cache/proj.git", true, true},
		{"/u/repo/.git/modules/sub", "", "", false, false},
		{"/u/repo/.git/modules/sub/worktrees/x", "", "", false, false},
		{"/u/repo/.git", "", "", false, false},
	}
	for _, tc := range cases {
		c, ok := commonDir(tc.gitdir)
		if ok != tc.ok || c != tc.common {
			t.Errorf("%s: common=%q ok=%v", tc.gitdir, c, ok)
			continue
		}
		if ok {
			if m, bare := mainOf(c); m != tc.main || bare != tc.bare {
				t.Errorf("%s: main=%q bare=%v", tc.gitdir, m, bare)
			}
		}
	}
	if repoLabel("/u/.repos/ws/github.com+acme+api.git", true) != "api" || repoLabel("/u/src/app", false) != "app" {
		t.Error("repoLabel")
	}
	if dirLabel("/h/.codex/worktrees/a732/app", "app") != "a732" || dirLabel("/h/src/app-x", "app") != "app-x" {
		t.Error("dirLabel")
	}
}

func TestOfflineVolume(t *testing.T) {
	if v := offlineVolume("/Volumes/lu-cleaner-no-such-volume/src/.git/worktrees/x"); v != "lu-cleaner-no-such-volume" {
		t.Errorf("missing volume: %q", v)
	}
	if v := offlineVolume("/Users/x/src"); v != "" {
		t.Errorf("not a volume path: %q", v)
	}
	// A stale mount point (plain directory on the /Volumes device) counts as offline.
	root := t.TempDir()
	saved := volumesRoot
	volumesRoot = root
	defer func() { volumesRoot = saved }()
	os.MkdirAll(filepath.Join(root, "Stale"), 0o755)
	if v := offlineVolume(filepath.Join(root, "Stale", "repo")); v != "Stale" {
		t.Errorf("stale mount point: %q", v)
	}
}

func TestExternalVolumeIsReportOnly(t *testing.T) {
	f := buildFixture(t)
	saved := devOf
	defer func() { devOf = saved }()
	homeDev, _ := saved(f.home)
	devOf = func(p string) (uint64, bool) {
		if fsx.Within(p, f.dirty) {
			return homeDev + 1, true
		}
		return saved(p)
	}
	env := newEnv(f.home, &fakeRunner{})
	c := scanWith(t, env, fixedCwd("/"))
	it := c.byPath(f.dirty)
	if it == nil || it.Method != core.MethodReport || it.Selectable || it.Warn != "on external volume — no internal gain" || it.Size != 0 {
		t.Errorf("external: %+v", it)
	}
}

func TestAbsentToolsAndEmptyHome(t *testing.T) {
	home := newHome(t)
	env := newEnv(home, &fakeRunner{})
	c := scanWith(t, env, fixedCwd("/"))
	if len(c.final) != 0 {
		t.Errorf("empty home produced %d items", len(c.final))
	}
	// No git on PATH: nothing, no error.
	env.Runner = noGit{}
	if err := New().Scan(context.Background(), env, func(*core.Item) { t.Error("emitted without git") }); err != nil {
		t.Error(err)
	}
}

type noGit struct{}

func (noGit) Output(context.Context, string, string, ...string) ([]byte, error) {
	return nil, os.ErrNotExist
}
func (noGit) LookPath(string) (string, error) { return "", os.ErrNotExist }

func TestCancelledContext(t *testing.T) {
	f := buildFixture(t)
	env := newEnv(f.home, &fakeRunner{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := New().Scan(ctx, env, func(*core.Item) {}); err == nil {
		t.Error("expected ctx error")
	}
}
