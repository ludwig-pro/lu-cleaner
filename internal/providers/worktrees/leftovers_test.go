package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
	"github.com/ludwig-pro/lu-cleaner/internal/safety"
)

// Regression tests for the integration leftovers (W1-W3).

// W1: an orphan checkout is removed with rm -rf and git can no longer see its
// uncommitted work: the executor must demand --force. Tracked worktrees
// (git worktree remove) and report-only orphans do not carry the flag.
func TestOrphanRequiresForce(t *testing.T) {
	gitEnv(t)
	home := newHome(t)
	main := newMain(t, home, "app")
	tracked := addWT(t, main, filepath.Join(home, ".codex", "worktrees", "t1", "app"), "-b", "t1")

	gone := newMain(t, home, "gone/app")
	mainGone := addWT(t, gone, filepath.Join(home, ".codex", "worktrees", "o1", "app"), "-b", "b")
	os.RemoveAll(gone)

	pruned := addWT(t, main, filepath.Join(home, ".codex", "worktrees", "o2", "app"), "--detach")
	os.RemoveAll(adminOf(t, pruned))

	// An orphan holding a clone found only while sizing it: report only.
	nested := addWT(t, main, filepath.Join(home, ".codex", "worktrees", "o3", "app"), "--detach")
	os.RemoveAll(adminOf(t, nested))
	os.MkdirAll(filepath.Join(nested, "extra"), 0o755)
	git(t, filepath.Join(nested, "extra"), "init", "-q")

	c := scanWith(t, newEnv(home, &fakeRunner{}), fixedCwd("/"))
	for _, p := range []string{mainGone, pruned} {
		it := c.byPath(p)
		if it == nil || it.Method != core.MethodDelete || !it.RequireForce || !it.AllowGitRepo || it.Recheck == nil {
			t.Errorf("orphan %s must be deleted only with --force: %+v", p, it)
		}
	}
	if it := c.byPath(tracked); it == nil || it.Method != core.MethodWorktree || it.RequireForce {
		t.Errorf("tracked worktree must not require --force: %+v", it)
	}
	if it := c.byPath(nested); it == nil || it.Method != core.MethodReport || it.CanClean() || it.RequireForce || it.AllowGitRepo {
		t.Errorf("orphan holding a clone must be report only: %+v", it)
	}
}

// W2: the areas where the scan proposes removals mirror the safety guard
// (safety.TempAreas). An unset or shared TMPDIR (/tmp) used to open its
// parent, i.e. / and /private; a private TMPDIR opened its parent folder.
func TestAllowedAreasMirrorGuard(t *testing.T) {
	home := newHome(t)
	env := newEnv(home, &fakeRunner{})
	emit := func(*core.Item) {}

	env.TmpDir = "/tmp"
	s := newScan(context.Background(), env, emit)
	for _, p := range []string{"/private/tmp/wt/app", "/tmp/wt/app", "/private/var/wt/app", "/Users/Shared/wt/app"} {
		if s.allowedPath(p) {
			t.Errorf("TMPDIR=/tmp must not allow %s (allowed areas %v)", p, s.allowed)
		}
	}
	if !s.allowedPath(filepath.Join(home, "wt")) {
		t.Errorf("home must stay allowed (allowed areas %v)", s.allowed)
	}

	base := realPath(t.TempDir())
	tmp := filepath.Join(base, "T")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	env.TmpDir = tmp
	s = newScan(context.Background(), env, emit)
	if !s.allowedPath(filepath.Join(tmp, "wt", "app")) {
		t.Errorf("inside TMPDIR must be allowed (allowed areas %v)", s.allowed)
	}
	for _, p := range []string{tmp, filepath.Join(base, "other", "app")} {
		if s.allowedPath(p) {
			t.Errorf("%s is not inside TMPDIR (allowed areas %v)", p, s.allowed)
		}
	}
	// Same verdict as the guard's own list.
	want := append([]string{realPath(home)}, safety.TempAreas(tmp)...)
	if len(s.allowed) != len(want) {
		t.Errorf("allowed areas %v, want %v", s.allowed, want)
	}
}

// W3: a worktree whose files are all shared with other files (hardlinks or
// APFS clones) frees about nothing: Reclaim must not stay 0, which means
// "same as Size".
func TestApplySizeFullyShared(t *testing.T) {
	s := &scan{ctx: t.Context(), now: time.Now()}
	for _, tc := range []struct{ total, reclaim, want int64 }{
		{1 << 20, 0, 1},
		{1 << 20, 4096, 4096},
		{1 << 20, 1 << 20, 0},
	} {
		it := &core.Item{}
		s.applySize(&worktree{}, it, measurement{total: tc.total, reclaim: tc.reclaim, artifacts: map[string]int64{}})
		if it.Size != tc.total || it.Reclaim != tc.want {
			t.Errorf("total %d reclaim %d: Size %d Reclaim %d, want Reclaim %d", tc.total, tc.reclaim, it.Size, it.Reclaim, tc.want)
		}
		shared, ok := it.Meta["shared_hardlinks"]
		if tc.want > 0 && shared != fsx.Bytes(tc.total-tc.reclaim) || tc.want == 0 && ok {
			t.Errorf("total %d reclaim %d: shared %q", tc.total, tc.reclaim, shared)
		}
	}
}
