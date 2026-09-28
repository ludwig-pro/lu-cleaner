package aitools

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/providers/catalog"
)

func TestCodexSessionsArchivedAndDBs(t *testing.T) {
	f := newFixture(t)
	f.runner.bins["sqlite3"] = true

	a := f.file(".codex/sessions/2026/05/03/rollout-a.jsonl", 4000, 120*day)
	b := f.file(".codex/sessions/2026/05/04/rollout-b.jsonl", 4000, 118*day) // pinned
	c := f.file(".codex/sessions/2026/09/27/rollout-c.jsonl", 4000, day)     // recent
	f.ageTree(".codex/sessions/2026/05", 118*day)
	f.age(".codex/sessions/2026/05/03/rollout-a.jsonl", 120*day)
	f.text(".codex/state_5.sqlite", "db", 0)
	f.runner.out["sqlite3 -readonly file://"+f.path(".codex/state_5.sqlite")] = b + "\n"

	arch1 := f.file(".codex/archived_sessions/rollout-x.jsonl", 3000, 40*day) // August
	arch2 := f.file(".codex/archived_sessions/rollout-y.jsonl", 3000, 10*day) // September
	fresh := f.file(".codex/archived_sessions/rollout-z.jsonl", 3000, 0)      // being written

	logs := f.file(".codex/logs_2.sqlite", 200_000, 0)
	logsWal := f.file(".codex/logs_2.sqlite-wal", 10_000, 0)
	logsShm := f.file(".codex/logs_2.sqlite-shm", 10_000, 0)
	f.runner.out["sqlite3 -readonly file://"+logs] = "100\n93\n"
	staleCopy := f.file(".codex/sqlite/logs_2.sqlite", 50_000, 100*day)
	liveDev := f.file(".codex/sqlite/codex-dev.db", 5000, 0)

	vizOld := f.file(".codex/visualizations/2026/07/11/shot.png", 7000, 70*day)
	f.ageTree(".codex/visualizations/2026/07", 70*day)
	vizNew := f.file(".codex/visualizations/2026/09/20/shot.png", 7000, 8*day)

	auth := f.text(".codex/auth.json", "{}", 0)
	cfg := f.text(".codex/config.toml", "", 0)
	mem := f.text(".codex/memories/m.md", "x", 0)
	f.running = []string{"ChatGPT"}

	r := f.scan()
	f.checkInvariants(r, auth, cfg, mem, liveDev, c, fresh, vizNew)

	s := r.one(t, "codex-old-sessions")
	if !hasTarget(s, a) || hasTarget(s, b) || len(s.Targets()) != 1 {
		t.Errorf("old sessions = %v", s.Targets())
	}
	if s.Meta["pinned_kept"] != "1" || s.Risk != core.RiskCaution || !strings.Contains(s.Name, "2026-05") {
		t.Errorf("old sessions meta/risk/name = %v %v %q", s.Meta, s.Risk, s.Name)
	}
	if s.Warn == "" || len(s.ProcessGuard) == 0 {
		t.Errorf("Codex items need a ProcessGuard and a running warning (ChatGPT embeds Codex)")
	}

	arch := r.byKind("codex-archived-sessions")
	if len(arch) != 2 {
		t.Fatalf("archived groups = %v", names(arch))
	}
	for _, it := range arch {
		if hasTarget(it, fresh) {
			t.Errorf("a rollout written in the last day must be skipped")
		}
		if it.Risk != core.RiskCaution || it.Recommended {
			t.Errorf("archived sessions must be caution")
		}
	}
	if !hasTarget(arch[0], arch1) && !hasTarget(arch[1], arch1) || !hasTarget(arch[0], arch2) && !hasTarget(arch[1], arch2) {
		t.Errorf("archived targets = %v", names(arch))
	}

	db := r.one(t, "codex-logs-db")
	if len(db.Paths) != 3 || !hasTarget(db, logs) || !hasTarget(db, logsWal) || !hasTarget(db, logsShm) {
		t.Errorf("logs DB must go with its -wal/-shm: %v", db.Targets())
	}
	if db.Risk != core.RiskSafe || db.Recommended || db.Meta["free_pages_pct"] != "93" {
		t.Errorf("logs DB = %v rec=%v meta=%v", db.Risk, db.Recommended, db.Meta)
	}
	if core.Recommend(db, f.now, 30*day) {
		t.Errorf("a live DB must not be smart-selected while Codex runs")
	}
	st := r.one(t, "codex-stale-logs-db")
	if !hasTarget(st, staleCopy) || !st.Recommended {
		t.Errorf("stale copy = %+v", st)
	}

	viz := r.one(t, "codex-visualizations")
	if !hasTarget(viz, f.path(".codex/visualizations/2026/07/11")) || hasTarget(viz, vizOld) {
		t.Errorf("visualizations = %v", viz.Targets())
	}
}

func TestCodexStaleCopyNewerThanLiveIsKept(t *testing.T) {
	f := newFixture(t)
	f.file(".codex/logs_2.sqlite", 1000, 50*day)
	f.file(".codex/sqlite/logs_2.sqlite", 1000, 10*day)
	r := f.scan()
	if len(r.byKind("codex-stale-logs-db")) != 0 {
		t.Errorf("a sqlite/ copy newer than the live DB must not be proposed")
	}
}

func TestMulticaTaskHomes(t *testing.T) {
	f := newFixture(t)
	ws := "multica_workspaces_example/ws-1"
	f.text(ws+"/t1/.gc_meta.json", `{"completed_at":"2026-08-01T10:00:00.5Z"}`, 0)
	f.file(ws+"/t1/codex-home/cache/x.bin", 9000, 0)
	f.link(ws+"/t1/codex-home/auth.json", f.path(".codex/auth.json"))
	f.file(ws+"/t1/workdir/src.ts", 100, 0)
	f.text(ws+"/t2/.gc_meta.json", `{"completed_at":"2026-09-27T10:00:00Z"}`, 0) // too recent
	f.file(ws+"/t2/codex-home/x", 100, 0)
	f.text(ws+"/t3/.gc_meta.json", `{"kind":"issue"}`, 0) // not finished
	f.file(ws+"/t3/codex-home/x", 100, 0)
	auth := f.text(".codex/auth.json", "{}", 0)

	r := f.scan()
	f.checkInvariants(r, auth, f.path(ws+"/t1/workdir"))
	it := r.one(t, "multica-task-codex-homes")
	if len(it.Paths) != 1 || it.Paths[0] != f.path(ws+"/t1/codex-home") {
		t.Errorf("multica targets = %v", it.Paths)
	}
	if !it.Recommended || it.Risk != core.RiskModerate {
		t.Errorf("multica = %v %v", it.Risk, it.Recommended)
	}
}

// TestCatalogAIEntries runs the static "ai-" catalog entries on a fixture:
// backups and temp files are found, credentials never are.
func TestCatalogAIEntries(t *testing.T) {
	f := newFixture(t)
	bak := f.file(".codex/logs_2.sqlite.codex-repair-1780986137.0.bak", 5000, 60*day)
	tmp := f.file(".codex/..codex-global-state.json.tmp-1-abc", 500, 3*day)
	freshTmp := f.file(".codex/..codex-global-state.json.tmp-2-def", 500, 0)
	state := f.text(".codex/.codex-global-state.json", "{}", 3*day)
	auth := f.text(".codex/auth.json", "{}", 60*day)
	dbg := f.file(".claude/debug/abc.txt", 800, 3*day)
	creds := f.text(".claude/.credentials.json", "{}", 60*day)
	vm := f.file("Library/Application Support/Claude/vm_bundles/claudevm.bundle/rootfs.img", 9000, 5*day)
	sess := f.file("Library/Application Support/Claude/vm_bundles/claudevm.bundle/sessiondata.img", 9000, 5*day)
	chats := f.file("Library/Application Support/Cursor/User/globalStorage/state.vscdb", 9000, 5*day)
	cursorSettings := f.text("Library/Application Support/Cursor/User/settings.json", "{}", 60*day)

	env := f.env
	items := map[string]*core.Item{}
	var mu sync.Mutex
	if err := catalog.New().Scan(context.Background(), env, func(it *core.Item) {
		if strings.HasPrefix(it.Kind, "ai-") {
			mu.Lock()
			items[it.ID] = it
			mu.Unlock()
		}
	}); err != nil {
		t.Fatal(err)
	}
	targets := map[string]*core.Item{}
	for _, it := range items {
		for _, p := range it.Targets() {
			targets[p] = it
		}
		if it.Path == "" && len(it.Paths) == 0 && it.Location != "" && it.Method != core.MethodCommand {
			targets[it.Location] = it
		}
	}
	for _, want := range []string{bak, tmp, dbg, vm} {
		if targets[want] == nil {
			t.Errorf("catalog should propose %s", want)
		}
	}
	for _, never := range []string{state, auth, creds, cursorSettings, freshTmp} {
		if it := targets[never]; it != nil {
			t.Errorf("catalog proposes %s (%s)", never, it.Kind)
		}
	}
	for _, reportOnly := range []string{sess, chats} {
		if it := targets[reportOnly]; it == nil || it.CanClean() {
			t.Errorf("%s must be report-only: %+v", reportOnly, it)
		}
	}
	if it := targets[vm]; it != nil && hasTarget(it, sess) {
		t.Errorf("the VM session disk must not be deleted with the image")
	}
}
