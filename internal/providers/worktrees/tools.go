package worktrees

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
	"github.com/ludwig-pro/lu-cleaner/internal/sysx"
)

// Tools that create worktrees (Item.Kind is "<tool>-worktree").
const (
	toolCodex         = "codex"
	toolCursor        = "cursor"
	toolConductor     = "conductor"
	toolClaude        = "claude"
	toolClaudeDesktop = "claude-desktop"
	toolSuperset      = "superset"
	toolClaudeSquad   = "claude-squad"
	toolVibeKanban    = "vibe-kanban"
	toolMultica       = "multica"
	toolManual        = "manual"
)

// toolRoots maps home-relative worktree homes to their tool.
var toolRoots = []struct{ rel, tool string }{
	{".codex/worktrees", toolCodex},
	{".cursor/worktrees", toolCursor},
	{"conductor/workspaces", toolConductor},
	{".superset/worktrees", toolSuperset},
	{"Library/Application Support/Claude/worktrees", toolClaudeDesktop},
	{".claude-squad/worktrees", toolClaudeSquad},
	{".claude/worktrees", toolClaude},
	{".claude-worktrees", toolClaude},
}

// claudeDesktopName matches the "<slug>-<6 hex>" names of Claude desktop worktrees.
var claudeDesktopName = regexp.MustCompile(`-[0-9a-f]{6}$`)

// classify tells which tool created w, from its location and the tools' own state.
func (s *scan) classify(w *worktree) string {
	for _, r := range toolRoots {
		if fsx.Within(w.path, filepath.Join(s.home, r.rel)) {
			return r.tool
		}
	}
	if strings.Contains(w.path, "/vibe-kanban/worktrees/") {
		return toolVibeKanban
	}
	if w.gitdir != "" && fsx.Exists(filepath.Join(w.gitdir, "codex-thread.json")) {
		return toolCodex
	}
	if strings.Contains(w.path, "/.claude/worktrees/") {
		// Claude desktop: claude/<slug>-<6hex> branches, tracked in its own state.
		// Claude Code CLI: worktree-<name> branches (claude -w), agent-<hex> dirs.
		name := filepath.Base(w.path)
		switch {
		case s.tools != nil && s.tools.claudeWT[pathKey(w.path)] != nil, strings.HasPrefix(w.branch, "claude/"):
			return toolClaudeDesktop
		case strings.HasPrefix(w.branch, "worktree-"), strings.HasPrefix(name, "agent-"):
			return toolClaude
		case w.branch == "" && claudeDesktopName.MatchString(name):
			return toolClaudeDesktop
		}
		return toolClaude
	}
	if strings.HasPrefix(w.main, filepath.Join(s.home, "multica_workspaces")) {
		return toolMultica
	}
	return toolManual
}

// ---------------------------------------------------------------- tool state

// toolState is what the AI tools and the OS know about worktrees: agent
// threads/sessions using them, editor windows, processes' cwd.
type toolState struct {
	mu          sync.Mutex
	codex       map[string]*codexCwd    // thread cwd -> threads
	codexQueued map[string]bool         // cwds queued for archival by the Codex app
	claudeWT    map[string]*claudeWT    // worktree path -> Claude desktop state
	conductor   map[string]*conductorWS // workspace path -> Conductor state
	editors     map[string][]string     // open folder -> editor names
	procs       []proc                  // processes' current directories
}

type codexCwd struct {
	active, archived int
	last             time.Time
}

type claudeWT struct {
	known            bool // listed in git-worktrees.json
	leased           bool // currently leased by a session
	active, archived int
	last             time.Time
}

type conductorWS struct {
	state string
}

type proc struct {
	pid int
	cmd string
	cwd string
}

const toolTimeout = 5 * time.Second

// newToolState returns an empty tool state. Its maps are keyed by pathKey
// (tools store paths in their own case and Unicode normalization).
func newToolState() *toolState {
	return &toolState{
		codex:       map[string]*codexCwd{},
		codexQueued: map[string]bool{},
		claudeWT:    map[string]*claudeWT{},
		conductor:   map[string]*conductorWS{},
		editors:     map[string][]string{},
	}
}

// loadToolState reads every source concurrently. Absent tools are skipped
// silently; databases are opened read-only.
func loadToolState(ctx context.Context, env *core.Env, home string, wts []*worktree) *toolState {
	ts := newToolState()
	if len(wts) == 0 {
		return ts
	}
	var wg sync.WaitGroup
	for _, f := range []func(context.Context, *core.Env, string){
		ts.loadCodex, ts.loadClaudeDesktop, ts.loadConductor, ts.loadEditors, ts.loadProcs,
	} {
		wg.Add(1)
		go func(f func(context.Context, *core.Env, string)) {
			defer wg.Done()
			defer func() { _ = recover() }() // tool state is best effort
			f(ctx, env, home)
		}(f)
	}
	wg.Wait()
	return ts
}

// binary returns the name to run for a system tool, or "" when absent.
func binary(env *core.Env, name, fallback string) string {
	if env.Has(name) {
		return name
	}
	if fallback != "" && fsx.Exists(fallback) {
		return fallback
	}
	return ""
}

// sqliteJSON runs a read-only query and decodes its JSON rows into out.
//
// The apps keep their databases in WAL mode: rows not yet checkpointed (a
// thread just started or unarchived) live only in the -wal file, which
// immutable=1 ignores. So a plain read-only open comes first; immutable=1 is
// the fallback when it fails (no -shm and a read-only folder, locked file...).
func sqliteJSON(ctx context.Context, env *core.Env, db, query string, out any) bool {
	bin := binary(env, "sqlite3", "/usr/bin/sqlite3")
	if bin == "" || !fsx.Exists(db) {
		return false
	}
	for _, mode := range []string{"?mode=ro", "?mode=ro&immutable=1"} {
		cctx, cancel := context.WithTimeout(ctx, toolTimeout)
		b, err := env.Output(cctx, "", bin, "-readonly", "-json", "-cmd", ".timeout 1000", "file:"+escapeURIPath(db)+mode, query)
		cancel()
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(b)) == "" {
			return true // no rows
		}
		return json.Unmarshal(b, out) == nil
	}
	return false
}

func escapeURIPath(p string) string {
	r := strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23")
	return r.Replace(p)
}

// loadCodex reads the Codex thread database (cwd of every thread) and the
// Codex app archival queue.
func (ts *toolState) loadCodex(ctx context.Context, env *core.Env, home string) {
	dbs, _ := filepath.Glob(filepath.Join(home, ".codex", "state_*.sqlite"))
	if len(dbs) > 0 {
		sort.Slice(dbs, func(i, j int) bool { return stateVersion(dbs[i]) > stateVersion(dbs[j]) })
		var rows []struct {
			Cwd      string `json:"cwd"`
			Archived int    `json:"archived"`
			N        int    `json:"n"`
			U        int64  `json:"u"`
		}
		if sqliteJSON(ctx, env, dbs[0],
			"select cwd, archived, count(*) as n, max(updated_at) as u from threads group by cwd, archived", &rows) {
			ts.mu.Lock()
			for _, r := range rows {
				if r.Cwd == "" {
					continue
				}
				cwd := pathKey(realPath(r.Cwd))
				c := ts.codex[cwd]
				if c == nil {
					c = &codexCwd{}
					ts.codex[cwd] = c
				}
				if r.Archived != 0 {
					c.archived += r.N
				} else {
					c.active += r.N
				}
				if t := unixAuto(r.U); t.After(c.last) {
					c.last = t
				}
			}
			ts.mu.Unlock()
		}
	}
	var gs struct {
		Archives []struct {
			Cwd   string `json:"cwd"`
			Phase string `json:"phase"`
		} `json:"electron-managed-worktree-archives"`
	}
	if b, err := os.ReadFile(filepath.Join(home, ".codex", ".codex-global-state.json")); err == nil && json.Unmarshal(b, &gs) == nil {
		ts.mu.Lock()
		for _, a := range gs.Archives {
			if a.Cwd != "" {
				ts.codexQueued[pathKey(realPath(a.Cwd))] = true
			}
		}
		ts.mu.Unlock()
	}
}

func stateVersion(p string) int {
	b := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(p), "state_"), ".sqlite")
	n, _ := strconv.Atoi(b)
	return n
}

// unixAuto converts seconds or milliseconds since the epoch.
func unixAuto(v int64) time.Time {
	switch {
	case v <= 0:
		return time.Time{}
	case v > 1e12:
		return time.UnixMilli(v)
	}
	return time.Unix(v, 0)
}

// loadClaudeDesktop reads the Claude desktop worktree pool and its Code sessions.
func (ts *toolState) loadClaudeDesktop(ctx context.Context, env *core.Env, home string) {
	base := filepath.Join(home, "Library", "Application Support", "Claude")
	var pool struct {
		Worktrees map[string]struct {
			Path     string          `json:"path"`
			LeasedBy json.RawMessage `json:"leasedBy"`
		} `json:"worktrees"`
	}
	get := func(p string) *claudeWT {
		p = pathKey(realPath(p))
		c := ts.claudeWT[p]
		if c == nil {
			c = &claudeWT{}
			ts.claudeWT[p] = c
		}
		return c
	}
	if b, err := os.ReadFile(filepath.Join(base, "git-worktrees.json")); err == nil && json.Unmarshal(b, &pool) == nil {
		ts.mu.Lock()
		for _, w := range pool.Worktrees {
			if w.Path == "" {
				continue
			}
			c := get(w.Path)
			c.known = true
			if l := strings.TrimSpace(string(w.LeasedBy)); l != "" && l != "null" {
				c.leased = true
			}
		}
		ts.mu.Unlock()
	}
	accounts, _ := filepath.Glob(filepath.Join(base, "claude-code-sessions", "*", "*"))
	for _, dir := range accounts {
		if ctx.Err() != nil {
			return
		}
		archived := map[string]bool{}
		var idx struct {
			Archived []string `json:"archived"`
		}
		if b, err := os.ReadFile(filepath.Join(dir, "archived-sessions.idx")); err == nil && json.Unmarshal(b, &idx) == nil {
			for _, id := range idx.Archived {
				archived[id] = true
			}
		}
		files, _ := filepath.Glob(filepath.Join(dir, "local_*.json"))
		if len(files) > 2000 {
			files = files[:2000]
		}
		for _, f := range files {
			var sess struct {
				SessionID      string `json:"sessionId"`
				WorktreePath   string `json:"worktreePath"`
				IsArchived     bool   `json:"isArchived"`
				LastActivityAt int64  `json:"lastActivityAt"`
			}
			b, err := os.ReadFile(f)
			if err != nil || json.Unmarshal(b, &sess) != nil || sess.WorktreePath == "" {
				continue
			}
			id := sess.SessionID
			if id == "" {
				id = strings.TrimSuffix(filepath.Base(f), ".json")
			}
			ts.mu.Lock()
			c := get(sess.WorktreePath)
			if sess.IsArchived || archived[id] {
				c.archived++
			} else {
				c.active++
			}
			if t := unixAuto(sess.LastActivityAt); t.After(c.last) {
				c.last = t
			}
			ts.mu.Unlock()
		}
	}
}

// loadConductor reads the Conductor workspaces table.
func (ts *toolState) loadConductor(ctx context.Context, env *core.Env, home string) {
	db := filepath.Join(home, "Library", "Application Support", "com.conductor.app", "conductor.db")
	var rows []struct {
		P string `json:"p"`
		S string `json:"s"`
	}
	if !sqliteJSON(ctx, env, db,
		"select workspace_path as p, state as s from workspaces where workspace_path is not null and workspace_path != ''", &rows) {
		return
	}
	ts.mu.Lock()
	defer ts.mu.Unlock()
	for _, r := range rows {
		ts.conductor[pathKey(realPath(r.P))] = &conductorWS{state: r.S}
	}
}

// loadEditors reads the folders of open Cursor / VS Code windows (only while
// the editor runs: the saved window state is otherwise stale).
func (ts *toolState) loadEditors(ctx context.Context, env *core.Env, home string) {
	for _, ed := range []struct{ name, dir, proc string }{
		{"Cursor", "Cursor", "/Cursor.app/Contents/MacOS/"},
		{"VS Code", "Code", "/Visual Studio Code.app/Contents/MacOS/"},
	} {
		p := filepath.Join(home, "Library", "Application Support", ed.dir, "User", "globalStorage", "storage.json")
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if len(sysx.Running(ed.proc)) == 0 {
			continue
		}
		var st struct {
			WindowsState struct {
				LastActiveWindow struct {
					Folder string `json:"folder"`
				} `json:"lastActiveWindow"`
				OpenedWindows []struct {
					Folder string `json:"folder"`
				} `json:"openedWindows"`
			} `json:"windowsState"`
		}
		if json.Unmarshal(b, &st) != nil {
			continue
		}
		folders := []string{st.WindowsState.LastActiveWindow.Folder}
		for _, w := range st.WindowsState.OpenedWindows {
			folders = append(folders, w.Folder)
		}
		ts.mu.Lock()
		for _, f := range folders {
			u, err := url.Parse(f)
			if err != nil || u.Scheme != "file" || u.Path == "" {
				continue
			}
			fp := pathKey(realPath(u.Path))
			ts.editors[fp] = appendUnique(ts.editors[fp], ed.name)
		}
		ts.mu.Unlock()
	}
}

// loadProcs lists the current directory of every process (lsof -d cwd: one
// cheap call, instead of lsof +D per worktree).
func (ts *toolState) loadProcs(ctx context.Context, env *core.Env, _ string) {
	bin := binary(env, "lsof", "/usr/sbin/lsof")
	if bin == "" {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, toolTimeout)
	defer cancel()
	out, _ := env.Output(cctx, "", bin, "-n", "-P", "-w", "-d", "cwd", "-Fpcn") // exit 1 with partial output is common
	self := os.Getpid()
	var list []proc
	cur := proc{}
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		switch line[0] {
		case 'p':
			cur = proc{}
			cur.pid, _ = strconv.Atoi(line[1:])
		case 'c':
			cur.cmd = line[1:]
		case 'n':
			if cur.pid != 0 && cur.pid != self && strings.HasPrefix(line[1:], "/") {
				cur.cwd = filepath.Clean(line[1:])
				list = append(list, cur)
			}
		}
	}
	ts.mu.Lock()
	ts.procs = list
	ts.mu.Unlock()
}

// apply fills w.inUse / editors / session / toolMeta from the tool state.
func (ts *toolState) apply(w *worktree) {
	if ts == nil {
		return
	}
	ts.mu.Lock()
	defer ts.mu.Unlock()
	w.toolMeta = map[string]string{}
	key := pathKey(w.path)
	for _, p := range ts.procs {
		if fsx.Within(pathKey(p.cwd), key) {
			w.inUse = appendUnique(w.inUse, p.cmd)
		}
	}
	for f, apps := range ts.editors {
		if fsx.Within(f, key) {
			for _, a := range apps {
				w.editors = appendUnique(w.editors, a)
			}
		}
	}
	var active, archived int
	for cwd, c := range ts.codex {
		if fsx.Within(cwd, key) {
			active += c.active
			archived += c.archived
		}
	}
	if active+archived > 0 {
		w.toolMeta["codex_threads"] = fmt.Sprintf("%d active, %d archived", active, archived)
		if active > 0 {
			w.session = plural(active, "active Codex thread", "active Codex threads")
		}
	}
	for cwd := range ts.codexQueued {
		if fsx.Within(cwd, key) {
			w.toolMeta["codex_archive"] = "queued"
			w.toolNotes = append(w.toolNotes, "the Codex app has queued it for archival (it snapshots and removes it itself)")
			break
		}
	}
	if c := ts.claudeWT[key]; c != nil {
		switch {
		case c.leased || c.active > 0:
			w.session = "active Claude desktop session"
			w.toolMeta["claude_session"] = "active"
		case c.archived > 0:
			w.toolMeta["claude_session"] = "archived"
			w.toolNotes = append(w.toolNotes, "its Claude desktop session is archived")
		case c.known:
			w.toolMeta["claude_session"] = "pooled"
		}
	}
	if c := ts.conductor[key]; c != nil {
		w.toolMeta["conductor"] = c.state
		if c.state == "archived" {
			w.toolNotes = append(w.toolNotes, "Conductor already archived this workspace")
		} else if c.state != "" {
			w.session = "active Conductor workspace — archive it in Conductor instead"
		}
	}
}

// lastActivity returns the newest agent activity recorded inside path.
func (ts *toolState) lastActivity(path string) time.Time {
	if ts == nil {
		return time.Time{}
	}
	ts.mu.Lock()
	defer ts.mu.Unlock()
	var t time.Time
	key := pathKey(path)
	for cwd, c := range ts.codex {
		if fsx.Within(cwd, key) && c.last.After(t) {
			t = c.last
		}
	}
	if c := ts.claudeWT[key]; c != nil && c.last.After(t) {
		t = c.last
	}
	return t
}

func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
