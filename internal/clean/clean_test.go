package clean

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/safety"
)

type fixture struct {
	home string
	g    *safety.Guard
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	home, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".state"))
	return &fixture{home: home, g: safety.New(home, "", []string{filepath.Join(home, "src")}, nil)}
}

func (f *fixture) mk(t *testing.T, rel string, files ...string) string {
	t.Helper()
	p := filepath.Join(f.home, rel)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		if err := os.WriteFile(filepath.Join(p, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func (f *fixture) opts() Options {
	return Options{Guard: f.g, Home: f.home, NoHistory: true}
}

func item(path string, m core.Method) *core.Item {
	return &core.Item{ID: path, Kind: "test", Name: filepath.Base(path), Path: path, Size: 10, Risk: core.RiskSafe, Method: m, Selectable: true}
}

func TestDeleteAndNested(t *testing.T) {
	f := newFixture(t)
	f.mk(t, "src/app", "package.json")
	nm := f.mk(t, "src/app/node_modules/react", "index.js")
	inner := filepath.Join(nm, "react")
	sum := Run(context.Background(), []*core.Item{item(inner, core.MethodDelete), item(nm, core.MethodDelete)}, f.opts(), nil)
	if len(sum.Results) != 1 || sum.Results[0].Status != StatusDone {
		t.Fatalf("results = %+v", sum.Results)
	}
	if _, err := os.Stat(nm); !os.IsNotExist(err) {
		t.Fatalf("node_modules still exists")
	}
	if _, err := os.Stat(filepath.Join(f.home, "src/app/package.json")); err != nil {
		t.Fatalf("package.json removed!")
	}
}

func TestDryRunTouchesNothing(t *testing.T) {
	f := newFixture(t)
	p := f.mk(t, ".npm/_cacache", "a")
	o := f.opts()
	o.DryRun = true
	sum := Run(context.Background(), []*core.Item{item(p, core.MethodDelete)}, o, nil)
	if sum.Results[0].Status != StatusDryRun {
		t.Fatalf("status = %v", sum.Results[0].Status)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal("dry-run deleted the path")
	}
}

func TestGuardAndSiblingBlock(t *testing.T) {
	f := newFixture(t)
	repo := f.mk(t, "src/repo", "main.go")
	f.mk(t, "src/repo/.git")
	build := f.mk(t, "src/other/build", "x") // no marker next to it
	it := item(build, core.MethodDelete)
	it.RequireSibling = []string{"build.gradle", "build.gradle.kts"}
	sum := Run(context.Background(), []*core.Item{item(repo, core.MethodDelete), item(f.home, core.MethodDelete), it}, f.opts(), nil)
	for _, r := range sum.Results {
		if r.Status != StatusSkipped {
			t.Errorf("%s: status %v (%s %s), want skipped", r.Item.Path, r.Status, r.Message, r.Error)
		}
	}
	for _, p := range []string{repo, build, f.home} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was removed", p)
		}
	}
}

func TestSymlinkRemovedNotFollowed(t *testing.T) {
	f := newFixture(t)
	target := f.mk(t, "precious", "data.txt")
	link := filepath.Join(f.mk(t, ".cache/x"), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	sum := Run(context.Background(), []*core.Item{item(link, core.MethodDelete)}, f.opts(), nil)
	if sum.Results[0].Status != StatusDone {
		t.Fatalf("%+v", sum.Results[0])
	}
	if _, err := os.Stat(filepath.Join(target, "data.txt")); err != nil {
		t.Fatal("symlink target content was deleted")
	}
}

func TestReadOnlyTree(t *testing.T) {
	f := newFixture(t)
	p := f.mk(t, "go/pkg/mod/example.com/m@v1", "go.mod")
	os.Chmod(p, 0o555)
	os.Chmod(filepath.Dir(p), 0o555)
	sum := Run(context.Background(), []*core.Item{item(filepath.Join(f.home, "go/pkg/mod"), core.MethodDelete)}, f.opts(), nil)
	if sum.Results[0].Status != StatusDone {
		t.Fatalf("%+v", sum.Results[0])
	}
}

func TestTrash(t *testing.T) {
	f := newFixture(t)
	p := f.mk(t, "Library/Caches/foo", "a")
	o := f.opts()
	o.Trash = true
	sum := Run(context.Background(), []*core.Item{item(p, core.MethodDelete)}, o, nil)
	if sum.Results[0].Status != StatusDone {
		t.Fatalf("%+v", sum.Results[0])
	}
	if _, err := os.Stat(filepath.Join(f.home, ".Trash", "foo", "a")); err != nil {
		t.Fatalf("not in trash: %v", err)
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func TestWorktreeRemoval(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	f := newFixture(t)
	main := f.mk(t, "src/repo", "README.md")
	git(t, main, "init", "-q", "-b", "main")
	git(t, main, "add", ".")
	git(t, main, "commit", "-qm", "init")
	wtRoot := f.mk(t, ".codex/worktrees/abcd")
	clean := filepath.Join(wtRoot, "clean")
	dirty := filepath.Join(wtRoot, "dirty")
	git(t, main, "worktree", "add", "-q", "-b", "wt-clean", clean)
	git(t, main, "worktree", "add", "-q", "-b", "wt-dirty", dirty)
	os.WriteFile(filepath.Join(dirty, "wip.txt"), []byte("work in progress"), 0o644)

	mkItem := func(p string) *core.Item {
		it := item(p, core.MethodWorktree)
		it.Project = main
		it.Risk = core.RiskModerate
		return it
	}
	sum := Run(context.Background(), []*core.Item{mkItem(clean), mkItem(dirty)}, f.opts(), nil)
	byPath := map[string]Result{}
	for _, r := range sum.Results {
		byPath[r.Item.Path] = r
	}
	if r := byPath[clean]; r.Status != StatusDone {
		t.Errorf("clean worktree: %+v", r)
	}
	if r := byPath[dirty]; r.Status != StatusSkipped {
		t.Errorf("dirty worktree should be skipped: %+v", r)
	}
	if _, err := os.Stat(clean); !os.IsNotExist(err) {
		t.Errorf("clean worktree still exists")
	}
	if _, err := os.Stat(filepath.Join(dirty, "wip.txt")); err != nil {
		t.Errorf("dirty worktree content lost")
	}
	if out := git(t, main, "worktree", "list", "--porcelain"); contains(out, clean) {
		t.Errorf("git still lists removed worktree:\n%s", out)
	}
	// branches are kept
	if out := git(t, main, "branch", "--list", "wt-clean"); !contains(out, "wt-clean") {
		t.Errorf("branch wt-clean was deleted")
	}

	// --force removes the dirty one
	o := f.opts()
	o.Force = true
	sum = Run(context.Background(), []*core.Item{mkItem(dirty)}, o, nil)
	if sum.Results[0].Status != StatusDone {
		t.Errorf("forced dirty removal: %+v", sum.Results[0])
	}
}

func TestCommandItem(t *testing.T) {
	f := newFixture(t)
	it := &core.Item{ID: "cmd", Kind: "cmd", Name: "echo", Risk: core.RiskSafe, Method: core.MethodCommand, Command: []string{"echo", "hello"}, Selectable: true}
	dup := it.Clone()
	dup.ID = "cmd2"
	sum := Run(context.Background(), []*core.Item{it, dup}, f.opts(), nil)
	if len(sum.Results) != 1 || sum.Results[0].Status != StatusDone || sum.Results[0].Message != "hello" {
		t.Fatalf("%+v", sum.Results)
	}
}

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && (stringIndex(s, sub) >= 0)
}

func stringIndex(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestInodeChangedSinceScan(t *testing.T) {
	f := newFixture(t)
	p := f.mk(t, ".npm/_cacache", "a")
	it := item(p, core.MethodDelete)
	it.Inodes = []uint64{inode(p) + 12345}
	sum := Run(context.Background(), []*core.Item{it}, f.opts(), nil)
	if sum.Results[0].Status != StatusSkipped {
		t.Fatalf("replaced path must be skipped: %+v", sum.Results[0])
	}
	it.Inodes = []uint64{inode(p)}
	sum = Run(context.Background(), []*core.Item{it}, f.opts(), nil)
	if sum.Results[0].Status != StatusDone {
		t.Fatalf("matching inode must be cleaned: %+v", sum.Results[0])
	}
}

func TestOpenSQLiteSkipped(t *testing.T) {
	f := newFixture(t)
	dir := f.mk(t, ".codex")
	db := filepath.Join(dir, "logs_2.sqlite")
	os.WriteFile(db, []byte("x"), 0o644)
	fh, err := os.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	sum := Run(context.Background(), []*core.Item{item(db, core.MethodDelete)}, f.opts(), nil)
	if sum.Results[0].Status != StatusSkipped {
		t.Fatalf("open sqlite must be skipped: %+v", sum.Results[0])
	}
}

func TestWorktreeUnknownUnpushedAndCodexParent(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	f := newFixture(t)
	main := f.mk(t, "src/repo2", "README.md")
	git(t, main, "init", "-q", "-b", "main")
	git(t, main, "add", ".")
	git(t, main, "commit", "-qm", "init")
	parent := f.mk(t, ".codex/worktrees/ab12")
	os.WriteFile(filepath.Join(parent, ".codex-worktree-name"), nil, 0o644)
	wt := filepath.Join(parent, "repo2")
	git(t, main, "worktree", "add", "-q", "-b", "wt-x", wt)

	it := item(wt, core.MethodWorktree)
	it.Project = main
	it.Meta = map[string]string{"unpushed": "?"}
	sum := Run(context.Background(), []*core.Item{it}, f.opts(), nil)
	if sum.Results[0].Status != StatusSkipped {
		t.Fatalf("unknown unpushed must be skipped: %+v", sum.Results[0])
	}
	it.Meta["unpushed"] = "0"
	sum = Run(context.Background(), []*core.Item{it}, f.opts(), nil)
	if sum.Results[0].Status != StatusDone {
		t.Fatalf("%+v", sum.Results[0])
	}
	if _, err := os.Stat(parent); !os.IsNotExist(err) {
		t.Errorf("empty codex task folder should be removed")
	}
}
