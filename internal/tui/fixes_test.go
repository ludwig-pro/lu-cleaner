package tui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ludwig-pro/lu-cleaner/internal/clean"
	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/engine"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
)

// ------------------------------------------------------------------ helpers

// gitEnv makes git hermetic for the test (and for the executor it runs).
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

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// gitRepoWithCommit creates a repository with one commit on main.
func gitRepoWithCommit(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(dir, "README"), "hello\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "init")
}

func mustExist(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Lstat(p); err != nil {
		t.Fatalf("%s should still exist: %v", p, err)
	}
}

// deleteEntry drives the analyzer's delete dialog for the entry name of the
// current directory and waits for the result.
func deleteEntry(t *testing.T, d *driver, m *analyzeModel, name string) {
	t.Helper()
	m.focusName(t, name)
	d.keys("d")
	if m.mode != anConfirm {
		t.Fatalf("d on %s: no confirmation (status %q)", name, m.status)
	}
	d.typeText("yes")
	d.keys("enter")
	d.until("result", func() bool { return m.mode == anResult })
}

// ------------------------------------------------------------------ analyzer: git layouts

// A bare-clone layout (proj/.bare + a proj/.git file) is a whole repository:
// it used to be tagged "linked worktree" and rm -rf'd with its history.
func TestAnalyzerBareLayoutRepositoryIsNeverDeleted(t *testing.T) {
	gitEnv(t)
	home := tempHome(t)
	origin := filepath.Join(home, "origin")
	gitRepoWithCommit(t, origin)
	code := filepath.Join(home, "code")
	proj := filepath.Join(code, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, proj, "clone", "-q", "--bare", origin, ".bare")
	writeFile(t, filepath.Join(proj, ".git"), "gitdir: ./.bare\n")
	runGit(t, proj, "worktree", "add", "-q", "feat", "-b", "feat")
	feat := filepath.Join(proj, "feat")
	writeFile(t, filepath.Join(feat, "local.txt"), "only here\n")
	runGit(t, feat, "add", ".")
	runGit(t, feat, "commit", "-q", "-m", "local only")
	writeFile(t, filepath.Join(feat, "wip.txt"), "uncommitted\n")

	m := newTestAnalyzer(t, code, home)
	d := newDriver(t, m)
	d.until("sizes", settled(m))
	e := m.cur().byName["proj"]
	if e.git.kind != gitRepo || strings.Contains(e.tag, "linked worktree") {
		t.Fatalf("proj = %+v (tag %q), want a git repository", e.git, e.tag)
	}
	if !strings.Contains(m.View(), "git repo (bare layout)") {
		t.Fatalf("view does not tag the repository:\n%s", m.View())
	}
	m.focusName(t, "proj")
	d.keys("space")
	if len(m.marked) != 0 || !strings.Contains(m.status, "never deletes") {
		t.Fatalf("marking a repository: marks %d status %q", len(m.marked), m.status)
	}
	d.keys("d")
	if m.mode != anBrowse || !strings.Contains(m.status, "never deletes") {
		t.Fatalf("d on a repository: mode %v status %q", m.mode, m.status)
	}
	// Even handed to the executor, the synthetic item is report-only.
	items := m.itemsFor([]*anEntry{e})
	if items[0].CanClean() || items[0].Method != core.MethodReport {
		t.Fatalf("repository item = %+v", items[0])
	}
	if sum := clean.Run(context.Background(), items, m.opt.Clean, nil); sum.Count(clean.StatusDone) != 0 {
		t.Fatalf("results %+v", sum.Results)
	}
	mustExist(t, filepath.Join(proj, ".bare", "HEAD"))

	// Its worktree is a linked worktree of the bare repository, removed by git
	// run from the common dir, which refuses the uncommitted change.
	d.keys("enter")
	d.until("proj listed", func() bool { return m.cwd == proj && settled(m)() })
	fe := m.cur().byName["feat"]
	bare := filepath.Join(proj, ".bare")
	if !fe.isWorktree() || fe.git.common != bare || fe.git.main != bare {
		t.Fatalf("feat = %+v", fe.git)
	}
	if b := m.cur().byName[".bare"]; b.git.kind != gitRepo {
		t.Fatalf(".bare = %+v", b.git)
	}
	if it := m.itemsFor([]*anEntry{fe})[0]; it.Method != core.MethodWorktree || it.Project != bare {
		t.Fatalf("feat item = %+v", it)
	}
	deleteEntry(t, d, m, "feat")
	if m.result.Count(clean.StatusSkipped) != 1 || m.result.Count(clean.StatusDone) != 0 {
		t.Fatalf("dirty worktree: results %+v", m.result.Results)
	}
	mustExist(t, filepath.Join(feat, "wip.txt"))

	// Clean, it goes away through git: history and branch are kept.
	d.keys("x")
	if err := os.Remove(filepath.Join(feat, "wip.txt")); err != nil {
		t.Fatal(err)
	}
	deleteEntry(t, d, m, "feat")
	if m.result.Count(clean.StatusDone) != 1 {
		t.Fatalf("clean worktree: results %+v", m.result.Results)
	}
	if _, err := os.Lstat(feat); !os.IsNotExist(err) {
		t.Fatalf("feat still exists: %v", err)
	}
	mustExist(t, filepath.Join(bare, "HEAD"))
	if out := runGit(t, bare, "branch", "--list", "feat"); !strings.Contains(out, "feat") {
		t.Fatalf("branch feat lost: %q", out)
	}
}

// Submodule checkouts and worktrees of a bare repository used to be tagged
// "linked worktree" but deleted with rm -rf, skipping git's dirty checks.
func TestAnalyzerSubmoduleAndBareRepoWorktree(t *testing.T) {
	gitEnv(t)
	home := tempHome(t)
	lib := filepath.Join(home, "libsrc")
	gitRepoWithCommit(t, lib)
	super := filepath.Join(home, "code", "super")
	gitRepoWithCommit(t, super)
	runGit(t, super, "-c", "protocol.file.allow=always", "submodule", "--quiet", "add", lib, "vendor/lib")
	writeFile(t, filepath.Join(super, "vendor", "lib", "README"), "edited, not committed\n")

	m := newTestAnalyzer(t, filepath.Join(super, "vendor"), home)
	d := newDriver(t, m)
	d.until("sizes", settled(m))
	e := m.cur().byName["lib"]
	if e.git.kind != gitOther || !strings.Contains(e.tag, "submodule") || strings.Contains(e.tag, "linked worktree") {
		t.Fatalf("submodule = %+v tag %q", e.git, e.tag)
	}
	m.focusName(t, "lib")
	d.keys("space", "d")
	if len(m.marked) != 0 || m.mode != anBrowse || !strings.Contains(m.status, "submodule") {
		t.Fatalf("submodule: marks %d mode %v status %q", len(m.marked), m.mode, m.status)
	}
	if it := m.itemsFor([]*anEntry{e})[0]; it.CanClean() {
		t.Fatalf("submodule item is cleanable: %+v", it)
	}

	// worktree of a plain bare repository
	bare := filepath.Join(home, "repos", "app.git")
	if err := os.MkdirAll(filepath.Dir(bare), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, home, "clone", "-q", "--bare", lib, bare)
	wts := filepath.Join(home, "wts")
	runGit(t, bare, "worktree", "add", "-q", filepath.Join(wts, "feature"), "-b", "feature")
	writeFile(t, filepath.Join(wts, "feature", "wip.txt"), "uncommitted\n")

	m2 := newTestAnalyzer(t, wts, home)
	d2 := newDriver(t, m2)
	d2.until("sizes", settled(m2))
	fe := m2.cur().byName["feature"]
	if !fe.isWorktree() || fe.git.common != bare {
		t.Fatalf("bare worktree = %+v", fe.git)
	}
	deleteEntry(t, d2, m2, "feature")
	if m2.result.Count(clean.StatusDone) != 0 {
		t.Fatalf("dirty bare worktree removed: %+v", m2.result.Results)
	}
	mustExist(t, filepath.Join(wts, "feature", "wip.txt"))
}

func TestClassifyGitLayouts(t *testing.T) {
	root := tempHome(t)
	main := filepath.Join(root, "main")

	// verified linked worktree (also found when the recorded path differs in case)
	wt := filepath.Join(root, "wts", "ok")
	admin := fakeWorktree(t, main, wt)
	if g := classifyGit(wt); g.kind != gitWorktree || g.common != filepath.Join(main, ".git") || g.main != main || g.locked {
		t.Fatalf("worktree = %+v", g)
	}
	writeFile(t, filepath.Join(admin, "gitdir"), strings.ToUpper(filepath.Join(wt, ".git"))+"\n")
	if g := classifyGit(wt); g.kind != gitWorktree {
		t.Fatalf("case-insensitive back-pointer = %+v", g)
	}
	// locked: the item carries it, so the executor refuses without --force
	writeFile(t, filepath.Join(admin, "locked"), "")
	g := classifyGit(wt)
	if !g.locked || !strings.Contains(g.label, "locked") {
		t.Fatalf("locked = %+v", g)
	}
	it := (&analyzeModel{}).itemsFor([]*anEntry{{name: "ok", path: wt, isDir: true, git: g}})[0]
	if it.Method != core.MethodWorktree || it.Meta["locked"] != "true" || it.Project != filepath.Join(main, ".git") {
		t.Fatalf("locked item = %+v", it)
	}

	// a copy of a worktree: git registers another path
	cp := filepath.Join(root, "wts", "copy")
	writeFile(t, filepath.Join(cp, ".git"), "gitdir: "+fakeWorktree(t, main, filepath.Join(root, "wts", "orig"))+"\n")
	if g := classifyGit(cp); g.kind != gitOther || !strings.Contains(g.why, "copy") {
		t.Fatalf("copy = %+v", g)
	}

	// orphan: the admin dir is gone
	orphan := filepath.Join(root, "wts", "orphan")
	writeFile(t, filepath.Join(orphan, ".git"), "gitdir: "+filepath.Join(root, "gone", ".git", "worktrees", "orphan")+"\n")
	if g := classifyGit(orphan); g.kind != gitOther || !strings.Contains(g.why, "orphaned") {
		t.Fatalf("orphan = %+v", g)
	}

	// the repository is gone but its admin dir remains (commondir to nowhere)
	half := filepath.Join(root, "wts", "half")
	hadmin := filepath.Join(root, "other", ".git", "worktrees", "half")
	writeFile(t, filepath.Join(hadmin, "commondir"), "../..\n")
	writeFile(t, filepath.Join(hadmin, "gitdir"), filepath.Join(half, ".git")+"\n")
	writeFile(t, filepath.Join(half, ".git"), "gitdir: "+hadmin+"\n")
	if g := classifyGit(half); g.kind != gitOther {
		t.Fatalf("missing repository = %+v", g)
	}

	// commondir that does not match the worktrees/<name> layout
	odd := filepath.Join(root, "wts", "odd")
	oadmin := filepath.Join(main, ".git", "worktrees", "odd")
	writeFile(t, filepath.Join(oadmin, "commondir"), filepath.Join(root, "elsewhere")+"\n")
	writeFile(t, filepath.Join(oadmin, "gitdir"), filepath.Join(odd, ".git")+"\n")
	writeFile(t, filepath.Join(odd, ".git"), "gitdir: "+oadmin+"\n")
	if g := classifyGit(odd); g.kind != gitOther {
		t.Fatalf("odd layout = %+v", g)
	}

	// --separate-git-dir main checkout: a repository's working tree
	sep := filepath.Join(root, "sep.git")
	for _, d := range []string{"objects", "refs"} {
		if err := os.MkdirAll(filepath.Join(sep, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(sep, "HEAD"), "ref: refs/heads/main\n")
	sepwt := filepath.Join(root, "sepwt")
	writeFile(t, filepath.Join(sepwt, ".git"), "gitdir: "+sep+"\n")
	if g := classifyGit(sepwt); g.kind != gitRepo {
		t.Fatalf("separate git dir = %+v", g)
	}
	// the bare repository itself
	if g := classifyGit(sep); g.kind != gitRepo {
		t.Fatalf("bare repo = %+v", g)
	}
	// history inside the directory, through a relative gitdir
	self := filepath.Join(root, "self")
	for _, d := range []string{"objects", "refs"} {
		if err := os.MkdirAll(filepath.Join(self, ".bare", d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(self, ".bare", "HEAD"), "ref: refs/heads/main\n")
	writeFile(t, filepath.Join(self, ".git"), "gitdir: ./.bare\n")
	if g := classifyGit(self); g.kind != gitRepo {
		t.Fatalf("bare layout = %+v", g)
	}
	// unparsable .git file, .git symlink
	junk := filepath.Join(root, "junk")
	writeFile(t, filepath.Join(junk, ".git"), "nonsense\n")
	if g := classifyGit(junk); g.kind != gitOther {
		t.Fatalf("junk = %+v", g)
	}
	link := filepath.Join(root, "link")
	if err := os.MkdirAll(link, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(main, ".git"), filepath.Join(link, ".git")); err != nil {
		t.Fatal(err)
	}
	if g := classifyGit(link); g.kind != gitOther {
		t.Fatalf(".git symlink = %+v", g)
	}
	// unreadable: only ENOENT proves there is no git data
	if os.Geteuid() != 0 {
		locked := filepath.Join(root, "noaccess")
		if err := os.MkdirAll(locked, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(locked, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
		if g := classifyGit(locked); g.kind != gitOther {
			t.Fatalf("unreadable = %+v", g)
		}
	}
	// a plain directory
	if g := classifyGit(filepath.Join(root, "wts")); g.kind != gitNone {
		t.Fatalf("plain dir = %+v", g)
	}
}

// ------------------------------------------------------------------ analyzer: marks & Trash

// Marks inside a deleted directory used to survive and hijack the next d.
func TestAnalyzerStaleNestedMarks(t *testing.T) {
	f := newAnFixture(t)
	mkdirFiles(t, filepath.Join(f.root, "src", "inner"), map[string]int{"x": 4000})
	m := newTestAnalyzer(t, f.root, f.home)
	d := newDriver(t, m)
	d.until("sizes", settled(m))
	m.focusName(t, "src")
	d.keys("enter")
	d.until("src listed", func() bool { return m.cwd == filepath.Join(f.root, "src") && settled(m)() })
	m.focusName(t, "inner")
	d.keys("space", "left")
	d.until("back", func() bool { return m.cwd == f.root })
	m.focusName(t, "src")
	d.keys("space", "d")
	if len(m.confirm) != 2 {
		t.Fatalf("confirm = %d entries", len(m.confirm))
	}
	d.typeText("yes")
	d.keys("enter")
	d.until("result", func() bool { return m.mode == anResult })
	d.keys("x")
	if len(m.marked) != 0 {
		t.Fatalf("stale marks left: %v", m.marked)
	}
	m.focusName(t, "big.bin")
	d.keys("d")
	if len(m.confirm) != 1 || m.confirm[0].name != "big.bin" {
		t.Fatalf("d should target the cursor entry, confirm = %+v", m.confirm)
	}
}

// Marks are re-classified when the dialog opens: an entry that became a
// repository since it was listed is left out, the others are deleted.
func TestAnalyzerMarkedEntryBecameRepository(t *testing.T) {
	f := newAnFixture(t)
	m := newTestAnalyzer(t, f.root, f.home)
	d := newDriver(t, m)
	d.until("sizes", settled(m))
	m.focusName(t, "src")
	d.keys("space")
	m.focusName(t, "big.bin")
	d.keys("space")
	if err := os.Mkdir(filepath.Join(f.root, "src", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	d.keys("d")
	if len(m.confirm) != 1 || m.confirm[0].name != "big.bin" || len(m.confRefused) != 1 {
		t.Fatalf("confirm %+v refused %+v", m.confirm, m.confRefused)
	}
	if v := m.View(); !strings.Contains(v, "1 marked entry left out") || !strings.Contains(v, "Delete 1 entry") {
		t.Fatalf("confirm view:\n%s", v)
	}
	d.typeText("yes")
	d.keys("enter")
	d.until("result", func() bool { return m.mode == anResult })
	if m.result.Count(clean.StatusDone) != 1 || len(m.result.Results) != 1 {
		t.Fatalf("results %+v", m.result.Results)
	}
	mustExist(t, filepath.Join(f.root, "src", "main.go"))
	if len(m.marked) != 0 {
		t.Fatalf("marks left: %v", m.marked)
	}
}

// In Trash mode nothing is freed: totals of the Trash's ancestors stay, the
// result never says "freed", and a worktree is skipped instead of being
// removed permanently behind a "Move to Trash" title.
func TestAnalyzerTrashMode(t *testing.T) {
	home := tempHome(t)
	proj := mkdirFiles(t, filepath.Join(home, "proj"), map[string]int{"big.bin": 50000, "keep.txt": 10})
	mkdirFiles(t, filepath.Join(home, ".Trash"), map[string]int{"old": 10})
	fakeWorktree(t, filepath.Join(home, "repo"), filepath.Join(proj, "wt"))
	opts := testCleanOpts(home)
	opts.Trash = true
	m := newAnalyzer(context.Background(), AnalyzeOptions{Env: testEnv(home), Root: home, Clean: opts})
	t.Cleanup(m.close)
	m.diskFn = fakeDisk
	m.w, m.h = 120, 40
	d := newDriver(t, m)
	d.until("sizes", settled(m))
	before, ok := m.sizes[home]
	if !ok {
		t.Fatal("home size not aggregated")
	}
	projBefore := m.sizes[proj]

	m.focusName(t, "proj")
	d.keys("enter")
	d.until("proj", func() bool { return m.cwd == proj && settled(m)() })
	m.focusName(t, "big.bin")
	d.keys("space")
	m.focusName(t, "wt")
	d.keys("space", "d")
	v := m.View()
	for _, s := range []string{"Move to Trash 1 entry", "not possible in Trash mode"} {
		if !strings.Contains(v, s) {
			t.Fatalf("confirm lacks %q:\n%s", s, v)
		}
	}
	d.typeText("yes")
	d.keys("enter")
	d.until("result", func() bool { return m.mode == anResult })
	var skipped string
	for _, r := range m.result.Results {
		if r.Item.Name == "wt" {
			skipped = r.Message
		}
	}
	if !strings.Contains(skipped, "not possible in Trash mode") {
		t.Fatalf("worktree result %q (%+v)", skipped, m.result.Results)
	}
	mustExist(t, filepath.Join(proj, "wt", ".git"))
	mustExist(t, filepath.Join(home, ".Trash", "big.bin"))
	v = m.View()
	if strings.Contains(v, "freed") || !strings.Contains(v, "moved to the Trash") {
		t.Fatalf("result must not claim freed space:\n%s", v)
	}
	if got := m.sizes[home]; got.Bytes != before.Bytes {
		t.Fatalf("home total %d -> %d: a move to the Trash frees nothing", before.Bytes, got.Bytes)
	}
	if got := m.sizes[proj]; got.Bytes >= projBefore.Bytes {
		t.Fatalf("proj total %d -> %d: big.bin left it", projBefore.Bytes, got.Bytes)
	}
	// the Trash is measured again and now holds big.bin
	d.keys("x", "left")
	d.until("home re-measured", func() bool { return m.cwd == home && settled(m)() })
	if tr := m.cur().byName[".Trash"]; tr == nil || tr.size < 50000 {
		t.Fatalf(".Trash = %+v", tr)
	}
}

// ------------------------------------------------------------------ escapes

func TestSafeText(t *testing.T) {
	cases := map[string]string{
		"plain name/é 日本 🌳":         "plain name/é 日本 🌳",
		"evil\x1b]52;c;SGk=\x07end": `evil\x1b]52;c;SGk=\aend`,
		"two\nlines\r\t":            `two\nlines\r\t`,
		"c1\u009b31m del\x7f":       `c1\x9b31m del\x7f`,
		"bidi\u202egpj.exe":         `bidi\u202egpj.exe`,
		"bad\xffutf8":               `bad\xffutf8`,
		"sep\u2028":                 `sep\u2028`,
	}
	for in, want := range cases {
		if got := safeText(in); got != want {
			t.Errorf("safeText(%q) = %q, want %q", in, got, want)
		}
	}
}

func hasRawControl(s string) bool {
	return strings.Contains(s, "\x1b]") || strings.Contains(s, "\x07") || strings.Contains(s, "\x1b[2J") ||
		strings.Contains(s, "\u009b") || strings.Contains(s, "\u202e")
}

func TestAnalyzerEscapesNames(t *testing.T) {
	home := tempHome(t)
	root := filepath.Join(home, "clone")
	evil := "evil\x1b]52;c;SGVsbG8=\x07name"
	mkdirFiles(t, filepath.Join(root, evil), map[string]int{"x": 10})
	mkdirFiles(t, filepath.Join(root, "two\nlines\u202e"), map[string]int{"x": 10})
	m := newTestAnalyzer(t, root, home)
	d := newDriver(t, m)
	d.until("sizes", settled(m))
	v := m.View()
	if hasRawControl(v) {
		t.Fatalf("raw control sequence in the view: %q", v)
	}
	if !strings.Contains(v, `evil\x1b]52;c;SGVsbG8=\aname/`) || !strings.Contains(v, `two\nlines\u202e/`) {
		t.Fatalf("names not escaped:\n%s", v)
	}
	m.focusName(t, evil)
	d.keys("space")
	m.focusName(t, "two\nlines\u202e")
	d.keys("space", "d")
	if v := m.View(); hasRawControl(v) {
		t.Fatalf("raw control sequence in the confirm view: %q", v)
	}
	d.keys("esc")
	m.focusName(t, evil)
	d.keys("o")
	if v := m.View(); hasRawControl(v) || !strings.Contains(v, "Revealed") {
		t.Fatalf("status: %q", v)
	}
}

func TestPickerEscapesNames(t *testing.T) {
	it := mkItem("x", core.CatArtifacts, 5e6, core.RiskCaution, 30*day)
	it.Name = "bad\x1b[2Jname\x1b]0;title\x07"
	it.Path = "/nonexistent/dir\x1b]52;c;SGk=\x07/node_modules"
	it.Warn = "warn\u009b31m"
	it.Note = "note\u202eexe"
	it.Meta = map[string]string{"branch": "b\x1b]0;x\x07"}
	m := newTestPicker(t, PickerOptions{Providers: []core.Provider{&fakeProvider{id: "p", cats: []core.Category{core.CatArtifacts}, items: []*core.Item{it}}}, Flat: true})
	d := newDriver(t, m)
	d.until("scan done", scanDone(m))
	d.send(teaSize(160, 50))
	if v := m.View(); hasRawControl(v) || !strings.Contains(v, `bad\x1b[2Jname`) {
		t.Fatalf("list/details: %q", v)
	}
	d.keys("space")
	if v := m.View(); hasRawControl(v) {
		t.Fatalf("status: %q", v)
	}
	d.keys("d")
	if v := m.View(); hasRawControl(v) {
		t.Fatalf("confirm: %q", v)
	}
	d.keys("esc", "/")
	d.send(keyMsg("q\x1b]0;x\x07"))
	if v := m.View(); hasRawControl(v) {
		t.Fatalf("filter input: %q", v)
	}
}

// ------------------------------------------------------------------ picker: Sizing & stale confirm

// A placeholder still being measured may be rejected by its provider (e.g.
// node_modules tracked by git): it can neither be selected nor cleaned, and
// a rejection arriving while the dialog is open cancels the clean.
func TestPickerSizingItemsAndChangesDuringConfirm(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	ph := mkItem("ph", core.CatArtifacts, 0, core.RiskModerate, 0)
	ph.Sizing = true
	ok := mkItem("ok", core.CatArtifacts, 1e6, core.RiskModerate, 0)
	m := newTestPicker(t, PickerOptions{Providers: []core.Provider{&fakeProvider{id: "p", cats: []core.Category{core.CatArtifacts}, items: []*core.Item{ph, ok}, gate: gate}}})
	calls := 0
	m.cleanFn = func(ctx context.Context, items []*core.Item, opt clean.Options, progress func(clean.Result)) *clean.Summary {
		calls++
		return &clean.Summary{}
	}
	d := newDriver(t, m)
	d.until("items", func() bool { return len(m.visible) == 2 })

	// the category toggle skips it
	d.keys("space")
	if selectedIDs(m) != "ok" || !strings.Contains(m.status, "still being verified") {
		t.Fatalf("category toggle: selected %q status %q", selectedIDs(m), m.status)
	}
	d.keys("space") // unselect
	// space on the placeholder itself is refused, A skips it
	d.keys("enter")
	for i, it := range m.list {
		if it.ID == "ph" {
			m.cursor, m.cursorID = i, it.ID
		}
	}
	d.keys("space")
	if m.selected["ph"] || !strings.Contains(m.status, "still being measured") {
		t.Fatalf("space on a sizing item: selected %v status %q", m.selected, m.status)
	}
	d.keys("A")
	if selectedIDs(m) != "ok" {
		t.Fatalf("A: selected %q", selectedIDs(m))
	}
	// even if it was selected before (re-emitted as Sizing), it is not cleaned
	m.selected["ph"] = true
	m.dirtySel = true
	m.refresh()
	if selectedIDs(m) != "ok" {
		t.Fatalf("sizing item counted in the selection: %q", selectedIDs(m))
	}

	// The provider rejects "ok" while the dialog is open: nothing is cleaned.
	d.keys("d")
	if m.mode != modeConfirm {
		t.Fatalf("mode = %v", m.mode)
	}
	rej := ok.Clone()
	rej.Selectable, rej.Method, rej.Risk = false, core.MethodReport, core.RiskNever
	rej.Warn = "not proposed: contains files tracked by git"
	d.send(scanBatchMsg{events: []engine.Event{{Item: rej, Provider: "p"}}})
	d.keys("y")
	if calls != 0 || m.mode != modeBrowse || !strings.Contains(m.status, "changed while confirming") {
		t.Fatalf("rejected during confirm: calls %d mode %v status %q", calls, m.mode, m.status)
	}

	// A size update alone does not cancel: the clean gets the fresh item.
	ok2 := mkItem("ok2", core.CatArtifacts, 2e6, core.RiskModerate, 0)
	d.send(scanBatchMsg{events: []engine.Event{{Item: ok2, Provider: "p"}}})
	for i, it := range m.list {
		if it.ID == "ok2" {
			m.cursor, m.cursorID = i, it.ID
		}
	}
	d.keys("space", "d")
	bigger := ok2.Clone()
	bigger.Size = 3e6
	d.send(scanBatchMsg{events: []engine.Event{{Item: bigger, Provider: "p"}}})
	var got []*core.Item
	m.cleanFn = func(ctx context.Context, items []*core.Item, opt clean.Options, progress func(clean.Result)) *clean.Summary {
		got = items
		return &clean.Summary{}
	}
	d.keys("y")
	d.until("summary", func() bool { return m.mode == modeSummary })
	if len(got) != 1 || got[0] != m.items["ok2"] || got[0].Size != 3e6 {
		t.Fatalf("cleaned %+v", got)
	}
}

// ------------------------------------------------------------------ picker: Trash mode

func TestPickerTrashModeSkipsWorktreesAndCommands(t *testing.T) {
	home := tempHome(t)
	junk := mkdirFiles(t, filepath.Join(home, "cache", "junk"), map[string]int{"a": 4000})
	wtDir := mkdirFiles(t, filepath.Join(home, "wts", "feature"), map[string]int{".env": 10})
	cmdDir := mkdirFiles(t, filepath.Join(home, "sim", "dev"), map[string]int{"x": 10})
	del := mkItem("junk", core.CatSystem, 4096, core.RiskSafe, 0)
	del.Path = junk
	wt := mkItem("feature", core.CatWorktrees, 5000, core.RiskModerate, 0)
	wt.Method, wt.Path = core.MethodWorktree, wtDir
	cmd := mkItem("dev", core.CatSimulators, 7000, core.RiskSafe, 0)
	cmd.Method, cmd.Path, cmd.Location = core.MethodCommand, "", cmdDir
	cmd.Command = []string{"rm", "-rf", cmdDir}
	prov := &fakeProvider{id: "p", cats: []core.Category{core.CatSystem, core.CatWorktrees, core.CatSimulators}, items: []*core.Item{del, wt, cmd}}
	m := newTestPicker(t, PickerOptions{Env: testEnv(home), Providers: []core.Provider{prov}, Flat: true})
	var got []string
	m.cleanFn = func(ctx context.Context, items []*core.Item, opt clean.Options, progress func(clean.Result)) *clean.Summary {
		got = ids(items)
		return clean.Run(ctx, items, opt, progress)
	}
	d := newDriver(t, m)
	d.until("scan done", scanDone(m))
	d.keys("t", "A", "d")
	v := m.View()
	for _, s := range []string{"Move to Trash 1 item", "Skipped", "not possible in Trash mode"} {
		if !strings.Contains(v, s) {
			t.Fatalf("confirm lacks %q:\n%s", s, v)
		}
	}
	d.keys("y")
	d.until("summary", func() bool { return m.mode == modeSummary })
	if strings.Join(got, ",") != "junk" {
		t.Fatalf("executor received %v", got)
	}
	for _, r := range m.lastSummary.Results {
		if (r.Item.ID == "feature" || r.Item.ID == "dev") && (r.Status != clean.StatusSkipped || !strings.Contains(r.Message, "Trash mode")) {
			t.Fatalf("%s: %v %q", r.Item.ID, r.Status, r.Message)
		}
	}
	mustExist(t, filepath.Join(wtDir, ".env"))
	mustExist(t, cmdDir)
	mustExist(t, filepath.Join(home, ".Trash", "junk"))
	v = m.View()
	if strings.Contains(v, "Estimated freed") || !strings.Contains(v, "Moved to Trash") || !strings.Contains(v, "1 item moved to the Trash") {
		t.Fatalf("summary:\n%s", v)
	}
	d.keys("x")
	if strings.Contains(m.status, "cleaned") || !strings.Contains(m.status, "moved to the Trash") {
		t.Fatalf("status %q", m.status)
	}
}

// ------------------------------------------------------------------ picker: nested caution, Covers, filter

func TestPickerNestedCautionRequiresYes(t *testing.T) {
	par := mkItem("wt", core.CatWorktrees, 600e6, core.RiskModerate, 30*day)
	par.Path = "/nonexistent/codex/wt"
	cau := mkItem("output", core.CatArtifacts, 242e6, core.RiskCaution, 30*day)
	cau.Path = "/nonexistent/codex/wt/output"
	prov := &fakeProvider{id: "p", cats: []core.Category{core.CatWorktrees, core.CatArtifacts}, items: []*core.Item{par, cau}}
	m := newTestPicker(t, PickerOptions{Providers: []core.Provider{prov}, Flat: true})
	calls := 0
	m.cleanFn = func(ctx context.Context, items []*core.Item, opt clean.Options, progress func(clean.Result)) *clean.Summary {
		calls++
		sum := &clean.Summary{}
		for _, it := range core.TopLevel(items) {
			sum.Results = append(sum.Results, clean.Result{Item: it, Status: clean.StatusDone})
		}
		return sum
	}
	d := newDriver(t, m)
	d.until("scan done", scanDone(m))
	m.cursor, m.cursorID = 0, "wt" // largest first
	d.keys("space", "d")
	c := m.confirm
	if c == nil || c.caution != 1 || len(c.nested) != 1 || !c.input.active {
		t.Fatalf("confirm = %+v", c)
	}
	if v := m.View(); !strings.Contains(v, "inside the selection") || !strings.Contains(v, "Type yes") {
		t.Fatalf("confirm view:\n%s", v)
	}
	d.keys("y")
	if m.mode != modeConfirm || calls != 0 {
		t.Fatalf("y must not confirm a nested caution item: mode %v calls %d", m.mode, calls)
	}
	// both selected: counted once, and the title explains the nesting
	d.keys("esc", "A", "d")
	if m.confirm.caution != 1 || len(m.confirm.items) != 1 || !strings.Contains(m.View(), "2 selected: 1 item inside another selected item") {
		t.Fatalf("confirm = %+v\n%s", m.confirm, m.View())
	}
	d.typeText("yes")
	d.keys("enter")
	d.until("summary", func() bool { return m.mode == modeSummary })
	if calls != 1 {
		t.Fatalf("calls = %d", calls)
	}
}

func TestPickerCommandCoversRemovesNestedItems(t *testing.T) {
	home := tempHome(t)
	dev := mkdirFiles(t, filepath.Join(home, "sim", "dev1"), map[string]int{"data/att/x": 5000})
	cmd := mkItem("dev", core.CatSimulators, 30000, core.RiskModerate, 0)
	cmd.Method, cmd.Path, cmd.Location, cmd.Covers = core.MethodCommand, "", dev, dev
	cmd.Command = []string{"rm", "-rf", dev}
	att := mkItem("att", core.CatSimulators, 5000, core.RiskSafe, 0)
	att.Path = filepath.Join(dev, "data", "att")
	m := newTestPicker(t, PickerOptions{Env: testEnv(home), Providers: []core.Provider{&fakeProvider{id: "p", cats: []core.Category{core.CatSimulators}, items: []*core.Item{cmd, att}}}, Flat: true})
	d := newDriver(t, m)
	d.until("scan done", scanDone(m))
	d.keys("A", "d", "y")
	d.until("summary", func() bool { return m.mode == modeSummary })
	if fsx.Exists(dev) {
		t.Fatal("command did not run")
	}
	d.keys("x")
	if m.items["att"] != nil || m.selected["att"] || len(m.selItems) != 0 || m.visTotal != 0 {
		t.Fatalf("att still listed: items %v selected %v visTotal %d", ids(m.visible), m.selected, m.visTotal)
	}
}

func TestPickerFilterHiddenSelectionCategory(t *testing.T) {
	alpha := mkItem("alpha", core.CatArtifacts, 100e6, core.RiskModerate, 0)
	bravo := mkItem("bravo", core.CatArtifacts, 900e6, core.RiskModerate, 0)
	m := newTestPicker(t, PickerOptions{Providers: []core.Provider{&fakeProvider{id: "p", cats: []core.Category{core.CatArtifacts}, items: []*core.Item{alpha, bravo}}}})
	d := newDriver(t, m)
	d.until("scan done", scanDone(m))
	d.keys("enter") // bravo is first (largest)
	d.keys("space", "left")
	d.keys("/")
	d.typeText("alpha")
	d.keys("enter")
	r := m.cats[0]
	if r.selN != 0 || r.selHidden != 1 {
		t.Fatalf("row selN %d selHidden %d", r.selN, r.selHidden)
	}
	var row string
	for _, l := range strings.Split(m.View(), "\n") {
		if strings.Contains(l, "Project artifacts") {
			row = l
		}
	}
	if !strings.Contains(row, "[ ]") || !strings.Contains(row, "+1 hidden") {
		t.Fatalf("row = %q", row)
	}
	// the toggle now selects the visible item instead of being stuck
	d.keys("space")
	if !m.selected["alpha"] || !m.selected["bravo"] || m.cats[0].selN != 1 {
		t.Fatalf("selected %v", m.selected)
	}
	d.keys("d")
	if m.confirm.hidden != 1 || !strings.Contains(m.View(), "hidden by the filter") {
		t.Fatalf("confirm hidden %d:\n%s", m.confirm.hidden, m.View())
	}
}
