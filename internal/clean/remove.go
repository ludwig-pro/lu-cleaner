package clean

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/safety"
	"golang.org/x/sys/unix"
)

var errSkip = errors.New("skipped")

// RemoveAll deletes path permanently. A symlink is removed itself (never
// followed). Read-only directories (Go module cache, some SDKs) and BSD
// immutable flags are cleared before a second attempt.
func RemoveAll(path string) error {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		if err := os.Remove(path); err != nil {
			if fi.Mode()&fs.ModeSymlink != 0 {
				return err
			}
			_ = unix.Chflags(path, 0) // clear uchg/schg-like user flags
			return os.Remove(path)
		}
		return nil
	}
	if err := os.RemoveAll(path); err == nil {
		return nil
	}
	_ = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if d == nil || d.Type()&fs.ModeSymlink != 0 {
			return nil // never touch symlink targets
		}
		_ = unix.Chflags(p, 0)
		if d.IsDir() {
			_ = os.Chmod(p, 0o755)
		}
		return nil
	})
	return os.RemoveAll(path)
}

// MoveToTrash moves path into ~/.Trash (renaming on collision) and returns
// the destination. Only works on the same volume as the home directory.
func MoveToTrash(home, path string) (string, error) {
	trash := filepath.Join(home, ".Trash")
	if err := os.MkdirAll(trash, 0o700); err != nil {
		return "", err
	}
	base := filepath.Base(path)
	dst := filepath.Join(trash, base)
	if _, err := os.Lstat(dst); err == nil {
		dst = filepath.Join(trash, fmt.Sprintf("%s %s", base, time.Now().Format("2006-01-02 15.04.05.000")))
	}
	if err := os.Rename(path, dst); err != nil {
		if errors.Is(err, unix.EXDEV) {
			return "", fmt.Errorf("%s is on another volume, cannot move it to the Trash", path)
		}
		return "", err
	}
	return dst, nil
}

// orphanedCommits returns how many commits removing the worktree would make
// unreachable: 0 when HEAD is on a branch (the branch survives the removal),
// otherwise the commits of the detached HEAD contained in no branch, tag or
// remote-tracking ref.
func orphanedCommits(ctx context.Context, r core.Runner, wt string) (int, error) {
	if _, err := r.Output(ctx, wt, "git", "symbolic-ref", "-q", "HEAD"); err == nil {
		return 0, nil
	}
	out, err := r.Output(ctx, wt, "git", "rev-list", "--count", "HEAD", "--not", "--branches", "--tags", "--remotes")
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, fmt.Errorf("unexpected rev-list output %q", strings.TrimSpace(string(out)))
	}
	return n, nil
}

// removeWorktree removes a linked git worktree. Unless opt.Force, it refuses
// when the worktree is locked, has uncommitted changes, or has commits that
// exist on no branch (detached HEAD). Branches are always kept.
func removeWorktree(ctx context.Context, it *core.Item, opt Options) (string, error) {
	path := it.Path
	fi, err := os.Lstat(filepath.Join(path, ".git"))
	if err != nil || fi.IsDir() {
		return "not a linked worktree (no .git file) — refusing", errSkip
	}
	if !opt.Force {
		if it.Meta["locked"] == "true" {
			return "worktree is locked (git worktree lock) — use --force", errSkip
		}
		// Unpushed commits on a branch are safe: `git worktree remove` keeps the
		// branch. Only commits reachable from nothing but this worktree's
		// detached HEAD would be lost.
		n, err := orphanedCommits(ctx, opt.Runner, path)
		if err != nil {
			return "cannot tell whether commits would be lost (" + err.Error() + ") — use --force", errSkip
		}
		if n > 0 {
			return fmt.Sprintf("detached HEAD with %d commit(s) on no branch — they would be lost; create a branch (git branch <name>) or use --force", n), errSkip
		}
		out, err := opt.Runner.Output(ctx, path, "git", "status", "--porcelain")
		if err == nil {
			if n := countLines(string(out)); n > 0 {
				return fmt.Sprintf("%d uncommitted change(s) — commit them or use --force", n), errSkip
			}
		} else if it.Project != "" && dirExists(it.Project) {
			// git works on the main repo but not here: be conservative.
			return "cannot read git status — use --force", errSkip
		}
	}

	main := it.Project
	if main != "" && dirExists(main) {
		// Without --force git itself refuses dirty, untracked, locked or
		// submodule worktrees: a second line of defence after our checks.
		// Ignored files (node_modules, Pods...) never block it.
		args := []string{"git", "worktree", "remove", path}
		if opt.Force {
			args = []string{"git", "worktree", "remove", "--force", "--force", path}
		}
		if _, err := runCmd(ctx, opt.Runner, main, args); err != nil {
			if !opt.Force {
				return "git refused to remove it (" + err.Error() + ") — use --force", errSkip
			}
			return "", err
		}
		removeEmptyToolParent(path, opt)
		return "git worktree removed (branch kept)", nil
	}
	// Main repository gone: git cannot help, remove the orphaned directory.
	if err := opt.Guard.Check(path, safetyOpts()); err != nil {
		return "", err
	}
	if err := RemoveAll(path); err != nil {
		return "", err
	}
	removeEmptyToolParent(path, opt)
	return "orphaned worktree directory removed", nil
}

// toolMarkers are the only files a tool leaves next to a worktree in its
// per-task folder (e.g. ~/.codex/worktrees/<id>/{<repo>,.codex-worktree-name}).
var toolMarkers = map[string]bool{".codex-worktree-name": true, ".DS_Store": true}

// removeEmptyToolParent deletes the per-task parent folder of a removed
// worktree when nothing but tool marker files is left in it.
func removeEmptyToolParent(path string, opt Options) {
	parent := filepath.Dir(path)
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) == 0 {
		return
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || !toolMarkers[e.Name()] || !info.Mode().IsRegular() || info.Size() > 4096 {
			return
		}
	}
	if opt.Guard.Check(parent, safety.Options{}) == nil {
		_ = RemoveAll(parent)
	}
}

func countLines(s string) int {
	n := 0
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			n++
		}
	}
	return n
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func safetyOpts() safety.Options { return safety.Options{} }
