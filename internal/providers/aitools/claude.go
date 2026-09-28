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
	"golang.org/x/text/unicode/norm"
)

// ---------------------------------------------------------------- Claude Code transcripts

// claudeLive is the set of Claude Code sessions alive right now, from the
// ~/.claude/sessions/<pid>.json registry (the .key files are never read).
type claudeLive struct {
	ids  map[string]bool
	cwds map[string]bool // pathKey(cwd)
}

// hasCwd reports whether a live session runs in folder p.
func (l claudeLive) hasCwd(p string) bool { return p != "" && l.cwds[pathKey(p)] }

// encodesTo reports whether a live session runs in a folder whose Claude
// Code project directory is name (both Unicode normalizations are tried:
// APFS keeps names as created, NFC or NFD).
func (l claudeLive) encodesTo(name string) (string, bool) {
	for c := range l.cwds {
		for _, v := range []string{c, norm.NFD.String(c)} {
			if strings.EqualFold(claudeEncode(v), name) {
				return c, true
			}
		}
	}
	return "", false
}

// pathKey folds a path for comparisons: APFS is case- and
// normalization-insensitive.
func pathKey(p string) string { return strings.ToLower(norm.NFC.String(filepath.Clean(p))) }

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
			live.cwds[pathKey(reg.Cwd)] = true
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
	// cwd is the folder this very session ran in ("" = unknown) and src
	// where it comes from ("transcript", "sessions-index"). Several folders
	// share one project dir (Claude Code maps every non-alphanumeric
	// character to '-': my-app, my_app and my.app collide), so the
	// existence of a folder is decided per session, never per project dir.
	cwd string
	src string
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
	for _, cs := range sessions {
		ordered = append(ordered, cs)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if !ordered[i].mtime.Equal(ordered[j].mtime) {
			return ordered[i].mtime.After(ordered[j].mtime)
		}
		return ordered[i].id < ordered[j].id
	})

	// Each session's own folder: the "cwd" of its transcript, else its
	// sessions-index.json entry.
	idx := readClaudeSessionsIndex(proj.path)
	anyCwd := false
	for _, cs := range ordered {
		if cs.jsonl != "" {
			if cwd := readJSONStringField(cs.jsonl, "cwd", 512<<10); filepath.IsAbs(cwd) {
				cs.cwd, cs.src = filepath.Clean(cwd), "transcript"
			}
		}
		if cs.cwd == "" {
			if p := idx.byID[cs.id]; filepath.IsAbs(p) {
				cs.cwd, cs.src = filepath.Clean(p), "sessions-index"
			}
		}
		anyCwd = anyCwd || cs.cwd != ""
	}
	// When no session names its folder, a project-level guess (the index's
	// originalPath, else the lossy directory name) applies to all of them.
	// Such a guess cannot tell colliding folders apart: never preselected.
	lossy := !anyCwd
	dirNameEx := existUnknown
	if lossy {
		cwd, src := "", "dir-name"
		if filepath.IsAbs(idx.originalPath) {
			cwd, src = filepath.Clean(idx.originalPath), "sessions-index"
		} else {
			cwd, dirNameEx = s.claudeRes.resolve(proj.name)
		}
		for _, cs := range ordered {
			cs.cwd, cs.src = cwd, src
		}
	}
	exists := map[string]existence{}
	exOf := func(cs *claudeSession) existence {
		if cs.src == "dir-name" {
			return dirNameEx
		}
		if cs.cwd == "" {
			return existUnknown
		}
		ex, ok := exists[cs.cwd]
		if !ok {
			ex = pathExistence(cs.cwd)
			exists[cs.cwd] = ex
		}
		return ex
	}
	// A live session whose folder encodes to this directory writes here now.
	_, liveHere := live.encodesTo(proj.name)

	// Orphans: sessions whose own folder is gone (deleted worktree...).
	var orphan, rest []*claudeSession
	for _, cs := range ordered {
		isLive := live.ids[cs.id] || live.hasCwd(cs.cwd) || (lossy && liveHere)
		if !isLive && exOf(cs) == existNo && s.now.Sub(cs.mtime) > liveGrace {
			orphan = append(orphan, cs)
		} else {
			rest = append(rest, cs)
		}
	}
	if len(orphan) > 0 {
		// The directory belongs to the orphans alone: its other files
		// (sessions-index.json...) go too, and the directory itself when it
		// holds no auto-memory.
		owned := len(rest) == 0 && !liveHere
		if !owned {
			others = nil
		}
		s.claudeOrphan(proj, orphan, len(rest), owned && !hasMemory, others, hasMemory, lossy)
	}
	if len(rest) == 0 {
		return
	}

	// Otherwise: sessions not touched for claudeSessionAge.
	var label *claudeSession // newest session naming a folder
	for _, cs := range rest {
		if cs.cwd != "" || cs.src == "dir-name" {
			label = cs
			break
		}
	}
	cwd, source := "", ""
	if label != nil {
		cwd, source = label.cwd, label.src
	}
	meta := map[string]string{"cwd_source": source}
	name := cwd
	if cwd != "" {
		meta["cwd"] = cwd
	} else {
		name = decodeNaive(proj.name, true)
	}
	cutoff := s.now.Add(-claudeSessionAge)
	it := s.newItem("claude-code-old-sessions", core.CatAI, "Claude Code sessions > 30d · "+s.env.Pretty(name), core.RiskCaution)
	it.ID = itemID(it.Kind, proj.path)
	it.Project = cwd
	n := 0
	for _, cs := range rest {
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
	if label != nil && exOf(label) == existUnknown && cwd != "" && missingVolume(cwd) {
		meta["cwd_status"] = "volume not mounted"
	}
	it.Meta = meta
	it.Name += " (" + plural(n, "session", "sessions") + ")"
	it.Note = "Session transcripts with their subagent logs and tool outputs, untouched for 30+ days; deleting them removes them from `claude --resume` / history (Claude Code prunes after cleanupPeriodDays)."
	s.publish(it, pubOpts{placeholder: true})
}

// claudeOrphan emits the sessions of proj whose folder no longer exists.
// kept is the number of other sessions of the directory; whole: the
// directory itself may go (only orphans, no auto-memory, no live session
// writing there); others: extra files removed with the sessions; lossy: the
// folder was guessed for the whole directory, not read per session.
//
// It is only preselected (moderate) when every folder was read from the
// sessions themselves, none is a restorable Conductor workspace, and nothing
// was written for orphanRecommendAge: a folder missing today may be renamed,
// unarchived or re-created tomorrow, and transcripts never come back.
func (s *scanner) claudeOrphan(proj entry, orphan []*claudeSession, kept int, whole bool, others []string, hasMemory, lossy bool) {
	var newest time.Time
	cwdSet := map[string]bool{}
	ids := map[string]bool{}
	restorable := false
	for _, cs := range orphan {
		newest = maxTime(newest, cs.mtime)
		ids[cs.id] = true
		if cs.cwd != "" {
			cwdSet[cs.cwd] = true
			restorable = restorable || s.restorable(cs.cwd)
		}
	}
	cwds := sortedKeys(cwdSet)
	label := decodeNaive(proj.name, true)
	if len(cwds) > 0 {
		label = cwds[0]
	}
	label = s.env.Pretty(label)
	if len(cwds) > 1 {
		label += fmt.Sprintf(" (+%d other folders)", len(cwds)-1)
	}
	recommend := !lossy && !restorable && s.now.Sub(newest) >= orphanRecommendAge
	risk := core.RiskCaution
	if recommend {
		risk = core.RiskModerate
	}
	it := s.newItem("claude-code-orphan-project", core.CatAI, "Claude Code data of deleted folder · "+label, risk)
	it.ID = itemID(it.Kind, proj.path)
	if len(cwds) > 0 {
		it.Project = cwds[0]
	}
	it.LastUsed = newest
	it.Recommended = recommend
	it.NoRecommend = !recommend
	meta := map[string]string{"cwd_source": orphan[0].src, "sessions": strconv.Itoa(len(orphan))}
	if len(cwds) > 0 {
		meta["cwd"] = cwds[0]
	}
	if len(cwds) > 1 {
		meta["cwds"] = strings.Join(cwds, ", ")
	}
	it.Note = "Transcripts, subagent logs and tool outputs of sessions run in a folder that no longer exists (deleted worktree…); `claude --resume` cannot reach them unless the folder is recreated."
	if whole {
		it.Path = proj.path
	} else {
		for _, cs := range orphan {
			it.Paths = append(it.Paths, cs.paths()...)
		}
		it.Paths = append(it.Paths, others...)
		if kept > 0 {
			meta["kept_sessions"] = strconv.Itoa(kept)
			it.Note += " Sessions of other folders sharing this project directory are kept."
		}
		sort.Strings(it.Paths)
		it.Location = proj.path + "/…"
	}
	if hasMemory {
		meta["kept"] = "memory/"
		it.Note += " The project's auto-memory (memory/) is kept."
	}
	switch {
	case lossy:
		it.Note += " Folder guessed for the whole directory (lossy): double-check before cleaning."
	case restorable:
		it.Note += " Conductor workspace: an archived workspace can be restored and its sessions resumed from these transcripts."
	case !recommend:
		it.Note += " Not preselected before 30 days: the folder may come back (renamed, restored, re-created worktree)."
	}
	it.Meta = meta
	it.Recheck = s.claudeOrphanRecheck(claudeOrphanCheck{
		projDir: proj.path, cwds: cwds, ids: ids, whole: whole, lossy: lossy,
		dirName: lossy && orphan[0].src == "dir-name",
	})
	s.publish(it, pubOpts{placeholder: true})
}

// restorable reports whether a missing folder p may legitimately come back:
// Conductor archives a workspace by deleting its folder and restores it on
// unarchive (its sessions are then resumed from the transcripts).
func (s *scanner) restorable(p string) bool {
	return strings.Contains(strings.ToLower(p), "/conductor/workspaces/")
}

// claudeOrphanCheck is what claudeOrphanRecheck re-validates.
type claudeOrphanCheck struct {
	projDir string
	cwds    []string        // folders that must still be missing
	ids     map[string]bool // sessions removed
	whole   bool            // the whole directory is removed
	lossy   bool            // folders guessed for the whole directory
	dirName bool            // ... from the directory name: re-resolve it
}

// claudeOrphanRecheck re-validates an orphan right before cleaning: every
// folder must still be missing, no targeted session may have been written
// meanwhile or be live, and when the whole directory goes no other session
// (nor an auto-memory) may have appeared in it.
func (s *scanner) claudeOrphanRecheck(c claudeOrphanCheck) func(context.Context) error {
	root := s.claudeRes.root
	projDir, cwds, ids, whole := c.projDir, c.cwds, c.ids, c.whole
	return func(context.Context) error {
		for _, cwd := range cwds {
			if pathExistence(cwd) != existNo {
				return fmt.Errorf("%s exists again (or cannot be checked): rescan", cwd)
			}
		}
		if c.dirName {
			r := newResolver(claudeEncode)
			r.root = root
			if _, ex := r.resolve(filepath.Base(projDir)); ex != existNo {
				return fmt.Errorf("a folder encoded as %s may exist: rescan", filepath.Base(projDir))
			}
		}
		for _, e := range list(projDir, true) {
			id := e.name
			switch {
			case e.name == "memory":
				if whole {
					return fmt.Errorf("%s now holds an auto-memory: rescan", projDir)
				}
				continue
			case !e.dir && strings.HasSuffix(e.name, ".jsonl"):
				id = strings.TrimSuffix(e.name, ".jsonl")
			case !e.dir:
				continue
			}
			if !ids[id] {
				if whole {
					return fmt.Errorf("new Claude Code session %s in %s: rescan", id, projDir)
				}
				continue
			}
			t := e.mtime
			if e.dir {
				t = newestShallow(e.path)
			}
			if time.Since(t) < liveGrace {
				return fmt.Errorf("a Claude Code session wrote %s recently", e.name)
			}
		}
		live := s.claudeLiveSessions()
		for id := range ids {
			if live.ids[id] {
				return fmt.Errorf("Claude Code session %s is live", id)
			}
		}
		for _, cwd := range cwds {
			if live.hasCwd(cwd) {
				return fmt.Errorf("a live Claude Code session runs in %s", cwd)
			}
		}
		if l, ok := live.encodesTo(filepath.Base(projDir)); ok && (whole || c.lossy) {
			return fmt.Errorf("a live Claude Code session runs in %s", l)
		}
		return nil
	}
}

// claudeIndex is what sessions-index.json says about a project directory.
type claudeIndex struct {
	originalPath string
	byID         map[string]string // sessionId -> projectPath
}

func readClaudeSessionsIndex(projDir string) claudeIndex {
	idx := claudeIndex{byID: map[string]string{}}
	data, err := os.ReadFile(filepath.Join(projDir, "sessions-index.json"))
	if err != nil || len(data) > 16<<20 {
		return idx
	}
	var raw struct {
		OriginalPath string `json:"originalPath"`
		Entries      []struct {
			SessionID   string `json:"sessionId"`
			ProjectPath string `json:"projectPath"`
		} `json:"entries"`
	}
	if json.Unmarshal(data, &raw) != nil {
		return idx
	}
	idx.originalPath = raw.OriginalPath
	for _, e := range raw.Entries {
		if e.SessionID != "" && e.ProjectPath != "" {
			idx.byID[e.SessionID] = e.ProjectPath
		}
	}
	return idx
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
