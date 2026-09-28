package aitools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"golang.org/x/sys/unix"
)

// ---------------------------------------------------------------- Claude Code transcripts

// claudeLive is the set of Claude Code sessions alive right now, from the
// ~/.claude/sessions/<pid>.json registry (the .key files are never read).
type claudeLive struct {
	ids  map[string]bool
	cwds map[string]bool
}

func (s *scanner) claudeLiveSessions() claudeLive {
	live := claudeLive{ids: map[string]bool{}, cwds: map[string]bool{}}
	for _, e := range list(s.home(".claude/sessions"), false) {
		if e.dir || !strings.HasSuffix(e.name, ".json") {
			continue
		}
		data, err := os.ReadFile(e.path)
		if err != nil || len(data) > 1<<20 {
			continue
		}
		var reg struct {
			PID       int    `json:"pid"`
			SessionID string `json:"sessionId"`
			Cwd       string `json:"cwd"`
		}
		if json.Unmarshal(data, &reg) != nil || reg.PID <= 0 || !pidAlive(reg.PID) {
			continue
		}
		if reg.SessionID != "" {
			live.ids[reg.SessionID] = true
		}
		if reg.Cwd != "" {
			live.cwds[filepath.Clean(reg.Cwd)] = true
		}
	}
	return live
}

// pidAlive uses signal 0 (no signal is delivered, it only checks existence).
func pidAlive(pid int) bool {
	err := unix.Kill(pid, 0)
	return err == nil || err == unix.EPERM
}

type claudeSession struct {
	id    string
	jsonl string // <id>.jsonl
	dir   string // <id>/ (subagents, tool-results, workflows...)
	mtime time.Time
}

func (cs *claudeSession) paths() []string {
	var out []string
	if cs.jsonl != "" {
		out = append(out, cs.jsonl)
	}
	if cs.dir != "" {
		out = append(out, cs.dir)
	}
	return out
}

// claudeProjects scans ~/.claude/projects/<encoded-cwd>/.
func (s *scanner) claudeProjects() {
	root := s.home(".claude/projects")
	entries := list(root, true)
	if len(entries) == 0 {
		return
	}
	live := s.claudeLiveSessions()
	for _, e := range entries {
		if s.ctx.Err() != nil {
			return
		}
		// The project dir itself may be "protected" because it holds memory/:
		// its targets are validated one by one when the item is emitted.
		if !e.dir || s.env.Excluded(e.path) {
			continue
		}
		s.claudeProject(e, live)
	}
}

func (s *scanner) claudeProject(proj entry, live claudeLive) {
	sessions := map[string]*claudeSession{}
	get := func(id string) *claudeSession {
		cs := sessions[id]
		if cs == nil {
			cs = &claudeSession{id: id}
			sessions[id] = cs
		}
		return cs
	}
	var others []string // files next to the sessions (sessions-index.json...)
	hasMemory := false
	for _, c := range list(proj.path, true) {
		switch {
		case c.name == "memory":
			hasMemory = true
		case c.name == ".DS_Store":
			others = append(others, c.path)
		case !c.dir && strings.HasSuffix(c.name, ".jsonl"):
			cs := get(strings.TrimSuffix(c.name, ".jsonl"))
			cs.jsonl = c.path
			cs.mtime = maxTime(cs.mtime, c.mtime)
		case c.dir:
			cs := get(c.name)
			cs.dir = c.path
			cs.mtime = maxTime(cs.mtime, newestShallow(c.path))
		default:
			others = append(others, c.path)
		}
	}
	if len(sessions) == 0 {
		return // only memory/ or nothing: never touched
	}
	ordered := make([]*claudeSession, 0, len(sessions))
	var newest time.Time
	anyLive := false
	for _, cs := range sessions {
		ordered = append(ordered, cs)
		newest = maxTime(newest, cs.mtime)
		if live.ids[cs.id] {
			anyLive = true
		}
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].mtime.After(ordered[j].mtime) })

	cwd, source := s.claudeProjectCwd(proj, ordered)
	var ex existence
	if cwd != "" {
		ex = pathExistence(cwd)
		if live.cwds[cwd] {
			anyLive = true
		}
	} else {
		var resolved string
		resolved, ex = s.claudeRes.resolve(proj.name)
		if resolved != "" {
			cwd = resolved
		}
		source = "dir-name"
	}
	label := cwd
	if label == "" {
		label = decodeNaive(proj.name, true)
	}
	label = s.env.Pretty(label)
	meta := map[string]string{"cwd_source": source}
	if cwd != "" {
		meta["cwd"] = cwd
	}

	// Orphan: the folder the sessions ran in is gone (deleted worktree...).
	if ex == existNo && !anyLive && s.now.Sub(newest) > liveGrace {
		it := s.newItem("claude-code-orphan-project", core.CatAI, "Claude Code data of deleted folder · "+label, core.RiskModerate)
		it.ID = itemID(it.Kind, proj.path)
		it.Project = cwd
		it.LastUsed = newest
		it.Recommended = source != "dir-name"
		meta["sessions"] = strconv.Itoa(len(sessions))
		it.Note = "Transcripts, subagent logs and tool outputs of sessions run in a folder that no longer exists (deleted worktree…); `claude --resume` cannot reach them unless the folder is recreated."
		if hasMemory {
			// keep the auto-memory: delete everything else
			for _, cs := range ordered {
				it.Paths = append(it.Paths, cs.paths()...)
			}
			it.Paths = append(it.Paths, others...)
			sort.Strings(it.Paths)
			it.Location = proj.path + "/…"
			meta["kept"] = "memory/"
			it.Note += " The project's auto-memory (memory/) is kept."
		} else {
			it.Path = proj.path
		}
		if source == "dir-name" {
			it.Note += " Folder decoded from the directory name (lossy): double-check before cleaning."
		}
		it.Meta = meta
		it.Recheck = s.claudeOrphanRecheck(proj.path, cwd)
		s.publish(it, pubOpts{placeholder: true})
		return
	}

	// Otherwise: sessions not touched for claudeSessionAge.
	cutoff := s.now.Add(-claudeSessionAge)
	it := s.newItem("claude-code-old-sessions", core.CatAI, "Claude Code sessions > 30d · "+label, core.RiskCaution)
	it.ID = itemID(it.Kind, proj.path)
	it.Project = cwd
	n := 0
	for _, cs := range ordered {
		if live.ids[cs.id] || !cs.mtime.Before(cutoff) {
			continue
		}
		it.Paths = append(it.Paths, cs.paths()...)
		it.LastUsed = maxTime(it.LastUsed, cs.mtime)
		n++
	}
	if n == 0 {
		return
	}
	sort.Strings(it.Paths)
	it.Location = proj.path + "/…"
	meta["sessions"] = strconv.Itoa(n)
	if ex == existUnknown && cwd != "" && missingVolume(cwd) {
		meta["cwd_status"] = "volume not mounted"
	}
	it.Meta = meta
	it.Name += " (" + plural(n, "session", "sessions") + ")"
	it.Note = "Session transcripts with their subagent logs and tool outputs, untouched for 30+ days; deleting them removes them from `claude --resume` / history (Claude Code prunes after cleanupPeriodDays)."
	s.publish(it, pubOpts{placeholder: true})
}

// claudeOrphanRecheck re-validates an orphan right before cleaning: the folder
// must still be missing and no session may have written there meanwhile.
func (s *scanner) claudeOrphanRecheck(projDir, cwd string) func(context.Context) error {
	return func(context.Context) error {
		if cwd != "" && pathExistence(cwd) != existNo {
			return fmt.Errorf("%s exists again", cwd)
		}
		for _, e := range list(projDir, true) {
			if strings.HasSuffix(e.name, ".jsonl") && time.Since(e.mtime) < liveGrace {
				return fmt.Errorf("a Claude Code session wrote %s recently", e.name)
			}
		}
		live := s.claudeLiveSessions()
		if cwd != "" && live.cwds[cwd] {
			return fmt.Errorf("a live Claude Code session runs in %s", cwd)
		}
		return nil
	}
}

// claudeProjectCwd recovers the real cwd of a project dir: the "cwd" field of
// its newest transcripts, else sessions-index.json's projectPath.
func (s *scanner) claudeProjectCwd(proj entry, sessions []*claudeSession) (string, string) {
	tried := 0
	for _, cs := range sessions {
		if cs.jsonl == "" {
			continue
		}
		if cwd := readJSONStringField(cs.jsonl, "cwd", 512<<10); filepath.IsAbs(cwd) {
			return filepath.Clean(cwd), "transcript"
		}
		if tried++; tried >= 4 {
			break
		}
	}
	data, err := os.ReadFile(filepath.Join(proj.path, "sessions-index.json"))
	if err == nil {
		var idx struct {
			OriginalPath string `json:"originalPath"`
			Entries      []struct {
				ProjectPath string `json:"projectPath"`
			} `json:"entries"`
		}
		if json.Unmarshal(data, &idx) == nil {
			if filepath.IsAbs(idx.OriginalPath) {
				return filepath.Clean(idx.OriginalPath), "sessions-index"
			}
			for _, e := range idx.Entries {
				if filepath.IsAbs(e.ProjectPath) {
					return filepath.Clean(e.ProjectPath), "sessions-index"
				}
			}
		}
	}
	return "", ""
}

// ---------------------------------------------------------------- Claude config backups

// claudeConfigBackups groups the timestamped ~/.claude.json backups, keeping
// the newest one (and the untimestamped ~/.claude.json.backup, never matched).
func (s *scanner) claudeConfigBackups() {
	var files []entry
	for _, pat := range []string{
		s.home(".claude/backups/.claude.json.backup.*"),
		s.home(".claude.json.backup.*"),
	} {
		matches, _ := filepath.Glob(pat)
		for _, m := range matches {
			fi, err := os.Lstat(m)
			if err != nil || !fi.Mode().IsRegular() {
				continue
			}
			files = append(files, entry{name: filepath.Base(m), path: m, mtime: fi.ModTime()})
		}
	}
	if len(files) < 2 {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mtime.After(files[j].mtime) })
	it := s.newItem("claude-code-config-backups", core.CatAI, "Claude Code ~/.claude.json backups", core.RiskModerate)
	it.ID = itemID(it.Kind, s.home(".claude.json.backup"))
	for _, f := range files[1:] {
		if s.now.Sub(f.mtime) < backupMinAge {
			continue
		}
		it.Paths = append(it.Paths, f.path)
		it.LastUsed = maxTime(it.LastUsed, f.mtime)
	}
	if len(it.Paths) == 0 {
		return
	}
	it.Name += " (" + strconv.Itoa(len(it.Paths)) + ")"
	it.Meta = map[string]string{"kept": files[0].path}
	it.Note = "Timestamped recovery copies of ~/.claude.json written on config changes; the newest one and ~/.claude.json.backup are kept."
	s.publish(it, pubOpts{})
}
