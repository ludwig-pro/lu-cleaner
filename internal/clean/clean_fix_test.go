package clean

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
	"github.com/ludwig-pro/lu-cleaner/internal/sysx"
	"golang.org/x/sys/unix"
)

func needGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

// newRepo creates a main repository with one commit under home/rel.
func (f *fixture) newRepo(t *testing.T, rel string, files map[string]string) string {
	t.Helper()
	main := f.mk(t, rel, "README.md")
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(main, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, main, "init", "-q", "-b", "main")
	git(t, main, "add", ".")
	git(t, main, "commit", "-qm", "init")
	return main
}

func wtItem(p, main string) *core.Item {
	it := item(p, core.MethodWorktree)
	it.Project = main
	it.Risk = core.RiskModerate
	return it
}

func byPath(sum *Summary) map[string]Result {
	m := map[string]Result{}
	for _, r := range sum.Results {
		m[r.Item.Path] = r
	}
	return m
}

func mustExist(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Lstat(p); err != nil {
		t.Errorf("%s was removed: %v", p, err)
	}
}

func mustBeGone(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Lstat(p); !os.IsNotExist(err) {
		t.Errorf("%s still exists (%v)", p, err)
	}
}

// --dry-run runs the same worktree checks as the real run and reports the
// same skips (finding dryrun-skips-worktree-checks).
func TestDryRunRunsWorktreeChecks(t *testing.T) {
	needGit(t)
	f := newFixture(t)
	main := f.newRepo(t, "src/repo", nil)
	root := f.mk(t, ".codex/worktrees/a1")
	dirty, det, locked, clean := filepath.Join(root, "dirty"), filepath.Join(root, "det"), filepath.Join(root, "locked"), filepath.Join(root, "clean")
	git(t, main, "worktree", "add", "-q", "-b", "wt-dirty", dirty)
	os.WriteFile(filepath.Join(dirty, "wip.txt"), []byte("wip"), 0o644)
	git(t, main, "worktree", "add", "-q", "--detach", det)
	os.WriteFile(filepath.Join(det, "x.txt"), []byte("x"), 0o644)
	git(t, det, "add", ".")
	git(t, det, "commit", "-qm", "detached work")
	git(t, main, "worktree", "add", "-q", "-b", "wt-locked", locked)
	git(t, main, "worktree", "lock", locked) // no Meta: the lock is read live
	git(t, main, "worktree", "add", "-q", "-b", "wt-clean", clean)
	items := func() []*core.Item {
		return []*core.Item{wtItem(dirty, main), wtItem(det, main), wtItem(locked, main), wtItem(clean, main)}
	}
	o := f.opts()
	o.DryRun = true
	dry := byPath(Run(context.Background(), items(), o, nil))
	want := map[string]string{dirty: "uncommitted change", det: "on no branch", locked: "locked"}
	for p, msg := range want {
		if r := dry[p]; r.Status != StatusSkipped || !strings.Contains(r.Message, msg) {
			t.Errorf("dry-run %s: %v %q, want skipped %q", filepath.Base(p), r.Status, r.Message, msg)
		}
	}
	if r := dry[clean]; r.Status != StatusDryRun {
		t.Errorf("dry-run clean worktree: %+v", r)
	}
	real := byPath(Run(context.Background(), items(), f.opts(), nil))
	for p := range want {
		if real[p].Status != StatusSkipped || real[p].Message != dry[p].Message {
			t.Errorf("%s: real run %v %q, dry run %v %q", filepath.Base(p), real[p].Status, real[p].Message, dry[p].Status, dry[p].Message)
		}
		mustExist(t, p)
	}
	if real[clean].Status != StatusDone {
		t.Errorf("clean worktree: %+v", real[clean])
	}
}

// status.showUntrackedFiles=no must not hide untracked work, neither from our
// check nor from git's own (finding show-untracked-no-bypass).
func TestShowUntrackedFilesNoDoesNotHideWork(t *testing.T) {
	needGit(t)
	f := newFixture(t)
	main := f.newRepo(t, "src/big", nil)
	git(t, main, "config", "status.showUntrackedFiles", "no")
	w := filepath.Join(f.mk(t, ".codex/worktrees/b2"), "big-w")
	git(t, main, "worktree", "add", "-q", "-b", "feat", w)
	newFile := filepath.Join(w, "pkg/new_feature.go")
	os.MkdirAll(filepath.Dir(newFile), 0o755)
	os.WriteFile(newFile, []byte("package pkg"), 0o644)

	r := Run(context.Background(), []*core.Item{wtItem(w, main)}, f.opts(), nil).Results[0]
	if r.Status != StatusSkipped || !strings.Contains(r.Message, "uncommitted") {
		t.Fatalf("worktree with untracked file: %+v", r)
	}
	mustExist(t, newFile)

	// git's own refusal (second line of defence) sees it too.
	msg, err := removeWorktree(context.Background(), wtItem(w, main), Options{Runner: core.ExecRunner{}, Guard: f.g, Home: f.home},
		&wtPlan{main: main, mainOK: true})
	if err == nil {
		t.Fatalf("git worktree remove accepted a worktree with untracked files: %s", msg)
	}
	mustExist(t, newFile)
}

// A worktree containing another worktree (in a gitignored folder) is not
// removed along with it; selecting both removes the inner one first
// (finding nested-worktree-destroyed).
func TestNestedWorktreeProtected(t *testing.T) {
	needGit(t)
	f := newFixture(t)
	main := f.newRepo(t, "src/app", map[string]string{".gitignore": ".claude/worktrees/\n"})
	a := filepath.Join(f.home, "wt/app-A")
	git(t, main, "worktree", "add", "-q", "-b", "feat-a", a)
	b := filepath.Join(a, ".claude/worktrees/B")
	git(t, main, "worktree", "add", "-q", "-b", "feat-b", b)
	wip := filepath.Join(b, "wip.txt")
	os.WriteFile(wip, []byte("precious"), 0o644)

	r := Run(context.Background(), []*core.Item{wtItem(a, main)}, f.opts(), nil).Results[0]
	if r.Status != StatusSkipped || !strings.Contains(r.Message, "contains another worktree") {
		t.Fatalf("outer worktree alone: %+v", r)
	}
	mustExist(t, wip)

	// Both selected, inner one dirty: both kept.
	res := byPath(Run(context.Background(), []*core.Item{wtItem(a, main), wtItem(b, main)}, f.opts(), nil))
	if res[b].Status != StatusSkipped || res[a].Status != StatusSkipped || !strings.Contains(res[a].Message, "nested inside it") {
		t.Fatalf("both selected, inner dirty: A=%+v B=%+v", res[a], res[b])
	}
	mustExist(t, wip)

	// Inner one clean: dry-run and real run remove B then A.
	os.Remove(wip)
	o := f.opts()
	o.DryRun = true
	res = byPath(Run(context.Background(), []*core.Item{wtItem(a, main), wtItem(b, main)}, o, nil))
	if res[a].Status != StatusDryRun || res[b].Status != StatusDryRun {
		t.Fatalf("dry-run, both clean: A=%+v B=%+v", res[a], res[b])
	}
	sum := Run(context.Background(), []*core.Item{wtItem(a, main), wtItem(b, main)}, f.opts(), nil)
	res = byPath(sum)
	if res[a].Status != StatusDone || res[b].Status != StatusDone || sum.Results[0].Item.Path != b {
		t.Fatalf("both clean: %+v", sum.Results)
	}
	mustBeGone(t, a)

	// An unregistered clone in an ignored folder also blocks the removal.
	a2 := filepath.Join(f.home, "wt/app-A2")
	git(t, main, "worktree", "add", "-q", "-b", "feat-a2", a2)
	clone := filepath.Join(a2, ".claude/worktrees/scratch")
	os.MkdirAll(clone, 0o755)
	git(t, clone, "init", "-q")
	os.WriteFile(filepath.Join(clone, "notes.md"), []byte("notes"), 0o644)
	r = Run(context.Background(), []*core.Item{wtItem(a2, main)}, f.opts(), nil).Results[0]
	if r.Status != StatusSkipped || !strings.Contains(r.Message, "contains the git repository") {
		t.Fatalf("worktree holding a clone: %+v", r)
	}
	mustExist(t, filepath.Join(clone, "notes.md"))
	// --force removes it (explicit consent).
	o = f.opts()
	o.Force = true
	if r := Run(context.Background(), []*core.Item{wtItem(a2, main)}, o, nil).Results[0]; r.Status != StatusDone {
		t.Fatalf("forced: %+v", r)
	}
}

// A main repository that cannot be read is never taken for a missing one;
// a missing one needs --force (cross-cutting decision 5).
func TestWorktreeMainRepoUnreadableOrMissing(t *testing.T) {
	needGit(t)
	f := newFixture(t)
	main := f.newRepo(t, "vault/repo", nil)
	w := filepath.Join(f.mk(t, ".codex/worktrees/c3"), "repo")
	git(t, main, "worktree", "add", "-q", "-b", "feat", w)
	vault := filepath.Dir(main)
	if err := os.Chmod(vault, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(vault, 0o755) })
	for _, force := range []bool{false, true} {
		o := f.opts()
		o.Force = force
		r := Run(context.Background(), []*core.Item{wtItem(w, main)}, o, nil).Results[0]
		if r.Status != StatusSkipped || !strings.Contains(r.Message, "unreadable") {
			t.Fatalf("force=%v, unreadable main: %+v", force, r)
		}
		mustExist(t, filepath.Join(w, "README.md"))
	}
	os.Chmod(vault, 0o755)

	// Confirmed missing: refused without --force, orphan removal with it.
	if err := os.RemoveAll(main); err != nil {
		t.Fatal(err)
	}
	r := Run(context.Background(), []*core.Item{wtItem(w, main)}, f.opts(), nil).Results[0]
	if r.Status != StatusSkipped || !strings.Contains(r.Message, "no longer exists") {
		t.Fatalf("missing main without --force: %+v", r)
	}
	mustExist(t, filepath.Join(w, "README.md"))
	o := f.opts()
	o.Force = true
	r = Run(context.Background(), []*core.Item{wtItem(w, main)}, o, nil).Results[0]
	if r.Status != StatusDone || !strings.Contains(r.Message, "orphaned") {
		t.Fatalf("missing main with --force: %+v", r)
	}
	mustBeGone(t, w)
}

// The empty per-task folder of a tool is removed; an empty folder of the
// user is kept (finding empty-codex-task-dir-left).
func TestEmptyToolParentRemoved(t *testing.T) {
	needGit(t)
	f := newFixture(t)
	main := f.newRepo(t, "src/repo", nil)
	task := f.mk(t, ".codex/worktrees/e5f6")
	codex := filepath.Join(task, "repo")
	manualDir := f.mk(t, "src/wts")
	manual := filepath.Join(manualDir, "feat")
	dsDir := f.mk(t, "src/wts2", ".DS_Store")
	ds := filepath.Join(dsDir, "feat2")
	git(t, main, "worktree", "add", "-q", "-b", "c", codex)
	git(t, main, "worktree", "add", "-q", "-b", "m", manual)
	git(t, main, "worktree", "add", "-q", "-b", "d", ds)
	sum := Run(context.Background(), []*core.Item{wtItem(codex, main), wtItem(manual, main), wtItem(ds, main)}, f.opts(), nil)
	for _, r := range sum.Results {
		if r.Status != StatusDone {
			t.Fatalf("%+v", r)
		}
	}
	mustBeGone(t, task)
	mustExist(t, manualDir)
	mustExist(t, dsDir)
}

// History keeps the paths of group items and the reason of skips (findings
// history-group-paths, history-and-table-output).
func TestHistoryRecordsGroupPathsAndReasons(t *testing.T) {
	f := newFixture(t)
	a := f.mk(t, ".cache/metro/a", "x")
	b := f.mk(t, ".cache/metro/b", "x")
	group := &core.Item{ID: "g", Kind: "metro", Name: "Metro caches (2 dirs)", Paths: []string{a, b}, Location: "~/.cache/metro/…",
		Size: 20, Risk: core.RiskSafe, Method: core.MethodDelete, Selectable: true}
	build := f.mk(t, "src/other/build", "x")
	skipped := item(build, core.MethodDelete)
	skipped.RequireSibling = []string{"build.gradle"}
	o := f.opts()
	o.NoHistory = false
	Run(context.Background(), []*core.Item{group, skipped}, o, nil)
	hist, err := ReadHistory()
	if err != nil || len(hist) != 2 {
		t.Fatalf("history: %v %+v", err, hist)
	}
	for _, e := range hist {
		switch e.Name {
		case group.Name:
			if e.Location != group.Location || e.Count != 2 || len(e.Paths) != 2 || e.Paths[0] != a || e.Status != "done" || e.Method != "delete" {
				t.Errorf("group entry: %+v", e)
			}
		default:
			if e.Status != "skipped" || !strings.Contains(e.Message, "marker") || e.Path != build {
				t.Errorf("skipped entry: %+v", e)
			}
		}
	}
}

// A read-only parent is detected before anything is deleted (finding
// partial-delete-accounting).
func TestReadOnlyParentNothingTouched(t *testing.T) {
	f := newFixture(t)
	proj := f.mk(t, "src/rofail", "package.json")
	nm := f.mk(t, "src/rofail/node_modules/y", "b")
	os.Chmod(proj, 0o555)
	t.Cleanup(func() { os.Chmod(proj, 0o755) })
	it := item(filepath.Dir(nm), core.MethodDelete)
	it.RequireSibling = []string{"package.json"}
	for _, dry := range []bool{true, false} {
		o := f.opts()
		o.DryRun = dry
		r := Run(context.Background(), []*core.Item{it}, o, nil).Results[0]
		if r.Status != StatusSkipped || !strings.Contains(r.Message, "read-only") {
			t.Fatalf("dry=%v: %+v", dry, r)
		}
	}
	mustExist(t, filepath.Join(nm, "b"))
}

// A deletion that fails half way reports what it freed; the retry never
// clears flags of an inode hard-linked outside the target (findings
// partial-delete-accounting, removeall-retry-crosses-mounts-zeroes-compressed).
func TestPartialDeleteAccounting(t *testing.T) {
	f := newFixture(t)
	target := f.mk(t, ".cache/partial")
	if err := os.WriteFile(filepath.Join(target, "big.bin"), bytes.Repeat([]byte{1}, 256<<10), 0o644); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(f.mk(t, "outside"), "keep.txt")
	os.WriteFile(keep, []byte("shared"), 0o644)
	link := filepath.Join(f.mk(t, ".cache/partial/ro"), "link.txt")
	if err := os.Link(keep, link); err != nil {
		t.Fatal(err)
	}
	if err := unix.Chflags(keep, unix.UF_IMMUTABLE); err != nil {
		t.Skipf("chflags: %v", err)
	}
	t.Cleanup(func() { unix.Chflags(keep, 0); unix.Chflags(link, 0) })
	st, _ := fsx.Size(context.Background(), target, nil)
	it := item(target, core.MethodDelete)
	it.Size = st.Bytes
	sum := Run(context.Background(), []*core.Item{it}, f.opts(), nil)
	r := sum.Results[0]
	if r.Status != StatusFailed || r.Freed <= 0 || !strings.Contains(r.Message, "partially removed") || sum.Estimated != r.Freed {
		t.Fatalf("partial failure: %+v (estimated %d)", r, sum.Estimated)
	}
	var kst unix.Stat_t
	unix.Lstat(keep, &kst)
	if kst.Flags&unix.UF_IMMUTABLE == 0 {
		t.Errorf("flags of a file hard-linked outside the target were cleared")
	}
}

// compressedFile creates an APFS/HFS+-compressed file (skips when impossible).
func compressedFile(t *testing.T, dst string) []byte {
	t.Helper()
	data := bytes.Repeat([]byte("lu-cleaner compressed content "), 8000)
	src := filepath.Join(t.TempDir(), "src.txt")
	os.WriteFile(src, data, 0o644)
	if out, err := exec.Command("/usr/bin/ditto", "--hfsCompression", src, dst).CombinedOutput(); err != nil {
		t.Skipf("ditto: %v %s", err, out)
	}
	var st unix.Stat_t
	if unix.Lstat(dst, &st) != nil || st.Flags&unix.UF_COMPRESSED == 0 {
		t.Skip("filesystem does not compress")
	}
	return data
}

func TestRemoveAllKeepsCompressionOfOtherFiles(t *testing.T) {
	f := newFixture(t)
	keep := filepath.Join(f.mk(t, "outside"), "keep.txt")
	data := compressedFile(t, keep)
	target := f.mk(t, "old-stuff")
	ro := f.mk(t, "old-stuff/ro")
	if err := os.Link(keep, filepath.Join(ro, "link.txt")); err != nil {
		t.Fatal(err)
	}
	os.Chmod(ro, 0o555)
	if err := RemoveAll(target); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	mustBeGone(t, target)
	if got, _ := os.ReadFile(keep); !bytes.Equal(got, data) {
		t.Fatalf("hard-linked compressed file damaged: %d bytes left of %d", len(got), len(data))
	}

	// Single file in a read-only dir: removal fails, the file is intact.
	dir := f.mk(t, "rodir")
	file := filepath.Join(dir, "c.txt")
	data = compressedFile(t, file)
	os.Chmod(dir, 0o555)
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	if err := RemoveAll(file); err == nil {
		t.Fatalf("removed a file from a read-only dir")
	}
	if got, _ := os.ReadFile(file); !bytes.Equal(got, data) {
		t.Fatalf("compressed file emptied by a failed removal: %d bytes", len(got))
	}
}

// Moves to the Trash never overwrite each other (finding trash-collision-overwrite).
func TestTrashNeverOverwrites(t *testing.T) {
	f := newFixture(t)
	var items []*core.Item
	var group []string
	for i := range 16 {
		d := f.mk(t, fmt.Sprintf("Library/Caches/app%d", i))
		p := filepath.Join(d, "state.json")
		os.WriteFile(p, []byte(fmt.Sprint(i)), 0o644)
		items = append(items, item(p, core.MethodDelete))
		g := filepath.Join(f.mk(t, fmt.Sprintf(".cache/tool%d", i)), "state.json")
		os.WriteFile(g, []byte(fmt.Sprint(100+i)), 0o644)
		group = append(group, g)
	}
	items = append(items, &core.Item{ID: "group", Kind: "g", Name: "group", Paths: group, Size: 16, Risk: core.RiskSafe, Method: core.MethodDelete, Selectable: true})
	o := f.opts()
	o.Trash = true
	o.Parallel = 8
	sum := Run(context.Background(), items, o, nil)
	if n := sum.Count(StatusDone); n != 17 {
		t.Fatalf("%d done: %+v", n, sum.Results)
	}
	entries, _ := os.ReadDir(filepath.Join(f.home, ".Trash"))
	seen := map[string]bool{}
	for _, e := range entries {
		b, _ := os.ReadFile(filepath.Join(f.home, ".Trash", e.Name()))
		seen[string(b)] = true
	}
	if len(entries) != 32 || len(seen) != 32 {
		t.Fatalf("%d entries, %d distinct contents in the Trash, want 32", len(entries), len(seen))
	}
}

// Trash mode: worktrees and commands are skipped (they would delete
// permanently), the Trash itself is not "moved to the Trash", and Trash moves
// are never counted as freed (findings trash-not-honored, trash-mode-on-trash-item).
func TestTrashModeSkipsPermanentAndTrashedItems(t *testing.T) {
	needGit(t)
	f := newFixture(t)
	main := f.newRepo(t, "src/repo", map[string]string{".gitignore": "node_modules/\n.env\n"})
	w := filepath.Join(f.mk(t, ".codex/worktrees/t1"), "repo")
	git(t, main, "worktree", "add", "-q", "-b", "feat", w)
	os.WriteFile(filepath.Join(w, ".env"), []byte("SECRET=1"), 0o644)
	nm := filepath.Join(w, "node_modules")
	os.MkdirAll(filepath.Join(nm, "x"), 0o755)
	os.WriteFile(filepath.Join(w, "package.json"), []byte("{}"), 0o644) // untracked: keeps git from removing it anyway
	oldProject := f.mk(t, ".Trash/old-project", "a")
	report := filepath.Join(f.home, ".Trash/report.pdf")
	os.WriteFile(report, []byte("pdf"), 0o644)
	cache := f.mk(t, "Library/Caches/foo", "a")

	trashItem := &core.Item{ID: "trash", Kind: "trash", Name: "Trash", Paths: []string{oldProject, report}, Size: 1000,
		Risk: core.RiskModerate, Method: core.MethodDelete, Selectable: true, AllowGitRepo: true}
	cmd := &core.Item{ID: "cmd", Kind: "cmd", Name: "empty", Risk: core.RiskSafe, Method: core.MethodCommand, Command: []string{"echo", "hi"}, Selectable: true}
	cacheItem := item(cache, core.MethodDelete)
	cacheItem.Size = 4096
	nmItem := item(nm, core.MethodDelete)
	o := f.opts()
	o.Trash = true
	sum := Run(context.Background(), []*core.Item{wtItem(w, main), nmItem, cmd, trashItem, cacheItem}, o, nil)
	got := map[string]Result{}
	for _, r := range sum.Results {
		got[r.Item.ID] = r
	}
	if r := got[w]; r.Status != StatusSkipped || r.Message != msgTrashPermanent {
		t.Errorf("worktree: %+v", r)
	}
	if r := got["cmd"]; r.Status != StatusSkipped || r.Message != msgTrashPermanent {
		t.Errorf("command: %+v", r)
	}
	if r := got["trash"]; r.Status != StatusSkipped || r.Message != msgAlreadyTrashed {
		t.Errorf("trash item: %+v", r)
	}
	if r := got[nm]; r.Status != StatusDone || r.Method != core.MethodTrash {
		t.Errorf("node_modules inside the skipped worktree: %+v", r)
	}
	if r := got[cache]; r.Status != StatusDone || r.Freed != 0 || r.Trashed != 4096 || r.Method != core.MethodTrash {
		t.Errorf("cache: %+v", r)
	}
	if sum.Estimated != 0 || sum.Trashed != 4096+10 {
		t.Errorf("summary: estimated %d, trashed %d", sum.Estimated, sum.Trashed)
	}
	mustExist(t, filepath.Join(w, ".env"))
	mustExist(t, oldProject)
	mustExist(t, report)
	if _, err := MoveToTrash(f.home, report); err == nil {
		t.Errorf("MoveToTrash accepted a path already in the Trash")
	}
}

// RequireSibling treats the project directory literally (finding require-sibling-glob).
func TestRequireSiblingLiteralDir(t *testing.T) {
	f := newFixture(t)
	f.mk(t, "dev/[wip] shop", "package.json")
	nm := f.mk(t, "dev/[wip] shop/node_modules", "x")
	f.mk(t, "dev2/app1", "package.json")
	f.mk(t, "dev2/app?")
	nm2 := f.mk(t, "dev2/app?/node_modules", "x")
	var items []*core.Item
	for _, p := range []string{nm, nm2} {
		it := item(p, core.MethodDelete)
		it.RequireSibling = []string{"package.json"}
		items = append(items, it)
	}
	o := f.opts()
	o.DryRun = true
	res := byPath(Run(context.Background(), items, o, nil))
	if res[nm].Status != StatusDryRun {
		t.Errorf("[wip] shop: %+v", res[nm])
	}
	if res[nm2].Status != StatusSkipped {
		t.Errorf("app? accepted through app1/package.json: %+v", res[nm2])
	}
}

// Busy checks and the current-directory check work when the item path is
// spelled with another case than on disk (finding case-mismatch-inuse-bypass).
func TestInUseDetectedDespiteCaseMismatch(t *testing.T) {
	f := newFixture(t)
	disk := f.mk(t, "Code/app-wt", "a")
	typed := filepath.Join(f.home, "code/app-wt")
	if _, err := os.Stat(typed); err != nil {
		t.Skip("case-sensitive filesystem")
	}
	if got := onDiskPath(typed); got != disk {
		t.Fatalf("onDiskPath(%s) = %q, want %q", typed, got, disk)
	}

	// Current directory.
	cwdDir := f.mk(t, "Code/cwd-proj", "a")
	t.Chdir(cwdDir)
	r := Run(context.Background(), []*core.Item{item(filepath.Join(f.home, "code/cwd-proj"), core.MethodDelete)}, f.opts(), nil).Results[0]
	if r.Status != StatusSkipped || !strings.Contains(r.Message, "current directory") {
		t.Fatalf("cwd with another case: %+v", r)
	}
	t.Chdir(f.home)

	// A process working inside.
	cmd := exec.Command("/bin/sleep", "30")
	cmd.Dir = disk
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	deadline := time.Now().Add(8 * time.Second)
	for sysx.CwdInside(disk) == "" {
		if time.Now().After(deadline) {
			t.Skip("process cwd not visible (lsof/proc_info unavailable)")
		}
		time.Sleep(200 * time.Millisecond)
	}
	r = Run(context.Background(), []*core.Item{item(typed, core.MethodDelete)}, f.opts(), nil).Results[0]
	if r.Status != StatusSkipped || !strings.Contains(r.Message, "in use") {
		t.Fatalf("process inside, other case: %+v", r)
	}
	mustExist(t, filepath.Join(disk, "a"))
}

// A bare-repo layout (.bare + .git file) is never rm -rf'd as a plain folder.
func TestBareLayoutRepoNotDeleted(t *testing.T) {
	f := newFixture(t)
	repo := f.mk(t, "dev/myrepo/.bare/objects")
	f.mk(t, "dev/myrepo/.bare/refs")
	os.WriteFile(filepath.Join(filepath.Dir(repo), "HEAD"), []byte("ref: refs/heads/main\n"), 0o644)
	root := filepath.Join(f.home, "dev/myrepo")
	os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: ./.bare\n"), 0o644)
	it := item(root, core.MethodDelete)
	it.Risk = core.RiskCaution
	if r := Run(context.Background(), []*core.Item{it}, f.opts(), nil).Results[0]; r.Status != StatusSkipped {
		t.Fatalf("bare layout: %+v", r)
	}
	mustExist(t, repo)
	// Even as a "worktree" item: its git data lives inside it.
	wt := wtItem(root, root)
	if r := Run(context.Background(), []*core.Item{wt}, f.opts(), nil).Results[0]; r.Status != StatusSkipped {
		t.Fatalf("bare layout as a worktree: %+v", r)
	}
	mustExist(t, repo)
}

func TestCountStatus(t *testing.T) {
	out := []byte("?? a.txt\x00R  new.go\x00old.go\x00 M b.go\x00")
	if n := countStatus(out); n != 3 {
		t.Fatalf("countStatus = %d, want 3", n)
	}
	if n := countStatus(nil); n != 0 {
		t.Fatalf("empty = %d", n)
	}
}

// Only a clean ENOENT under a readable, mounted parent means "missing"
// (cross-cutting decision 5).
func TestProbeMissingVersusUnreadable(t *testing.T) {
	f := newFixture(t)
	gone := filepath.Join(f.home, "src/gone/repo")
	f.mk(t, "src")
	locked := f.mk(t, "locked")
	os.Chmod(locked, 0o000)
	t.Cleanup(func() { os.Chmod(locked, 0o755) })
	dangling := filepath.Join(f.home, "link-to-volume")
	os.Symlink("/Volumes/lu-cleaner-test-not-mounted/repo", dangling)
	cases := map[string]pathState{
		f.home:                         pathExists,
		gone:                           pathMissing,
		filepath.Join(locked, "repo"):  pathUnreadable,
		dangling:                       pathUnreadable,
		filepath.Join(dangling, "sub"): pathUnreadable,
		"/Volumes/lu-cleaner-test-not-mounted/src/repo": pathUnreadable,
		"": pathUnreadable,
	}
	for p, want := range cases {
		if got, err := probe(p); got != want {
			t.Errorf("probe(%q) = %v (%v), want %v", p, got, err, want)
		}
	}
}

// A mounted volume inside a target is never entered nor modified (finding
// removeall-retry-crosses-mounts-zeroes-compressed). It mounts a small disk
// image, so it only runs with LU_TEST_MOUNT=1.
func TestMountPointNeverEntered(t *testing.T) {
	if os.Getenv("LU_TEST_MOUNT") != "1" {
		t.Skip("set LU_TEST_MOUNT=1 to mount a test disk image")
	}
	f := newFixture(t)
	img := filepath.Join(t.TempDir(), "lutest.dmg")
	if out, err := exec.Command("/usr/bin/hdiutil", "create", "-quiet", "-size", "8m", "-fs", "APFS", "-volname", "lutest", img).CombinedOutput(); err != nil {
		t.Skipf("hdiutil create: %v %s", err, out)
	}
	mp := f.mk(t, "old-stuff/nas")
	f.mk(t, "old-stuff/other", "x")
	if out, err := exec.Command("/usr/bin/hdiutil", "attach", "-quiet", "-nobrowse", "-noverify", "-mountpoint", mp, img).CombinedOutput(); err != nil {
		t.Skipf("hdiutil attach: %v %s", err, out)
	}
	locked := filepath.Join(mp, "locked.txt")
	t.Cleanup(func() {
		unix.Chflags(locked, 0)
		exec.Command("/usr/bin/hdiutil", "detach", "-quiet", "-force", mp).Run()
	})
	data := compressedFile(t, filepath.Join(mp, "compressed.txt"))
	private := filepath.Join(mp, "private")
	os.Mkdir(private, 0o700)
	os.WriteFile(locked, []byte("l"), 0o644)
	unix.Chflags(locked, unix.UF_IMMUTABLE)

	// Refused before anything is touched (mount table), in every mode.
	for _, o := range []Options{f.opts(), {Guard: f.g, Home: f.home, NoHistory: true, DryRun: true}, {Guard: f.g, Home: f.home, NoHistory: true, Trash: true}} {
		r := Run(context.Background(), []*core.Item{item(filepath.Join(f.home, "old-stuff"), core.MethodDelete)}, o, nil).Results[0]
		if r.Status != StatusSkipped || !strings.Contains(r.Message, "mount point") {
			t.Errorf("tree holding a mount point (dry=%v trash=%v): %+v", o.DryRun, o.Trash, r)
		}
	}
	mustExist(t, filepath.Join(f.home, "old-stuff/other/x"))
	r := Run(context.Background(), []*core.Item{item(mp, core.MethodDelete)}, f.opts(), nil).Results[0]
	if r.Status != StatusSkipped || !strings.Contains(r.Message, "mount point") {
		t.Errorf("mount point itself: %+v", r)
	}
	// RemoveAll itself (defence in depth) never enters it either.
	if err := RemoveAll(filepath.Join(f.home, "old-stuff")); err == nil || !strings.Contains(err.Error(), "mount point") {
		t.Errorf("RemoveAll of a tree holding a mount point: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(mp, "compressed.txt")); !bytes.Equal(got, data) {
		t.Errorf("compressed file on the mounted volume changed: %d bytes", len(got))
	}
	if fi, err := os.Stat(private); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("private dir on the mounted volume changed: %v %v", fi.Mode(), err)
	}
	var st unix.Stat_t
	if unix.Lstat(locked, &st) != nil || st.Flags&unix.UF_IMMUTABLE == 0 {
		t.Errorf("uchg flag cleared on the mounted volume")
	}
	mustBeGone(t, filepath.Join(f.home, "old-stuff/other"))

	// A worktree holding a mounted volume: git would wipe it, even --force.
	if _, err := exec.LookPath("git"); err == nil {
		main := f.newRepo(t, "src/repo", map[string]string{".gitignore": "mnt/\n"})
		w := filepath.Join(f.mk(t, ".codex/worktrees/mt"), "repo")
		git(t, main, "worktree", "add", "-q", "-b", "feat", w)
		wmp := filepath.Join(w, "mnt")
		os.Mkdir(wmp, 0o755)
		if out, err := exec.Command("/usr/bin/hdiutil", "detach", "-quiet", "-force", mp).CombinedOutput(); err != nil {
			t.Fatalf("hdiutil detach: %v %s", err, out)
		}
		if out, err := exec.Command("/usr/bin/hdiutil", "attach", "-quiet", "-nobrowse", "-noverify", "-mountpoint", wmp, img).CombinedOutput(); err != nil {
			t.Fatalf("hdiutil attach: %v %s", err, out)
		}
		t.Cleanup(func() { exec.Command("/usr/bin/hdiutil", "detach", "-quiet", "-force", wmp).Run() })
		o := f.opts()
		o.Force = true
		if r := Run(context.Background(), []*core.Item{wtItem(w, main)}, o, nil).Results[0]; r.Status != StatusSkipped || !strings.Contains(r.Message, "mount point") {
			t.Errorf("worktree holding a mount point: %+v", r)
		}
		if got, _ := os.ReadFile(filepath.Join(wmp, "compressed.txt")); !bytes.Equal(got, data) {
			t.Errorf("volume mounted in a worktree changed: %d bytes", len(got))
		}
	}
}

// A target holding a mount point is refused before anything is touched, for
// every method and in dry-run (reverification of
// removeall-retry-crosses-mounts-zeroes-compressed: git worktree remove and
// rm -rf would wipe the mounted volume). Uses a fake mount table.
func TestTargetHoldingMountPointRefused(t *testing.T) {
	needGit(t)
	f := newFixture(t)
	old := f.mk(t, "Old-Stuff/other", "x")
	fakeMnt := f.mk(t, "Old-Stuff/Nas", "data")
	main := f.newRepo(t, "src/repo", map[string]string{".gitignore": "mnt/\n"})
	w := filepath.Join(f.mk(t, ".codex/worktrees/fm"), "repo")
	git(t, main, "worktree", "add", "-q", "-b", "feat", w)
	wMnt := filepath.Join(w, "mnt")
	os.Mkdir(wMnt, 0o755)
	saved := mountPoints
	t.Cleanup(func() { mountPoints = saved })
	mountPoints = func() ([]string, error) { return []string{"/", "/System/Volumes/Data", fakeMnt, wMnt}, nil }

	typed := filepath.Join(f.home, "old-stuff") // other case than on disk
	for _, o := range []Options{f.opts(), {Guard: f.g, Home: f.home, NoHistory: true, DryRun: true}, {Guard: f.g, Home: f.home, NoHistory: true, Trash: true}} {
		for _, p := range []string{filepath.Dir(old), typed} {
			if _, err := os.Stat(p); err != nil {
				continue // case-sensitive filesystem
			}
			r := Run(context.Background(), []*core.Item{item(p, core.MethodDelete)}, o, nil).Results[0]
			if r.Status != StatusSkipped || !strings.Contains(r.Message, "mount point") {
				t.Errorf("%s (dry=%v trash=%v): %+v", p, o.DryRun, o.Trash, r)
			}
		}
	}
	mustExist(t, filepath.Join(old, "x"))
	// The mount point's siblings and the mount point's own contents are fine to clean.
	if r := Run(context.Background(), []*core.Item{item(old, core.MethodDelete)}, f.opts(), nil).Results[0]; r.Status != StatusDone {
		t.Errorf("sibling of a mount point: %+v", r)
	}
	for _, force := range []bool{false, true} {
		o := f.opts()
		o.Force = force
		if r := Run(context.Background(), []*core.Item{wtItem(w, main)}, o, nil).Results[0]; r.Status != StatusSkipped || !strings.Contains(r.Message, "mount point") {
			t.Errorf("worktree holding a mount point (force=%v): %+v", force, r)
		}
	}
	mustExist(t, filepath.Join(w, "README.md"))
	// Unknown mount table: fail closed.
	mountPoints = func() ([]string, error) { return nil, fmt.Errorf("getfsstat: boom") }
	if r := Run(context.Background(), []*core.Item{item(fakeMnt, core.MethodDelete)}, f.opts(), nil).Results[0]; r.Status != StatusSkipped {
		t.Errorf("mount table unreadable: %+v", r)
	}
}

// A copied or moved worktree folder is refused by our checks, identically in
// dry-run and real run (git would refuse it: "is not a working tree"), and a
// .git file pointing to an admin dir that is not a linked worktree's is
// refused (reverification of dryrun-skips-worktree-checks, guard-gitfile-bypass).
func TestCopiedOrMovedWorktreeRefusedInDryRunToo(t *testing.T) {
	needGit(t)
	f := newFixture(t)
	main := f.newRepo(t, "src/repo", nil)
	w := filepath.Join(f.mk(t, ".codex/worktrees/x1"), "repo")
	git(t, main, "worktree", "add", "-q", "-b", "feat", w)
	cp := filepath.Join(f.mk(t, "copies"), "repo-copy")
	if out, err := exec.Command("/bin/cp", "-R", w, cp).CombinedOutput(); err != nil {
		t.Fatalf("cp: %v %s", err, out)
	}
	moved := filepath.Join(f.mk(t, "moved"), "repo")
	w2 := filepath.Join(f.mk(t, ".codex/worktrees/x2"), "repo")
	git(t, main, "worktree", "add", "-q", "-b", "feat2", w2)
	if err := os.Rename(w2, moved); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{cp, moved} {
		o := f.opts()
		o.DryRun = true
		dry := Run(context.Background(), []*core.Item{wtItem(p, main)}, o, nil).Results[0]
		real := Run(context.Background(), []*core.Item{wtItem(p, main)}, f.opts(), nil).Results[0]
		if dry.Status != StatusSkipped || !strings.Contains(dry.Message, "git records this worktree at") || real.Message != dry.Message {
			t.Errorf("%s: dry %v %q, real %v %q", p, dry.Status, dry.Message, real.Status, real.Message)
		}
		mustExist(t, filepath.Join(p, "README.md"))
	}
	// The original worktree is still removable.
	if r := Run(context.Background(), []*core.Item{wtItem(w, main)}, f.opts(), nil).Results[0]; r.Status != StatusDone {
		t.Fatalf("original worktree: %+v", r)
	}

	// An admin dir named worktrees/<x> without commondir is not a linked worktree's.
	adm := f.mk(t, "src/other/worktrees/x")
	os.WriteFile(filepath.Join(adm, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644)
	fake := f.mk(t, "dev/fake", "a")
	os.WriteFile(filepath.Join(fake, ".git"), []byte("gitdir: "+adm+"\n"), 0o644)
	o := f.opts()
	o.Force = true
	if r := Run(context.Background(), []*core.Item{wtItem(fake, "")}, o, nil).Results[0]; r.Status != StatusSkipped || !strings.Contains(r.Message, "no commondir") {
		t.Fatalf("admin dir without commondir: %+v", r)
	}
	mustExist(t, filepath.Join(fake, "a"))
}

// A registered nested worktree whose folder is already gone no longer blocks
// the outer one; SwiftPM's .build clones do not either (reverification of
// nested-worktree-destroyed).
func TestNestedCheckIgnoresGoneWorktreeAndSwiftPMBuild(t *testing.T) {
	needGit(t)
	f := newFixture(t)
	main := f.newRepo(t, "src/app", map[string]string{".gitignore": ".claude/worktrees/\n.build/\n"})
	a := filepath.Join(f.home, "wt/app-A")
	git(t, main, "worktree", "add", "-q", "-b", "feat-a", a)
	b := filepath.Join(a, ".claude/worktrees/B")
	git(t, main, "worktree", "add", "-q", "-b", "feat-b", b)
	if err := os.RemoveAll(b); err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(a, ".build/checkouts/swift-log")
	os.MkdirAll(checkout, 0o755)
	git(t, checkout, "init", "-q")
	if r := Run(context.Background(), []*core.Item{wtItem(a, main)}, f.opts(), nil).Results[0]; r.Status != StatusDone {
		t.Fatalf("outer worktree with a gone nested one and a .build clone: %+v", r)
	}
	mustBeGone(t, a)
}

// Tool marker files are removed with the per-task folder; the folder itself
// is removed with rmdir, never recursively.
func TestToolParentWithMarkerRemoved(t *testing.T) {
	needGit(t)
	f := newFixture(t)
	main := f.newRepo(t, "src/repo", nil)
	task := f.mk(t, ".codex/worktrees/k1l2", ".codex-worktree-name", ".DS_Store")
	w := filepath.Join(task, "repo")
	git(t, main, "worktree", "add", "-q", "-b", "c", w)
	if r := Run(context.Background(), []*core.Item{wtItem(w, main)}, f.opts(), nil).Results[0]; r.Status != StatusDone {
		t.Fatalf("%+v", r)
	}
	mustBeGone(t, task)

	// A folder that gained something else is kept.
	task2 := f.mk(t, ".codex/worktrees/m3n4", ".codex-worktree-name")
	w2 := filepath.Join(task2, "repo")
	git(t, main, "worktree", "add", "-q", "-b", "d", w2)
	f.mk(t, ".codex/worktrees/m3n4/notes", "todo.md")
	if r := Run(context.Background(), []*core.Item{wtItem(w2, main)}, f.opts(), nil).Results[0]; r.Status != StatusDone {
		t.Fatalf("%+v", r)
	}
	mustExist(t, filepath.Join(task2, "notes/todo.md"))
	mustExist(t, filepath.Join(task2, ".codex-worktree-name"))
}
