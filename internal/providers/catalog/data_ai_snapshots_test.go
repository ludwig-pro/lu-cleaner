package catalog

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/safety"
)

// A Claude Code shell snapshot is sourced by every command of its session:
// sessions left open for days still use it. Snapshots have their own entry
// with a week threshold; the 1-day debug-log entry never matches them.
func TestClaudeShellSnapshotsOwnEntry(t *testing.T) {
	home, _ := filepath.EvalSymlinks(t.TempDir())
	day := 24 * time.Hour
	mkFile(t, filepath.Join(home, ".claude/debug/old.txt"), 10, 3*day)
	mkFile(t, filepath.Join(home, ".claude/shell-snapshots/snapshot-zsh-live.sh"), 10, 3*day)
	mkFile(t, filepath.Join(home, ".claude/shell-snapshots/snapshot-zsh-gone.sh"), 10, 10*day)
	env := core.NewEnv()
	env.Home = home
	env.Protected = safety.New(home, t.TempDir(), nil, nil).Protected

	logs := expanded(env, entryByID(t, "ai-claude-code-debug-logs"))
	if len(logs) != 1 || !logs[".claude/debug/old.txt"] {
		t.Errorf("debug logs entry = %v, want only .claude/debug/old.txt", logs)
	}
	snaps := entryByID(t, "ai-claude-code-shell-snapshots")
	if snaps.OlderThan < 7*day {
		t.Errorf("shell snapshots OlderThan = %v, want >= 7 days", snaps.OlderThan)
	}
	got := expanded(env, snaps)
	if len(got) != 1 || !got[".claude/shell-snapshots/snapshot-zsh-gone.sh"] {
		t.Errorf("shell snapshots entry = %v, want only the 10-day-old snapshot", got)
	}
}
