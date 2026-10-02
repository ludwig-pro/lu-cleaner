package worktrees

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/providers/internal/scanmemo"
)

// Checks run by the cleaner right before removing an item (core.Item.Recheck,
// also in dry-run): the state seen by the scan may be minutes or hours old.

// orphanRecheck verifies that an orphan checkout is still an orphan for the
// same verified reason: its .git file unchanged, and its admin dir (and, for a
// deleted main repository, the common dir) still verifiably gone — or, for a
// copy, the other checkout still owning the admin entry.
func (s *scan) orphanRecheck(w *worktree) func(context.Context) error {
	path, key, gitdir, common, copyOf, why := w.path, w.key, w.gitdir, w.common, w.copyOf, w.orphan
	return func(context.Context) error {
		if k, ok := keyOf(path); !ok || k != key {
			return fmt.Errorf("%s changed since the scan — rescan", path)
		}
		if g, ok := readGitFile(path); !ok || g != gitdir {
			return fmt.Errorf("the .git file of %s changed since the scan — rescan", path)
		}
		if why == orphanCopy {
			back := readTrim(filepath.Join(gitdir, "gitdir"))
			if back != "" && !filepath.IsAbs(back) {
				back = filepath.Join(gitdir, back)
			}
			if back == "" || filepath.Dir(filepath.Clean(back)) != copyOf {
				return fmt.Errorf("git metadata %s changed since the scan — rescan", gitdir)
			}
			if k, ok := keyOf(copyOf); !ok || k == key || gitKind(copyOf) != gitFile {
				return fmt.Errorf("%s is no longer the checkout git tracks — rescan", copyOf)
			}
		} else {
			if err := stillGone(gitdir, "git metadata"); err != nil {
				return err
			}
			if why == orphanMainGone {
				if err := stillGone(common, "main repository"); err != nil {
					return err
				}
			}
		}
		// Every orphan is removed with rm -rf, which the safety guard does not
		// stop for a repository inside the target.
		return noNestedRepo(path)
	}
}

// noNestedRepo refuses an orphan (removed with rm -rf) that holds another
// repository or worktree, which would go with it.
func noNestedRepo(path string) error {
	found, err := findNested(path)
	switch {
	case err != nil:
		return fmt.Errorf("cannot verify that %s holds no other git repository (%v) — remove it yourself", path, errText(err))
	case found != "":
		return fmt.Errorf("%s contains another git repository or worktree (%s) — remove it yourself", path, found)
	}
	return nil
}

// nestedWalkMax bounds the walk of findNested.
const nestedWalkMax = 500_000

// depDirs are dependency trees installed by package managers: findNested
// checks them but does not descend into them (they hold no user clones and
// can be huge). Build output, dist/, tmp/... are searched.
var depDirs = map[string]bool{
	"node_modules": true, "pods": true, "deriveddata": true, ".gradle": true, ".cxx": true,
	".pnpm-store": true, "__pycache__": true, ".venv": true, "venv": true, ".dart_tool": true,
	".next": true, ".turbo": true, ".expo": true, ".yarn": true,
}

// findNested returns a git repository or worktree strictly inside root: a
// directory holding a .git entry of any type, or a bare layout. Dependency
// trees are checked themselves but not descended into; symlinks are not
// followed. It fails closed: an unreadable folder or a tree too large to walk
// is an error.
func findNested(root string) (string, error) {
	seen := 0
	var walk func(dir string, depth int) (string, error)
	walk = func(dir string, depth int) (string, error) {
		des, err := os.ReadDir(dir)
		if err != nil {
			return "", err
		}
		if seen += len(des); seen > nestedWalkMax {
			return "", fmt.Errorf("more than %d entries", nestedWalkMax)
		}
		if depth > 0 {
			for _, de := range des {
				if de.Name() == ".git" {
					return dir, nil
				}
			}
			if looksBare(dir) {
				return dir, nil
			}
			if depDirs[strings.ToLower(filepath.Base(dir))] {
				return "", nil
			}
		}
		for _, de := range des {
			if !de.IsDir() || de.Name() == ".git" { // IsDir is false for symlinks
				continue
			}
			if found, err := walk(filepath.Join(dir, de.Name()), depth+1); found != "" || err != nil {
				return found, err
			}
		}
		return "", nil
	}
	return walk(root, 0)
}

// stillGone returns nil when p verifiably does not exist.
func stillGone(p, what string) error {
	if v := offlineVolume(p); v != "" {
		return fmt.Errorf("%s %s is on unmounted volume %s — nothing removed", what, p, v)
	}
	gone, err := confirmedMissing(p)
	switch {
	case err != nil:
		return fmt.Errorf("cannot verify that %s %s is gone (%s) — nothing removed", what, p, errText(err))
	case !gone:
		return fmt.Errorf("%s %s exists again — rescan", what, p)
	}
	return nil
}

// liveToolsTTL is how long the tool state read by rechecks is reused (one
// clean run removes many worktrees in a row).
const liveToolsTTL = 30 * time.Second

// liveTools caches the AI tools' session state for rechecks.
type liveTools struct {
	cache scanmemo.Cache[struct{}, *toolState]
}

// freshTools reads the Codex, Claude desktop and Conductor state again (at
// most every liveToolsTTL), sharing an in-flight load without holding a lock.
func (s *scan) freshTools(ctx context.Context) (*toolState, error) {
	return s.live.cache.GetTTL(ctx, struct{}{}, liveToolsTTL, func(ctx context.Context) (*toolState, error) {
		ts := newToolState()
		for _, f := range []func(context.Context, *core.Env, string){ts.loadCodex, ts.loadClaudeDesktop, ts.loadConductor} {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			func() {
				defer func() { _ = recover() }() // tool state is best effort
				f(ctx, s.env, s.home)
			}()
		}
		return ts, ctx.Err()
	})
}

// sessionRecheck refuses a worktree that an AI tool started using after the
// scan (new or unarchived Codex thread, Claude desktop session, Conductor
// workspace): removing it would pull the folder from under that session. A
// session already known at scan time is shown in the warning: the user chose.
func (s *scan) sessionRecheck(w *worktree) func(context.Context) error {
	if w.session != "" {
		return nil
	}
	path := w.path
	return func(ctx context.Context) error {
		probe := &worktree{path: path}
		ts, err := s.freshTools(ctx)
		if err != nil {
			return err
		}
		ts.apply(probe)
		if probe.session != "" {
			return fmt.Errorf("%s since the scan — rescan", probe.session)
		}
		return nil
	}
}
