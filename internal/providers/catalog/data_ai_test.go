package catalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/safety"
)

func entryByID(t *testing.T, id string) Entry {
	t.Helper()
	for _, e := range Entries() {
		if e.ID == id {
			return e
		}
	}
	t.Fatalf("no catalog entry %s", id)
	return Entry{}
}

// aiFixture is a fake home with old files at the given relative paths.
func aiFixture(t *testing.T, rels ...string) *core.Env {
	t.Helper()
	home := t.TempDir()
	if r, err := filepath.EvalSymlinks(home); err == nil {
		home = r
	}
	old := time.Now().Add(-90 * 24 * time.Hour)
	for _, rel := range rels {
		p := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	g := safety.New(home, t.TempDir(), nil, nil)
	env := core.NewEnv()
	env.Home = home
	env.Protected = g.Protected
	return env
}

func expanded(env *core.Env, e Entry) map[string]bool {
	out := map[string]bool{}
	for _, m := range e.Expand(env) {
		rel, _ := filepath.Rel(env.Home, m.path)
		out[rel] = true
	}
	return out
}

// Pre-repair copies of the memories / goals DBs are the only way back after a
// bad repair: never in the safe, preselected entry.
func TestCodexRepairBackupsKeepDataDBs(t *testing.T) {
	env := aiFixture(t,
		".codex/logs_2.sqlite.codex-repair-1780986137.0.bak",
		".codex/logs_2.sqlite-wal.codex-repair-1780986137.0.bak",
		".codex/memories_1.sqlite.codex-repair-1780986137.0.bak",
		".codex/memories_1.sqlite-wal.codex-repair-1780986137.0.bak",
		".codex/goals_1.sqlite.codex-repair-1780986137.0.bak",
		".codex/state_5.sqlite.codex-repair-1780986137.0.bak",
	)
	safe := entryByID(t, "ai-codex-repair-backups")
	got := expanded(env, safe)
	if !got[".codex/logs_2.sqlite.codex-repair-1780986137.0.bak"] || !got[".codex/logs_2.sqlite-wal.codex-repair-1780986137.0.bak"] || len(got) != 2 {
		t.Errorf("safe repair backups = %v, want only the logs DB ones", got)
	}
	data := entryByID(t, "ai-codex-repair-backups-data")
	if data.Risk < core.RiskCaution || data.Recommended {
		t.Errorf("data DB repair backups must be caution, not recommended: %v %v", data.Risk, data.Recommended)
	}
	for rel := range expanded(env, data) {
		if b := filepath.Base(rel); strings.HasPrefix(b, "logs_") || strings.HasPrefix(b, "state_") {
			t.Errorf("data entry matches %s", rel)
		}
	}
}

// Cursor's state.vscdb backups are the only restore points of every chat.
func TestCursorStateBackupsNotRecommended(t *testing.T) {
	e := entryByID(t, "ai-cursor-state-db-backups")
	if e.Risk < core.RiskCaution || e.Recommended {
		t.Fatalf("ai-cursor-state-db-backups = %v rec %v, want caution and not recommended", e.Risk, e.Recommended)
	}
	it := (&Provider{}).baseItem(core.NewEnv(), e)
	it.Size, it.LastUsed = 500<<20, time.Now().Add(-90*24*time.Hour)
	it.Warn = "" // Cursor may be running on this machine: judge the entry itself
	if core.Recommend(it, time.Now(), 14*24*time.Hour) {
		t.Errorf("smart select must not pick the chat DB backups")
	}
}
