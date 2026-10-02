package worktrees

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"golang.org/x/text/unicode/norm"
)

// Regression tests for defects found by the adversarial review.

// lockOut makes dir unreadable (like a folder macOS privacy protection hides
// from this process) until the test ends.
func lockOut(t *testing.T, dir string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root ignores permissions")
	}
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
}

// commitFile commits one file, dated 10 days ago.
func commitFile(t *testing.T, dir, name, content, msg string) {
	t.Helper()
	write(t, filepath.Join(dir, name), content)
	git(t, dir, "add", name)
	old := time.Now().Add(-10 * 24 * time.Hour).Format(time.RFC3339)
	cmd := exec.Command("git", "-C", dir, "commit", "-q", "-m", msg)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE="+old, "GIT_COMMITTER_DATE="+old)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("commit: %v %s", err, out)
	}
}

// A renamed main repository still lists its worktrees (one locked, one with
// uncommitted work): they are not orphans to rm -rf but need a repair.
func TestRenamedMainIsRepairNotOrphan(t *testing.T) {
	gitEnv(t)
	home := newHome(t)
	main := newMain(t, home, "moved-repo")
	dirty := addWT(t, main, filepath.Join(home, ".codex", "worktrees", "zz99", "moved-repo"), "--detach")
	write(t, filepath.Join(dirty, "work.txt"), "wip\n")
	locked := addWT(t, main, filepath.Join(home, ".codex", "worktrees", "lk11", "moved-repo"), "--detach")
	git(t, main, "worktree", "lock", "--reason", "keep me", locked)
	renamed := main + "-renamed"
	if err := os.Rename(main, renamed); err != nil {
		t.Fatal(err)
	}

	c := scanWith(t, newEnv(home, &fakeRunner{}), fixedCwd("/"))
	for _, p := range []string{dirty, locked} {
		it := c.byPath(p)
		if it == nil {
			t.Fatalf("no item for %s", p)
		}
		if it.Method != core.MethodReport || it.CanClean() || it.Risk != core.RiskCaution ||
			it.Meta["status"] == statusOrphan || it.Project != renamed ||
			!strings.Contains(it.Warn, "git -C "+renamed+" worktree repair "+p) {
			t.Errorf("%s: method=%s cleanable=%v risk=%s status=%s project=%s warn=%q",
				p, it.Method, it.CanClean(), it.Risk, it.Meta["status"], it.Project, it.Warn)
		}
	}
	if it := c.byPath(locked); it.Meta["locked"] != "true" || it.Meta["status"] != statusLocked || !strings.Contains(it.Warn, "keep me") {
		t.Errorf("lock lost: meta=%v warn=%q", it.Meta, it.Warn)
	}
}

// Ignored secrets that exist only in the worktree make it caution (git
// worktree remove deletes them silently); copies of the main's files do not.
func TestIgnoredSecretsAreCaution(t *testing.T) {
	gitEnv(t)
	home := newHome(t)
	main := newMain(t, home, "app")
	write(t, filepath.Join(main, ".git", "info", "exclude"), ".env\n*.keystore\nsecrets/\n.claude/\n.qa-cache/\n")
	wt := addWT(t, main, filepath.Join(home, ".codex", "worktrees", "s1", "app"), "--detach")
	write(t, filepath.Join(wt, ".env"), "API_KEY=abc\n")
	write(t, filepath.Join(wt, "android", "app", "release.keystore"), "k")
	write(t, filepath.Join(wt, "secrets", "deep", "AuthKey.p8"), "k")
	write(t, filepath.Join(wt, ".claude", "settings.local.json"), "{}")
	write(t, filepath.Join(wt, "node_modules", "pkg", ".env"), "x") // dependency: not a secret
	write(t, filepath.Join(wt, ".qa-cache", "proj", ".env"), "x")   // cache of other projects
	idle(t, wt, 10*24*time.Hour)

	copied := addWT(t, main, filepath.Join(home, ".codex", "worktrees", "s2", "app"), "--detach")
	write(t, filepath.Join(main, ".env"), "SAME=1\n")
	write(t, filepath.Join(copied, ".env"), "SAME=1\n")
	idle(t, copied, 10*24*time.Hour)

	c := scanWith(t, newEnv(home, &fakeRunner{}), fixedCwd("/"))
	it := c.byPath(wt)
	if it == nil {
		t.Fatal("no item")
	}
	if it.Risk != core.RiskCaution || it.Recommended || !strings.Contains(it.Warn, "would be lost") {
		t.Errorf("secrets: risk=%s rec=%v warn=%q", it.Risk, it.Recommended, it.Warn)
	}
	for _, want := range []string{".env", "android/app/release.keystore", "secrets/deep/AuthKey.p8", ".claude/settings.local.json"} {
		if !strings.Contains(", "+it.Meta["env_files"]+",", ", "+want+",") {
			t.Errorf("env_files=%q misses %s", it.Meta["env_files"], want)
		}
	}
	if strings.Contains(it.Meta["env_files"], "node_modules") || strings.Contains(it.Meta["env_files"], ".qa-cache") {
		t.Errorf("dependency reported as secret: %q", it.Meta["env_files"])
	}
	if it := c.byPath(copied); it == nil || it.Risk != core.RiskModerate || !it.Recommended || it.Warn != "" {
		t.Errorf("identical copy of the main's .env: %+v", it)
	}
}

func TestIsSecretFile(t *testing.T) {
	for p, want := range map[string]bool{
		".env": true, "apps/web/.env.production": true, ".env.local": true, "prod.env": true,
		".env.example": false, ".env.sample": false, "config.template": false,
		"android/app/upload.keystore": true, "release.jks": true, "AuthKey_X.p8": true, "cert.p12": true,
		"ios/App/GoogleService-Info.plist": true, "android/app/google-services.json": true,
		"android/key.properties": true, "CLAUDE.local.md": true, ".claude/settings.local.json": true,
		"terraform.tfstate": true, "terraform.tfstate.backup": true, "id_rsa": true,
		"README.md": false, "src/index.ts": false, "package-lock.json": false, "build.log": false,
		"android/app/debug.keystore": false, ".yarnrc.yml": false,
	} {
		if got := isSecretFile(p); got != want {
			t.Errorf("%s: %v want %v", p, got, want)
		}
	}
}

// A main repository this process cannot read (chmod 000, macOS privacy
// protection) is not "deleted": its worktree is reported, never removed.
func TestUnreadableMainIsReportOnly(t *testing.T) {
	gitEnv(t)
	home := newHome(t)
	main := newMain(t, home, "priv/app")
	wt := addWT(t, main, filepath.Join(home, ".codex", "worktrees", "x1", "app"), "-b", "wip")
	write(t, filepath.Join(wt, "wip.go"), "package wip\n")
	lockOut(t, filepath.Dir(main))

	c := scanWith(t, newEnv(home, &fakeRunner{}), fixedCwd("/"))
	it := c.byPath(wt)
	if it == nil {
		t.Fatal("no item")
	}
	if it.Method != core.MethodReport || it.CanClean() || it.Meta["status"] == statusOrphan || it.Meta["orphan"] != "" ||
		it.Meta["unreadable"] == "" || !strings.Contains(it.Warn, "unreadable") {
		t.Errorf("unreadable main: method=%s cleanable=%v meta=%v warn=%q", it.Method, it.CanClean(), it.Meta, it.Warn)
	}
}

func TestConfirmedMissing(t *testing.T) {
	root := realPath(t.TempDir())
	os.MkdirAll(filepath.Join(root, "ok"), 0o755)
	if gone, err := confirmedMissing(filepath.Join(root, "ok", "a", "b")); !gone || err != nil {
		t.Errorf("missing below a readable dir: %v %v", gone, err)
	}
	if gone, err := confirmedMissing(filepath.Join(root, "ok")); gone || err != nil {
		t.Errorf("existing: %v %v", gone, err)
	}
	// A dangling symlink on the way (e.g. to an unplugged disk) proves nothing.
	os.Symlink(filepath.Join(root, "no-such-volume"), filepath.Join(root, "link"))
	if gone, err := confirmedMissing(filepath.Join(root, "link", "repo", ".git")); gone || err == nil {
		t.Errorf("dangling symlink ancestor: %v %v", gone, err)
	}
	os.MkdirAll(filepath.Join(root, "priv", "x"), 0o755)
	lockOut(t, filepath.Join(root, "priv"))
	if gone, err := confirmedMissing(filepath.Join(root, "priv", "x", "repo")); gone || err == nil {
		t.Errorf("below an unreadable dir: %v %v", gone, err)
	}
}

// The orphan verdict is verified again right before the removal.
func TestOrphanRecheck(t *testing.T) {
	gitEnv(t)
	home := newHome(t)
	gone := newMain(t, home, "gone/app")
	wt := addWT(t, gone, filepath.Join(home, ".codex", "worktrees", "o1", "app"), "-b", "b")
	os.RemoveAll(gone)

	c := scanWith(t, newEnv(home, &fakeRunner{}), fixedCwd("/"))
	it := c.byPath(wt)
	if it == nil || it.Method != core.MethodDelete || it.Recheck == nil || !it.AllowGitRepo || !it.NoRecommend {
		t.Fatalf("orphan item: %+v", it)
	}
	ctx := context.Background()
	if err := it.Recheck(ctx); err != nil {
		t.Fatalf("recheck of a verified orphan: %v", err)
	}
	// The folder that held the main repository becomes unreadable.
	parent := filepath.Dir(gone)
	os.Chmod(parent, 0)
	if os.Geteuid() != 0 {
		if err := it.Recheck(ctx); err == nil {
			t.Error("recheck must refuse when the main repository's folder is unreadable")
		}
	}
	os.Chmod(parent, 0o755)
	// A clone appears inside the orphan (below a build folder): rm -rf would
	// take it along.
	os.MkdirAll(filepath.Join(wt, "sub", "tmp", "lib"), 0o755)
	git(t, filepath.Join(wt, "sub", "tmp", "lib"), "init", "-q")
	if err := it.Recheck(ctx); err == nil || !strings.Contains(err.Error(), "another git repository") {
		t.Errorf("recheck must refuse an orphan holding a repository: %v", err)
	}
	os.RemoveAll(filepath.Join(wt, "sub"))
	if err := it.Recheck(ctx); err != nil {
		t.Errorf("recheck after removing the clone: %v", err)
	}
	// The main repository comes back (restored from a backup, re-cloned).
	os.MkdirAll(filepath.Join(gone, ".git", "worktrees", "app"), 0o755)
	if err := it.Recheck(ctx); err == nil {
		t.Error("recheck must refuse when the git metadata exists again")
	}
}

// `git worktree prune` also drops live worktrees git cannot lstat (an
// unreadable folder): only the verifiably deleted entry may go.
func TestPruneKeepsUnreadableCheckout(t *testing.T) {
	gitEnv(t)
	home := newHome(t)
	main := newMain(t, home, "app")
	hidden := addWT(t, main, filepath.Join(home, "src", "hidden", "wt"), "--detach")
	commitFile(t, hidden, "x.txt", "x\n", "detached work")
	gone := addWT(t, main, filepath.Join(home, "src", "app-gone"), "-b", "gone")
	goneAdmin := adminOf(t, gone)
	os.RemoveAll(gone)
	lockOut(t, filepath.Dir(hidden))

	c := scanWith(t, newEnv(home, &fakeRunner{}), fixedCwd("/"))
	pr := c.final["worktrees:prune:"+main]
	if pr == nil {
		t.Fatalf("no prune item: %v", keys(c.final))
	}
	if pr.Method != core.MethodDelete || len(pr.Command) != 0 || len(pr.Paths) != 1 || pr.Paths[0] != goneAdmin ||
		!strings.Contains(pr.Meta["kept"], "hidden") {
		t.Errorf("prune must delete only %s: method=%s paths=%v cmd=%v meta=%v", goneAdmin, pr.Method, pr.Paths, pr.Command, pr.Meta)
	}
	if pr.Recheck == nil || pr.Recheck(context.Background()) != nil {
		t.Errorf("targeted prune recheck")
	}
}

// Nothing prunable but the unreadable checkout: no prune item at all.
func TestPruneOnlyUnreadableCheckout(t *testing.T) {
	gitEnv(t)
	home := newHome(t)
	main := newMain(t, home, "app")
	hidden := addWT(t, main, filepath.Join(home, "src", "hidden", "wt"), "--detach")
	lockOut(t, filepath.Dir(hidden))
	c := scanWith(t, newEnv(home, &fakeRunner{}), fixedCwd("/"))
	if pr := c.final["worktrees:prune:"+main]; pr != nil {
		t.Errorf("prune proposed for an unreadable live worktree: %+v", pr)
	}
}

// The prune command is re-evaluated by git at clean time: its Recheck refuses
// when an entry became prunable after the scan (unmounted, moved away).
func TestPruneCommandRecheck(t *testing.T) {
	gitEnv(t)
	home := newHome(t)
	main := newMain(t, home, "app")
	gone := addWT(t, main, filepath.Join(home, "src", "app-gone"), "-b", "gone")
	os.RemoveAll(gone)
	vol := addWT(t, main, filepath.Join(home, "src", "vol", "wt"), "--detach")

	c := scanWith(t, newEnv(home, &fakeRunner{}), fixedCwd("/"))
	pr := c.final["worktrees:prune:"+main]
	if pr == nil || pr.Method != core.MethodCommand || pr.Recheck == nil {
		t.Fatalf("prune item: %+v", pr)
	}
	ctx := context.Background()
	if err := pr.Recheck(ctx); err != nil {
		t.Fatalf("unchanged: %v", err)
	}
	if os.Geteuid() != 0 {
		os.Chmod(filepath.Dir(vol), 0) // the disk holding it is not readable anymore
		if err := pr.Recheck(ctx); err == nil {
			t.Error("recheck must refuse: git would now prune the unreadable worktree")
		}
		os.Chmod(filepath.Dir(vol), 0o755)
	}
	if err := os.Rename(vol, vol+"-moved"); err != nil {
		t.Fatal(err)
	}
	if err := pr.Recheck(ctx); err == nil {
		t.Error("recheck must refuse: git would now prune the moved worktree")
	}
}

// A submodule checked out under <super>/worktrees/ is not a linked worktree;
// a repository living under a "modules" folder is a normal one.
func TestSubmoduleUnderWorktreesDir(t *testing.T) {
	gitEnv(t)
	home := newHome(t)
	newMain(t, home, "lib")
	super := newMain(t, home, "super")
	git(t, super, "-c", "protocol.file.allow=always", "submodule", "add", "-q", filepath.Join(home, "remotes", "lib.git"), "worktrees/lib")
	git(t, super, "commit", "-q", "-m", "sub")
	sub := filepath.Join(super, "worktrees", "lib")
	idle(t, sub, 10*24*time.Hour)

	mods := newMain(t, home, "modules/app")
	wt := addWT(t, mods, filepath.Join(home, "src", "modules", "app-wt"), "-b", "x")

	c := scanWith(t, newEnv(home, &fakeRunner{}), fixedCwd("/"))
	if it := c.byPath(sub); it != nil {
		t.Errorf("submodule emitted as a worktree: %+v", it)
	}
	for id, it := range c.final {
		if strings.Contains(it.Project+"/", "/.git/modules/") {
			t.Errorf("%s: submodule git dir used as a main repository", id)
		}
	}
	if it := c.byPath(wt); it == nil || it.Project != mods {
		t.Errorf("worktree of a repository under a modules/ folder: %+v", it)
	}
}

func TestCommonDirRejectsLiveNonWorktreeGitdir(t *testing.T) {
	root := realPath(t.TempDir())
	// A submodule named worktrees/lib and any live admin-looking dir without
	// commondir are not linked worktrees.
	for _, g := range []string{
		filepath.Join(root, "super", ".git", "modules", "worktrees", "lib"),
		filepath.Join(root, "other", "worktrees", "lib"),
	} {
		write(t, filepath.Join(g, "HEAD"), "ref: refs/heads/main\n")
		if c, ok := commonDir(context.Background(), g); ok {
			t.Errorf("%s: common=%s ok", g, c)
		}
	}
	// Gone admin dir: derived from the layout only for a .git / bare dir.
	if _, ok := commonDir(context.Background(), filepath.Join(root, "gone", "src", "worktrees", "x")); ok {
		t.Error("layout fallback accepted a non-git dir")
	}
	if c, ok := commonDir(context.Background(), filepath.Join(root, "gone", "modules", "app", ".git", "worktrees", "x")); !ok || c != filepath.Join(root, "gone", "modules", "app", ".git") {
		t.Errorf("repository under modules/: %s %v", c, ok)
	}
}

func TestCommonDirCancellationDoesNotDeriveOrphan(t *testing.T) {
	gitdir := filepath.Join(t.TempDir(), ".git", "worktrees", "removed")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if common, ok := commonDir(ctx, gitdir); ok || common != "" {
		t.Fatalf("cancelled lookup derived an orphan's common directory: %q %v", common, ok)
	}
}

// A detached HEAD whose commits were cherry-picked to the default branch
// still loses them on removal (the cleaner refuses): not merged-and-recommended.
func TestDetachedCherryPickedIsNotRecommended(t *testing.T) {
	gitEnv(t)
	home := newHome(t)
	main := newMain(t, home, "app")
	wt := addWT(t, main, filepath.Join(home, ".codex", "worktrees", "c1", "app"), "--detach")
	commitFile(t, wt, "x.txt", "x\n", "x")
	sha := git(t, wt, "rev-parse", "HEAD")
	commitFile(t, main, "y.txt", "y\n", "y")
	git(t, main, "cherry-pick", sha)
	git(t, main, "push", "-q", "origin", "main")
	idle(t, wt, 10*24*time.Hour)

	c := scanWith(t, newEnv(home, &fakeRunner{}), fixedCwd("/"))
	it := c.byPath(wt)
	if it == nil {
		t.Fatal("no item")
	}
	if it.Recommended || it.Risk != core.RiskCaution || it.Meta["unpushed"] != "1" || !strings.Contains(it.Warn, "detached HEAD") {
		t.Errorf("rec=%v risk=%s meta=%v warn=%q", it.Recommended, it.Risk, it.Meta, it.Warn)
	}
}

// Other repositories or worktrees inside a worktree would be deleted with it.
func TestNestedRepoIsCaution(t *testing.T) {
	gitEnv(t)
	home := newHome(t)
	main := newMain(t, home, "app")
	write(t, filepath.Join(main, ".git", "info", "exclude"), "vendor/\ndist/\n")
	wt := addWT(t, main, filepath.Join(home, "src", "app-nested"), "-b", "n")
	os.MkdirAll(filepath.Join(wt, "vendor", "lib"), 0o755)
	git(t, filepath.Join(wt, "vendor", "lib"), "init", "-q")
	dist := addWT(t, main, filepath.Join(wt, "dist"), "--detach") // gh-pages style
	idle(t, wt, 10*24*time.Hour)
	idle(t, dist, 10*24*time.Hour)

	// An orphan holding a clone must not be removed with rm -rf.
	orphan := addWT(t, main, filepath.Join(home, ".codex", "worktrees", "o2", "app"), "--detach")
	os.RemoveAll(adminOf(t, orphan))
	os.MkdirAll(filepath.Join(orphan, "extra"), 0o755)
	git(t, filepath.Join(orphan, "extra"), "init", "-q")

	c := scanWith(t, newEnv(home, &fakeRunner{}), fixedCwd("/"))
	it := c.byPath(wt)
	if it == nil {
		t.Fatal("no item")
	}
	if it.Risk != core.RiskCaution || it.Recommended || !strings.Contains(it.Warn, "contains another git repository or worktree") ||
		!strings.Contains(it.Meta["nested"], filepath.Join(wt, "vendor", "lib")) || !strings.Contains(it.Meta["nested"], dist) {
		t.Errorf("nested: risk=%s rec=%v warn=%q meta=%v", it.Risk, it.Recommended, it.Warn, it.Meta)
	}
	if it := c.byPath(dist); it == nil || it.Risk == core.RiskCaution && strings.Contains(it.Warn, "contains another") {
		t.Errorf("the inner worktree itself: %+v", it)
	}
	if it := c.byPath(orphan); it == nil || it.Method != core.MethodReport || it.CanClean() {
		t.Errorf("orphan with a nested clone: %+v", it)
	}
}

// A Codex thread started in a worktree after the scan blocks its removal.
func TestSessionRecheck(t *testing.T) {
	gitEnv(t)
	home := newHome(t)
	main := newMain(t, home, "app")
	a := addWT(t, main, filepath.Join(home, ".codex", "worktrees", "r1", "app"), "--detach")
	b := addWT(t, main, filepath.Join(home, ".codex", "worktrees", "r2", "app"), "--detach")
	idle(t, a, 10*24*time.Hour)
	idle(t, b, 10*24*time.Hour)
	write(t, filepath.Join(home, ".codex", "state_5.sqlite"), "")
	r := &fakeRunner{sqlite: map[string]string{}}
	c := scanWith(t, newEnv(home, r), fixedCwd("/"))
	ia, ib := c.byPath(a), c.byPath(b)
	if ia == nil || ib == nil || !ia.Recommended || ia.Recheck == nil || ib.Recheck == nil {
		t.Fatalf("items: %+v %+v", ia, ib)
	}
	r.sqlite["from threads"] = fmt.Sprintf(`[{"cwd":%q,"archived":0,"n":1,"u":%d}]`, a, time.Now().Unix())
	ctx := context.Background()
	if err := ia.Recheck(ctx); err == nil || !strings.Contains(err.Error(), "Codex thread") {
		t.Errorf("new Codex thread not detected: %v", err)
	}
	if err := ib.Recheck(ctx); err != nil {
		t.Errorf("unrelated worktree refused: %v", err)
	}
}

// Tools store paths in their own case and Unicode normalization (APFS
// ignores both): a thread in the NFD / upper-case spelling still counts.
func TestToolPathsCaseAndNormalization(t *testing.T) {
	gitEnv(t)
	home := newHome(t)
	main := newMain(t, home, "app")
	wt := addWT(t, main, filepath.Join(home, ".codex", "worktrees", "café", "app"), "--detach")
	idle(t, wt, 10*24*time.Hour)
	write(t, filepath.Join(home, ".codex", "state_5.sqlite"), "")
	spelled := strings.ToUpper(norm.NFD.String(wt))
	r := &fakeRunner{sqlite: map[string]string{
		"from threads": fmt.Sprintf(`[{"cwd":%q,"archived":0,"n":1,"u":%d}]`, spelled, time.Now().Unix()),
	}}
	c := scanWith(t, newEnv(home, r), fixedCwd("/"))
	if it := c.byPath(wt); it == nil || it.Risk != core.RiskCaution || !strings.Contains(it.Warn, "active Codex thread") {
		t.Errorf("thread under another spelling missed: %+v", it)
	}
}

// Rows committed to the WAL but not yet checkpointed (a thread just started)
// must be visible: immutable=1 alone would ignore them.
func TestSqliteReadsWAL(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 not installed")
	}
	db := filepath.Join(t.TempDir(), "state_5.sqlite")
	run := func(stdin string, args ...string) {
		t.Helper()
		cmd := exec.Command("sqlite3", append([]string{db}, args...)...)
		cmd.Stdin = strings.NewReader(stdin)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("sqlite3: %v %s", err, out)
		}
	}
	run("", "PRAGMA journal_mode=WAL; create table threads(cwd text); insert into threads values('/a');")
	run(".dbconfig no_ckpt_on_close on\ninsert into threads values('/b');\n")
	if _, err := os.Stat(db + "-wal"); err != nil {
		t.Skip("this sqlite3 checkpointed on close")
	}
	env := &core.Env{Runner: core.ExecRunner{}}
	var rows []struct {
		Cwd string `json:"cwd"`
	}
	if !sqliteJSON(context.Background(), env, db, "select cwd from threads order by cwd", &rows) || len(rows) != 2 {
		t.Errorf("rows=%v, want /a and /b (the WAL row)", rows)
	}
}

// When the WAL-aware open fails, the immutable read is the fallback.
func TestSqliteFallsBackToImmutable(t *testing.T) {
	db := filepath.Join(t.TempDir(), "x.sqlite")
	write(t, db, "")
	r := &uriRunner{}
	env := &core.Env{Runner: r}
	var rows []struct {
		N int `json:"n"`
	}
	if !sqliteJSON(context.Background(), env, db, "select 1 as n", &rows) || len(rows) != 1 {
		t.Errorf("fallback: rows=%v", rows)
	}
	if len(r.uris) != 2 || strings.Contains(r.uris[0], "immutable") || !strings.Contains(r.uris[1], "immutable=1") {
		t.Errorf("uris=%v", r.uris)
	}
}

type uriRunner struct{ uris []string }

func (r *uriRunner) Output(_ context.Context, _, _ string, args ...string) ([]byte, error) {
	uri := args[len(args)-2]
	r.uris = append(r.uris, uri)
	if !strings.Contains(uri, "immutable=1") {
		return nil, errors.New("unable to open database file")
	}
	return []byte(`[{"n":1}]`), nil
}

func (r *uriRunner) LookPath(name string) (string, error) { return "/fake/" + name, nil }
