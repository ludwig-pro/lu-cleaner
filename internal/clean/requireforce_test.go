package clean

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
)

// orphanDir creates an orphaned worktree folder (a .git file pointing to an
// admin dir that no longer exists) with some work in it, and its item as the
// worktrees provider emits it (MethodDelete, RequireForce, AllowGitRepo).
func (f *fixture) orphanDir(t *testing.T, rel string) (string, *core.Item) {
	t.Helper()
	dir := f.mk(t, rel, "wip.txt")
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: "+filepath.Join(f.home, "gone/.git/worktrees/x")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	it := item(dir, core.MethodDelete)
	it.Risk = core.RiskCaution
	it.RequireForce = true
	it.AllowGitRepo = true
	return dir, it
}

// Items with RequireForce are refused without --force, in dry-run too, with
// the reason from Warn (or the orphaned-worktree default); --force and Trash
// mode (recoverable) proceed (leftover C1).
func TestRequireForceRefusedWithoutForce(t *testing.T) {
	f := newFixture(t)
	dir, it := f.orphanDir(t, ".codex/worktrees/t1/repo")
	for _, dry := range []bool{true, false} {
		o := f.opts()
		o.DryRun = dry
		r := Run(context.Background(), []*core.Item{it}, o, nil).Results[0]
		if r.Status != StatusSkipped || r.Message != "needs --force: "+defaultForceReason {
			t.Fatalf("dry=%v: %+v", dry, r)
		}
		mustExist(t, filepath.Join(dir, "wip.txt"))
	}

	warned := it.Clone()
	warned.Warn = "  main repository gone: 2 uncommitted files would be lost  "
	r := Run(context.Background(), []*core.Item{warned}, f.opts(), nil).Results[0]
	if r.Status != StatusSkipped || r.Message != "needs --force: main repository gone: 2 uncommitted files would be lost" {
		t.Fatalf("with a warning: %+v", r)
	}
	mustExist(t, filepath.Join(dir, "wip.txt"))

	// A RequireForce command item is refused as well (no filesystem check needed).
	cmd := &core.Item{ID: "cmd", Kind: "cmd", Name: "cmd", Risk: core.RiskSafe, Method: core.MethodCommand,
		Command: []string{"true"}, Selectable: true, RequireForce: true, Warn: "reason"}
	if r := Run(context.Background(), []*core.Item{cmd}, f.opts(), nil).Results[0]; r.Status != StatusSkipped || r.Message != "needs --force: reason" {
		t.Fatalf("command: %+v", r)
	}

	// --force: the dry-run reports it, the real run deletes it.
	o := f.opts()
	o.Force, o.DryRun = true, true
	if r := Run(context.Background(), []*core.Item{it}, o, nil).Results[0]; r.Status != StatusDryRun {
		t.Fatalf("forced dry-run: %+v", r)
	}
	mustExist(t, filepath.Join(dir, "wip.txt"))

	// Trash mode proceeds without --force: the move is recoverable.
	o = f.opts()
	o.Trash = true
	r = Run(context.Background(), []*core.Item{it}, o, nil).Results[0]
	if r.Status != StatusDone || r.Method != core.MethodTrash || r.Trashed != it.Size || r.Freed != 0 {
		t.Fatalf("trash mode: %+v", r)
	}
	mustBeGone(t, dir)
	if m, _ := filepath.Glob(filepath.Join(f.home, ".Trash", "repo*", "wip.txt")); len(m) != 1 {
		t.Fatalf("work not found in the Trash: %v", m)
	}

	dir2, it2 := f.orphanDir(t, ".codex/worktrees/t2/repo")
	o = f.opts()
	o.Force = true
	if r := Run(context.Background(), []*core.Item{it2}, o, nil).Results[0]; r.Status != StatusDone {
		t.Fatalf("forced: %+v", r)
	}
	mustBeGone(t, dir2)
}

// An item refused for lack of --force does not make the items inside it
// redundant (they are cleaned), and is itself dropped when a kept item covers
// it; with --force it covers them as usual (leftover C1).
func TestRequireForceDoesNotHideNestedItems(t *testing.T) {
	f := newFixture(t)
	dir, orphan := f.orphanDir(t, ".codex/worktrees/t1/repo")
	os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}"), 0o644)
	nm := f.mk(t, ".codex/worktrees/t1/repo/node_modules/x", "index.js")
	nmItem := item(filepath.Dir(nm), core.MethodDelete)
	nmItem.RequireSibling = []string{"package.json"}

	for _, dry := range []bool{true, false} {
		o := f.opts()
		o.DryRun = dry
		sum := Run(context.Background(), []*core.Item{orphan, nmItem}, o, nil)
		res := byPath(sum)
		want := StatusDone
		if dry {
			want = StatusDryRun
		}
		if len(sum.Results) != 2 || res[nmItem.Path].Status != want ||
			res[dir].Status != StatusSkipped || !strings.HasPrefix(res[dir].Message, msgNeedsForce) {
			t.Fatalf("dry=%v: %+v", dry, sum.Results)
		}
		if sum.Estimated != map[bool]int64{true: 0, false: nmItem.Size}[dry] {
			t.Errorf("dry=%v: estimated %d", dry, sum.Estimated)
		}
	}
	mustBeGone(t, nmItem.Path)
	mustExist(t, filepath.Join(dir, "wip.txt"))

	// With --force the orphan covers what is inside it: one result.
	f.mk(t, ".codex/worktrees/t1/repo/node_modules/x", "index.js")
	o := f.opts()
	o.Force, o.DryRun = true, true
	if sum := Run(context.Background(), []*core.Item{nmItem, orphan}, o, nil); len(sum.Results) != 1 || sum.Results[0].Item != orphan {
		t.Fatalf("forced: %+v", sum.Results)
	}

	// A refused item inside a kept item is left to it: one result.
	parent := item(filepath.Dir(filepath.Dir(dir)), core.MethodDelete)
	parent.AllowGitRepo = true
	o = f.opts()
	o.DryRun = true
	sum := Run(context.Background(), []*core.Item{orphan, parent}, o, nil)
	if len(sum.Results) != 1 || sum.Results[0].Item != parent || sum.Results[0].Status != StatusDryRun {
		t.Fatalf("refused item inside a kept one: %+v", sum.Results)
	}
	mustExist(t, filepath.Join(dir, "wip.txt"))
}

// A worktree item with RequireForce is refused, the artifacts inside it are
// still cleaned, and a selected outer worktree containing it is kept
// (leftover C1).
func TestRequireForceWorktree(t *testing.T) {
	needGit(t)
	f := newFixture(t)
	main := f.newRepo(t, "src/app", map[string]string{".gitignore": ".claude/worktrees/\nnode_modules/\n"})
	a := filepath.Join(f.home, "wt/app-A")
	git(t, main, "worktree", "add", "-q", "-b", "feat-a", a)
	b := filepath.Join(a, ".claude/worktrees/B")
	git(t, main, "worktree", "add", "-q", "-b", "feat-b", b)
	nm := filepath.Join(b, "node_modules")
	os.MkdirAll(filepath.Join(nm, "x"), 0o755)
	os.WriteFile(filepath.Join(b, "package.json"), []byte("{}"), 0o644)
	git(t, b, "add", "package.json")
	git(t, b, "commit", "-qm", "pkg")

	inner := wtItem(b, main)
	inner.RequireForce = true
	inner.Warn = "keep me"
	nmItem := item(nm, core.MethodDelete)
	nmItem.RequireSibling = []string{"package.json"}
	for _, dry := range []bool{true, false} {
		o := f.opts()
		o.DryRun = dry
		res := byPath(Run(context.Background(), []*core.Item{inner, nmItem}, o, nil))
		if res[b].Status != StatusSkipped || res[b].Message != "needs --force: keep me" {
			t.Fatalf("dry=%v worktree: %+v", dry, res[b])
		}
		want := StatusDone
		if dry {
			want = StatusDryRun
		}
		if res[nm].Status != want {
			t.Fatalf("dry=%v node_modules: %+v", dry, res[nm])
		}
	}
	mustExist(t, filepath.Join(b, "package.json"))
	mustBeGone(t, nm)

	// The outer worktree is kept along with the refused inner one.
	for _, dry := range []bool{true, false} {
		o := f.opts()
		o.DryRun = dry
		sum := Run(context.Background(), []*core.Item{wtItem(a, main), inner}, o, nil)
		res := byPath(sum)
		if res[b].Status != StatusSkipped || !strings.HasPrefix(res[b].Message, msgNeedsForce) || sum.Results[0].Item != inner {
			t.Fatalf("dry=%v inner: %+v", dry, sum.Results)
		}
		if res[a].Status != StatusSkipped || !strings.Contains(res[a].Message, "nested inside it") {
			t.Fatalf("dry=%v outer: %+v", dry, res[a])
		}
	}
	mustExist(t, filepath.Join(a, "README.md"))
	mustExist(t, filepath.Join(b, "package.json"))

	// --force removes both, inner first.
	o := f.opts()
	o.Force = true
	sum := Run(context.Background(), []*core.Item{wtItem(a, main), inner}, o, nil)
	if res := byPath(sum); res[a].Status != StatusDone || res[b].Status != StatusDone || sum.Results[0].Item != inner {
		t.Fatalf("forced: %+v", sum.Results)
	}
	mustBeGone(t, a)
}
