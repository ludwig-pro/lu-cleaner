package worktrees

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"golang.org/x/text/unicode/norm"
)

// Regression tests added while re-verifying the fixes of the adversarial
// review.

// A copy of a worktree (cp -R) is an orphan removed with rm -rf: like the
// other orphans, its clean-time check must refuse it once another repository
// appeared inside (the safety guard does not look inside the target).
func TestCopyOrphanRecheckRefusesNestedRepo(t *testing.T) {
	gitEnv(t)
	home := newHome(t)
	main := newMain(t, home, "app")
	wt := addWT(t, main, filepath.Join(home, ".codex", "worktrees", "c1", "app"), "-b", "c1")
	cp := filepath.Join(home, ".codex", "worktrees", "c2", "app")
	os.MkdirAll(filepath.Dir(cp), 0o755)
	if out, err := exec.Command("cp", "-R", wt, cp).CombinedOutput(); err != nil {
		t.Fatalf("cp: %v %s", err, out)
	}

	c := scanWith(t, newEnv(home, &fakeRunner{}), fixedCwd("/"))
	it := c.byPath(cp)
	if it == nil || it.Meta["copy_of"] != wt || it.Method != core.MethodDelete || it.Recheck == nil {
		t.Fatalf("copy item: %+v", it)
	}
	ctx := context.Background()
	if err := it.Recheck(ctx); err != nil {
		t.Fatalf("recheck of an unchanged copy: %v", err)
	}
	clone := filepath.Join(cp, "vendor", "lib")
	os.MkdirAll(clone, 0o755)
	git(t, clone, "init", "-q")
	if err := it.Recheck(ctx); err == nil || !strings.Contains(err.Error(), "another git repository") {
		t.Errorf("recheck must refuse a copy holding a repository: %v", err)
	}
}

// Removing a worktree deletes a symlink, never its target: an ignored .env
// that links to a file elsewhere is not lost (no caution for it), while an
// ignored real file still is.
func TestSymlinkedSecretIsNotLost(t *testing.T) {
	gitEnv(t)
	home := newHome(t)
	main := newMain(t, home, "app")
	write(t, filepath.Join(main, ".git", "info", "exclude"), ".env\n.env.prod\n")
	write(t, filepath.Join(home, "secrets", "app.env"), "API_KEY=abc\n")

	linked := addWT(t, main, filepath.Join(home, ".codex", "worktrees", "l1", "app"), "--detach")
	if err := os.Symlink(filepath.Join(home, "secrets", "app.env"), filepath.Join(linked, ".env")); err != nil {
		t.Fatal(err)
	}
	idle(t, linked, 10*24*time.Hour)

	real := addWT(t, main, filepath.Join(home, ".codex", "worktrees", "l2", "app"), "--detach")
	if err := os.Symlink(".env.prod", filepath.Join(real, ".env")); err != nil { // link to a file lost with it
		t.Fatal(err)
	}
	write(t, filepath.Join(real, ".env.prod"), "API_KEY=abc\n")
	idle(t, real, 10*24*time.Hour)

	c := scanWith(t, newEnv(home, &fakeRunner{}), fixedCwd("/"))
	if it := c.byPath(linked); it == nil || it.Risk != core.RiskModerate || it.Meta["env_files"] != "" || !it.Recommended {
		t.Errorf("symlinked .env reported as lost: %+v", it)
	}
	if it := c.byPath(real); it == nil || it.Risk != core.RiskCaution || it.Meta["env_files"] != ".env.prod" {
		t.Errorf("real ignored secret: %+v", it)
	}
}

// The current directory may be spelled in another case or Unicode
// normalization than the scan found the worktree (APFS ignores both): it is
// still recognised, and the worktree cannot be selected.
func TestCurrentDirOtherSpelling(t *testing.T) {
	gitEnv(t)
	home := newHome(t)
	main := newMain(t, home, "app")
	wt := addWT(t, main, filepath.Join(home, ".codex", "worktrees", "café", "app"), "--detach")
	idle(t, wt, 10*24*time.Hour)
	cwd := strings.ToUpper(norm.NFD.String(filepath.Join(wt, "src")))
	os.MkdirAll(filepath.Join(wt, "src"), 0o755)
	if _, err := os.Stat(cwd); err != nil {
		t.Skip("case- or normalization-sensitive filesystem")
	}
	c := scanWith(t, newEnv(home, &fakeRunner{}), fixedCwd(cwd))
	if it := c.byPath(wt); it == nil || it.Selectable || it.Recommended || !strings.Contains(it.Warn, "current directory") {
		t.Errorf("current directory under another spelling: %+v", it)
	}
}

// Regenerated local files are not secrets: React Native's ios/.xcode.env.local
// (the node path, rewritten by pod install) must not make every React Native
// worktree caution.
func TestRegeneratedLocalFilesAreNotSecrets(t *testing.T) {
	for p, want := range map[string]bool{
		"ios/.xcode.env.local": false, "apps/mobile/ios/.xcode.env.local": false,
		"ios/.xcode.env": true, ".env.local": true, "android/local.properties": false,
	} {
		if got := isSecretFile(p); got != want {
			t.Errorf("%s: %v want %v", p, got, want)
		}
	}
}

// `git worktree repair` cannot restore a pruned admin entry: the note of such
// an orphan must not suggest it (it does for a moved main repository).
func TestPrunedOrphanNoteHasNoRepair(t *testing.T) {
	gitEnv(t)
	home := newHome(t)
	main := newMain(t, home, "app")
	wt := addWT(t, main, filepath.Join(home, ".codex", "worktrees", "p1", "app"), "--detach")
	os.RemoveAll(adminOf(t, wt))
	c := scanWith(t, newEnv(home, &fakeRunner{}), fixedCwd("/"))
	it := c.byPath(wt)
	if it == nil || it.Meta["orphan"] != orphanPruned {
		t.Fatalf("pruned orphan: %+v", it)
	}
	if strings.Contains(it.Note, "worktree repair") || !strings.Contains(it.Note, "worktree add") {
		t.Errorf("note: %q", it.Note)
	}
}
