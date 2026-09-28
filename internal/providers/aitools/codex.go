package aitools

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
)

// ---------------------------------------------------------------- Codex rollouts

var (
	reYear  = regexp.MustCompile(`^\d{4}$`)
	reMonth = regexp.MustCompile(`^\d{2}$`)
)

// codexSessions groups ~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl older than
// codexSessionAge per month. Pinned threads (state DB) are never proposed.
func (s *scanner) codexSessions() {
	root := s.home(".codex/sessions")
	if !s.usable(root) || !isDir(root) {
		return
	}
	pinned := s.codexPinnedRollouts()
	cutoff := s.now.Add(-codexSessionAge)
	for _, y := range list(root, false) {
		if !y.dir || !reYear.MatchString(y.name) {
			continue
		}
		for _, m := range list(y.path, false) {
			if s.ctx.Err() != nil {
				return
			}
			if !m.dir || !reMonth.MatchString(m.name) {
				continue
			}
			it := s.newItem("codex-old-sessions", core.CatAI, "", core.RiskCaution)
			it.ID = itemID(it.Kind, m.path)
			it.ProcessGuard = procCodex
			skipped := 0
			for _, d := range list(m.path, false) {
				if !d.dir {
					continue
				}
				for _, f := range list(d.path, false) {
					if f.dir || !strings.HasSuffix(f.name, ".jsonl") || !f.mtime.Before(cutoff) {
						continue
					}
					if pinned[f.path] {
						skipped++
						continue
					}
					it.Paths = append(it.Paths, f.path)
					it.LastUsed = maxTime(it.LastUsed, f.mtime)
				}
			}
			if len(it.Paths) == 0 {
				continue
			}
			it.Location = m.path + "/…"
			it.Name = "Codex sessions > 30d · " + y.name + "-" + m.name + " (" + plural(len(it.Paths), "rollout", "rollouts") + ")"
			it.Meta = map[string]string{"month": y.name + "-" + m.name, "rollouts": strconv.Itoa(len(it.Paths))}
			if skipped > 0 {
				it.Meta["pinned_kept"] = strconv.Itoa(skipped)
			}
			it.Note = "Codex thread rollouts untouched for 30+ days (single files can reach hundreds of MB with screenshots); deleting one makes that thread unresumable. Pinned threads are kept."
			s.publish(it, pubOpts{placeholder: true})
		}
	}
}

// codexArchived groups ~/.codex/archived_sessions/rollout-*.jsonl per month of
// last write.
func (s *scanner) codexArchived() {
	root := s.home(".codex/archived_sessions")
	if !s.usable(root) {
		return
	}
	pinned := s.codexPinnedRollouts()
	groups := map[string]*core.Item{}
	for _, f := range list(root, false) {
		if f.dir || !strings.HasSuffix(f.name, ".jsonl") || s.now.Sub(f.mtime) < day || pinned[f.path] {
			continue
		}
		month := f.mtime.Format("2006-01")
		it := groups[month]
		if it == nil {
			it = s.newItem("codex-archived-sessions", core.CatAI, "", core.RiskCaution)
			it.ID = itemID(it.Kind, root+"/"+month)
			it.ProcessGuard = procCodex
			it.Location = root + "/…"
			groups[month] = it
		}
		it.Paths = append(it.Paths, f.path)
		it.LastUsed = maxTime(it.LastUsed, f.mtime)
	}
	for _, month := range sortedKeys(groups) {
		it := groups[month]
		it.Name = "Codex archived sessions · " + month + " (" + plural(len(it.Paths), "rollout", "rollouts") + ")"
		it.Meta = map[string]string{"month": month, "rollouts": strconv.Itoa(len(it.Paths))}
		it.Note = "Rollouts of threads you archived in Codex (still listed under Archived); deleting them loses those conversations for good. Codex tolerates missing rollouts."
		s.publish(it, pubOpts{placeholder: true})
	}
}

// codexPinnedRollouts reads the rollout paths of pinned threads from the
// newest ~/.codex/state_*.sqlite (read-only). Empty when sqlite3 is absent.
func (s *scanner) codexPinnedRollouts() map[string]bool {
	s.pinnedOnce.Do(func() { s.pinned = s.readCodexPinned() })
	return s.pinned
}

func (s *scanner) readCodexPinned() map[string]bool {
	out := map[string]bool{}
	dbs, _ := filepath.Glob(s.home(".codex/state_*.sqlite"))
	if len(dbs) == 0 || !s.env.Has("sqlite3") {
		return out
	}
	sort.Slice(dbs, func(i, j int) bool { return dbNumber(dbs[i]) > dbNumber(dbs[j]) })
	res, ok := s.sqliteQuery(dbs[0], "select rollout_path from threads where is_pinned=1;")
	if !ok {
		return out
	}
	for _, l := range res {
		if l = strings.TrimSpace(l); filepath.IsAbs(l) {
			out[filepath.Clean(l)] = true
		}
	}
	return out
}

func dbNumber(p string) int {
	base := strings.TrimSuffix(filepath.Base(p), ".sqlite")
	n, _ := strconv.Atoi(base[strings.LastIndex(base, "_")+1:])
	return n
}

// sqliteQuery runs a read-only query (mode=ro, then immutable=1 which some
// Codex/Conductor DBs require) and returns the output lines.
func (s *scanner) sqliteQuery(db, query string) ([]string, bool) {
	u := (&url.URL{Scheme: "file", Path: db}).String()
	for _, mode := range []string{"?mode=ro", "?immutable=1"} {
		ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
		out, err := s.env.Output(ctx, "", "sqlite3", "-readonly", u+mode, query)
		cancel()
		if err == nil {
			return strings.Split(strings.TrimSpace(string(out)), "\n"), true
		}
	}
	return nil, false
}

// ---------------------------------------------------------------- Codex databases

// codexDatabases proposes the logs DBs (debug logs only, recreated by Codex)
// and the stale logs copies left in ~/.codex/sqlite. Other DBs are live state
// and are only reported by the catalog.
func (s *scanner) codexDatabases() {
	dbs, _ := filepath.Glob(s.home(".codex/logs_*.sqlite"))
	for _, db := range dbs {
		if s.ctx.Err() != nil {
			return
		}
		fi, err := os.Lstat(db)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		it := s.newItem("codex-logs-db", core.CatAI, "Codex logs database ("+filepath.Base(db)+")", core.RiskSafe)
		it.ID = itemID(it.Kind, db)
		it.Paths = sqliteFiles(db)
		it.Location = db
		it.LastUsed = fi.ModTime()
		it.ProcessGuard = procCodex
		it.Note = "Codex tracing/feedback logs only (never pruned nor vacuumed); Codex recreates an empty DB at next start. Its -wal/-shm files are removed together, so quit Codex and ChatGPT first."
		it.Meta = map[string]string{}
		if s.env.Has("sqlite3") {
			if res, ok := s.sqliteQuery(db, "pragma page_count; pragma freelist_count;"); ok && len(res) == 2 {
				pages, _ := strconv.ParseFloat(strings.TrimSpace(res[0]), 64)
				free, _ := strconv.ParseFloat(strings.TrimSpace(res[1]), 64)
				if pages > 0 {
					it.Meta["free_pages_pct"] = strconv.Itoa(int(free / pages * 100))
				}
			}
		}
		s.publish(it, pubOpts{placeholder: true})
	}

	// ~/.codex/sqlite/logs_*.sqlite: an old sqlite_home copy, stale when the
	// same DB in ~/.codex is newer.
	stale, _ := filepath.Glob(s.home(".codex/sqlite/logs_*.sqlite"))
	for _, db := range stale {
		fi, err := os.Lstat(db)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		live, err := os.Lstat(s.home(".codex/" + filepath.Base(db)))
		if err != nil || !live.ModTime().After(fi.ModTime()) {
			continue
		}
		it := s.newItem("codex-stale-logs-db", core.CatAI, "Codex stale logs DB copy (sqlite/"+filepath.Base(db)+")", core.RiskSafe)
		it.ID = itemID(it.Kind, db)
		it.Paths = sqliteFiles(db)
		it.Location = db
		it.LastUsed = fi.ModTime()
		it.ProcessGuard = procCodex
		it.Recommended = true
		it.Note = "Old copy of the Codex logs DB left in ~/.codex/sqlite; the live one is ~/.codex/" + filepath.Base(db) + " (newer)."
		s.publish(it, pubOpts{placeholder: true})
	}
}

// sqliteFiles returns db and its existing -wal / -shm / -journal companions.
func sqliteFiles(db string) []string {
	out := []string{db}
	for _, suf := range []string{"-wal", "-shm", "-journal"} {
		if fileExists(db + suf) {
			out = append(out, db+suf)
		}
	}
	return out
}

// ---------------------------------------------------------------- Codex visualizations

// codexVisualizations groups ~/.codex/visualizations/YYYY/MM/DD dirs older than
// visualizationAge per month.
func (s *scanner) codexVisualizations() {
	root := s.home(".codex/visualizations")
	if !s.usable(root) {
		return
	}
	for _, y := range list(root, false) {
		if !y.dir || !reYear.MatchString(y.name) {
			continue
		}
		for _, m := range list(y.path, false) {
			if !m.dir || !reMonth.MatchString(m.name) {
				continue
			}
			it := s.newItem("codex-visualizations", core.CatAI, "", core.RiskCaution)
			it.ID = itemID(it.Kind, m.path)
			for _, d := range list(m.path, false) {
				t := newestShallow(d.path)
				if s.now.Sub(t) < visualizationAge {
					continue
				}
				it.Paths = append(it.Paths, d.path)
				it.LastUsed = maxTime(it.LastUsed, t)
			}
			if len(it.Paths) == 0 {
				continue
			}
			it.Location = m.path + "/…"
			it.Name = "Codex visualizations · " + y.name + "-" + m.name + " (" + plural(len(it.Paths), "day", "days") + ")"
			it.Note = "Screenshots and screen recordings produced by Codex agents, older than 30 days; they are not regenerated."
			s.publish(it, pubOpts{placeholder: true, newest: true})
		}
	}
}

// ---------------------------------------------------------------- Multica

// multicaTaskHomes proposes the per-task CODEX_HOME copies Multica leaves in
// ~/multica_workspaces_*/<workspace>/<task>/codex-home once the task is
// completed. Task workdirs (repository checkouts) are never touched.
func (s *scanner) multicaTaskHomes() {
	metas, _ := filepath.Glob(s.home("multica_workspaces_*/*/*/.gc_meta.json"))
	groups := map[string]*core.Item{}
	for _, meta := range metas {
		task := filepath.Dir(meta)
		home := filepath.Join(task, "codex-home")
		if fi, err := os.Lstat(home); err != nil || !fi.IsDir() {
			continue
		}
		data, err := os.ReadFile(meta)
		if err != nil {
			continue
		}
		var m struct {
			CompletedAt string `json:"completed_at"`
		}
		if json.Unmarshal(data, &m) != nil || m.CompletedAt == "" {
			continue // not finished
		}
		done, err := time.Parse(time.RFC3339Nano, m.CompletedAt)
		if err != nil || s.now.Sub(done) < multicaTaskMinAge {
			continue
		}
		ws := filepath.Dir(task)
		it := groups[ws]
		if it == nil {
			it = s.newItem("multica-task-codex-homes", core.CatAI, "", core.RiskModerate)
			it.ID = itemID(it.Kind, ws)
			it.ProcessGuard = procMultica
			it.Recommended = true
			it.Location = ws + "/…"
			groups[ws] = it
		}
		it.Paths = append(it.Paths, home)
		it.LastUsed = maxTime(it.LastUsed, done)
	}
	for _, ws := range sortedKeys(groups) {
		it := groups[ws]
		sort.Strings(it.Paths)
		it.Name = "Multica finished-task Codex homes (" + plural(len(it.Paths), "task", "tasks") + ")"
		it.Note = "Full CODEX_HOME copy (skills, caches, logs DB) Multica keeps for every task completed 7+ days ago; not reused. Symlinks inside (auth.json, sessions) are removed, never followed. Task workdirs are left alone."
		it.Meta = map[string]string{"tasks": strconv.Itoa(len(it.Paths))}
		s.publish(it, pubOpts{placeholder: true})
	}
}
