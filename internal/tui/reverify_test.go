package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ludwig-pro/lu-cleaner/internal/clean"
	"github.com/ludwig-pro/lu-cleaner/internal/core"
)

// Linked worktrees of an ordinary repository (<main>/.git) and of a
// --separate-git-dir repository are removed by git run from their common git
// dir: a dirty one is refused, a clean one is removed and unregistered, and
// the repositories themselves are never deletable.
func TestAnalyzerWorktreesOfNormalAndSeparateGitDirRepos(t *testing.T) {
	gitEnv(t)
	home := tempHome(t)
	main := filepath.Join(home, "code", "main")
	gitRepoWithCommit(t, main)
	wts := filepath.Join(home, "wts")
	runGit(t, main, "worktree", "add", "-q", filepath.Join(wts, "feat"), "-b", "feat")
	writeFile(t, filepath.Join(wts, "feat", "wip.txt"), "uncommitted\n")

	sep := filepath.Join(home, "seps", "sep.git")
	sepMain := filepath.Join(home, "code", "sepmain")
	if err := os.MkdirAll(filepath.Dir(sep), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, home, "init", "-q", "-b", "main", "--separate-git-dir", sep, sepMain)
	writeFile(t, filepath.Join(sepMain, "README"), "x\n")
	runGit(t, sepMain, "add", ".")
	runGit(t, sepMain, "commit", "-q", "-m", "init")
	runGit(t, sepMain, "worktree", "add", "-q", filepath.Join(wts, "sepfeat"), "-b", "sepfeat")

	m := newTestAnalyzer(t, wts, home)
	d := newDriver(t, m)
	d.until("sizes", settled(m))
	fe, se := m.cur().byName["feat"], m.cur().byName["sepfeat"]
	if !fe.isWorktree() || fe.git.common != filepath.Join(main, ".git") || fe.git.main != main {
		t.Fatalf("feat = %+v", fe.git)
	}
	if !se.isWorktree() || se.git.common != sep {
		t.Fatalf("sepfeat = %+v", se.git)
	}
	if it := m.itemsFor([]*anEntry{fe})[0]; it.Method != core.MethodWorktree || it.Project != filepath.Join(main, ".git") {
		t.Fatalf("feat item = %+v", it)
	}

	deleteEntry(t, d, m, "feat")
	if m.result.Count(clean.StatusSkipped) != 1 || m.result.Count(clean.StatusDone) != 0 {
		t.Fatalf("dirty worktree: %+v", m.result.Results)
	}
	mustExist(t, filepath.Join(wts, "feat", "wip.txt"))
	d.keys("x")
	if err := os.Remove(filepath.Join(wts, "feat", "wip.txt")); err != nil {
		t.Fatal(err)
	}
	deleteEntry(t, d, m, "feat")
	d.keys("x")
	deleteEntry(t, d, m, "sepfeat")
	for _, wt := range []string{"feat", "sepfeat"} {
		if _, err := os.Lstat(filepath.Join(wts, wt)); !os.IsNotExist(err) {
			t.Fatalf("%s still exists (%v)", wt, err)
		}
	}
	if out := runGit(t, main, "worktree", "list"); strings.Contains(out, "feat") {
		t.Fatalf("feat still registered: %s", out)
	}
	if out := runGit(t, sepMain, "worktree", "list"); strings.Contains(out, "sepfeat") {
		t.Fatalf("sepfeat still registered: %s", out)
	}
	if out := runGit(t, main, "branch", "--list", "feat"); !strings.Contains(out, "feat") {
		t.Fatalf("branch feat lost: %q", out)
	}

	m2 := newTestAnalyzer(t, filepath.Join(home, "code"), home)
	d2 := newDriver(t, m2)
	d2.until("sizes", settled(m2))
	for _, n := range []string{"main", "sepmain"} {
		if e := m2.cur().byName[n]; e.git.kind != gitRepo || e.refusal() == "" {
			t.Fatalf("%s = %+v", n, e.git)
		}
	}
}

// Submodule git dirs are recognised precisely: a submodule checked out in a
// linked worktree and a worktree of a submodule are refused, while a worktree
// of a repository that merely lives in a folder named "modules" is not.
func TestClassifyGitModulesFolders(t *testing.T) {
	gitEnv(t)
	root := tempHome(t)

	// a repository under ~/code/modules/api: its worktree is an ordinary one
	api := filepath.Join(root, "code", "modules", "api")
	wt := filepath.Join(root, "wts", "api-feat")
	fakeWorktree(t, api, wt)
	if g := classifyGit(wt); g.kind != gitWorktree || g.common != filepath.Join(api, ".git") {
		t.Fatalf("worktree of a repository in a modules folder = %+v", g)
	}

	lib := filepath.Join(root, "libsrc")
	gitRepoWithCommit(t, lib)
	super := filepath.Join(root, "super")
	gitRepoWithCommit(t, super)
	runGit(t, super, "-c", "protocol.file.allow=always", "submodule", "--quiet", "add", lib, "vendor/lib")
	runGit(t, super, "commit", "-q", "-m", "sub")
	superWt := filepath.Join(root, "superwt")
	runGit(t, super, "worktree", "add", "-q", superWt, "-b", "wt")
	runGit(t, superWt, "-c", "protocol.file.allow=always", "submodule", "--quiet", "update", "--init")
	libWt := filepath.Join(root, "libwt")
	runGit(t, filepath.Join(super, "vendor", "lib"), "worktree", "add", "-q", libWt, "-b", "lwt")

	for _, p := range []string{filepath.Join(super, "vendor", "lib"), filepath.Join(superWt, "vendor", "lib"), libWt} {
		g := classifyGit(p)
		if g.kind != gitOther || !strings.Contains(g.label, "submodule") {
			t.Fatalf("%s = %+v", p, g)
		}
	}
	// the submodule's git dir itself
	if g := classifyGit(filepath.Join(super, ".git", "modules", "vendor", "lib")); g.kind != gitRepo {
		t.Fatalf("submodule git dir = %+v", g)
	}
}

// Only ENOENT proves a directory holds no git data: an unreadable one is
// refused, but not mislabelled as a git checkout. A directory named .git is
// git data even when it does not look like a complete repository.
func TestClassifyGitUnreadableAndDotGit(t *testing.T) {
	root := tempHome(t)
	partial := filepath.Join(root, "proj", ".git")
	if err := os.MkdirAll(filepath.Join(partial, "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if g := classifyGit(partial); g.kind != gitRepo {
		t.Fatalf("partial .git dir = %+v", g)
	}
	upper := filepath.Join(root, "other", ".GIT")
	if err := os.MkdirAll(upper, 0o755); err != nil {
		t.Fatal(err)
	}
	if g := classifyGit(upper); g.kind != gitRepo {
		t.Fatalf(".GIT dir = %+v", g)
	}
	if os.Geteuid() == 0 {
		t.Skip("root reads everything")
	}
	locked := filepath.Join(root, "noaccess")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	g := classifyGit(locked)
	if g.kind != gitOther || strings.Contains(g.label, "checkout") || !strings.Contains(g.label, "unreadable") ||
		!strings.Contains(g.why, "cannot be checked") {
		t.Fatalf("unreadable = %+v", g)
	}
}
