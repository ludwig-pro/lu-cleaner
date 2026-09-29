package clean

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
)

// History records what was really freed: the reclaimable size of a deletion,
// 0 for a Trash move and a skip, always written (leftover C2).
func TestHistoryRecordsFreed(t *testing.T) {
	f := newFixture(t)
	shared := item(f.mk(t, ".cache/shared", "a"), core.MethodDelete)
	shared.Size = 1000
	shared.SetReclaim(400)
	full := item(f.mk(t, ".cache/full", "a"), core.MethodDelete)
	full.Size = 300
	skipped := item(f.mk(t, "src/app/build", "a"), core.MethodDelete)
	skipped.Size = 500
	skipped.RequireSibling = []string{"build.gradle"}
	trashed := item(f.mk(t, ".cache/totrash", "a"), core.MethodDelete)
	trashed.Size = 700

	o := f.opts()
	o.NoHistory = false
	Run(context.Background(), []*core.Item{shared, full, skipped}, o, nil)
	o.Trash = true
	Run(context.Background(), []*core.Item{trashed}, o, nil)

	hist, err := ReadHistory()
	if err != nil || len(hist) != 4 {
		t.Fatalf("history: %v %+v", err, hist)
	}
	want := map[string]struct {
		status, method string
		size, freed    int64
	}{
		shared.Path:  {"done", "delete", 1000, 400},
		full.Path:    {"done", "delete", 300, 300},
		skipped.Path: {"skipped", "delete", 500, 0},
		trashed.Path: {"done", "trash", 700, 0},
	}
	var total int64
	for _, e := range hist {
		w, ok := want[e.Path]
		if !ok || e.Status != w.status || e.Method != w.method || e.Size != w.size || e.Freed != w.freed {
			t.Errorf("entry %s: %+v, want %+v", e.Path, e, w)
		}
		total += e.Freed
	}
	if total != 700 {
		t.Errorf("total freed = %d, want 700 (not the full sizes, Trash move excluded)", total)
	}
	raw, _ := os.ReadFile(HistoryPath())
	if n := strings.Count(string(raw), `"freed":`); n != 4 {
		t.Errorf("freed written on %d of 4 lines (must be written even when 0):\n%s", n, raw)
	}
}

// A failed deletion records what it freed before failing.
func TestHistoryEntryFreedOfFailure(t *testing.T) {
	it := item("/x", core.MethodDelete)
	it.Size = 100
	for _, c := range []struct {
		r    Result
		want int64
	}{
		{Result{Item: it, Status: StatusFailed, Method: core.MethodDelete, Freed: 60}, 60},
		{Result{Item: it, Status: StatusFailed, Method: core.MethodDelete}, 0},
		{Result{Item: it, Status: StatusDryRun, Method: core.MethodDelete, Freed: 100}, 0},
		{Result{Item: it, Status: StatusDone, Method: core.MethodTrash, Trashed: 100}, 0},
		{Result{Item: it, Status: StatusDone, Method: core.MethodCommand, Freed: 100}, 100},
	} {
		if got := historyEntry(time.Now(), c.r).Freed; got != c.want {
			t.Errorf("%s %s freed %d: got %d, want %d", c.r.Status, c.r.Method, c.r.Freed, got, c.want)
		}
	}
}

// Lines written before Freed existed get the full size of done permanent
// removals (what was reported then), 0 otherwise; an explicit 0 is kept.
func TestReadHistoryLegacyFreed(t *testing.T) {
	newFixture(t)
	if err := os.MkdirAll(filepath.Dir(HistoryPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	lines := []string{
		`{"time":"2026-01-01T00:00:00Z","name":"old delete","method":"delete","status":"done","size":100}`,
		`{"time":"2026-01-01T00:00:00Z","name":"old worktree","method":"worktree","status":"done","size":50}`,
		`{"time":"2026-01-01T00:00:00Z","name":"old trash","method":"trash","status":"done","size":200}`,
		`{"time":"2026-01-01T00:00:00Z","name":"old skip","method":"delete","status":"skipped","size":300}`,
		`{"time":"2026-01-01T00:00:00Z","name":"old failure","method":"delete","status":"failed","size":400}`,
		`{"time":"2026-01-01T00:00:00Z","name":"new shared","method":"delete","status":"done","size":500,"freed":0}`,
		`{"time":"2026-01-01T00:00:00Z","name":"new","method":"delete","status":"done","size":600,"freed":250}`,
		`not json`,
	}
	if err := os.WriteFile(HistoryPath(), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hist, err := ReadHistory()
	if err != nil || len(hist) != 7 {
		t.Fatalf("history: %v %+v", err, hist)
	}
	want := map[string]int64{"old delete": 100, "old worktree": 50, "old trash": 0, "old skip": 0, "old failure": 0, "new shared": 0, "new": 250}
	for _, e := range hist {
		if e.Freed != want[e.Name] {
			t.Errorf("%s: freed %d, want %d", e.Name, e.Freed, want[e.Name])
		}
	}
}
