package aitools

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

	"github.com/ludwig-pro/lu-cleaner/internal/core"
)

// ---------------------------------------------------------------- CachedData

// cursorProduct returns the commit of the installed Cursor.app build.
func (s *scanner) cursorCommit() string {
	app := s.findApp("Cursor")
	if app == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(app, "Contents/Resources/app/product.json"))
	if err != nil {
		return ""
	}
	var p struct {
		Commit string `json:"commit"`
	}
	if json.Unmarshal(data, &p) != nil {
		return ""
	}
	return p.Commit
}

// cursorCachedData proposes the CachedData/<commit> dirs of old Cursor builds:
// only the running build's commit is used.
func (s *scanner) cursorCachedData() {
	root := s.appSupport("Cursor/CachedData")
	if !s.usable(root) {
		return
	}
	var dirs []entry
	for _, e := range list(root, false) {
		if e.dir {
			dirs = append(dirs, e)
		}
	}
	if len(dirs) < 2 {
		return
	}
	keep := s.cursorCommit()
	found := false
	for _, d := range dirs {
		if d.name == keep {
			found = true
		}
	}
	reason := "commit of the installed Cursor.app"
	if !found {
		keep, reason = pickKeep(dirs, keepNewestMtime), "newest (Cursor.app build not found)"
	}
	it := s.newItem("cursor-cached-data-old-builds", core.CatIDE, "", core.RiskSafe)
	it.ID = itemID(it.Kind, root)
	it.ProcessGuard = procCursor
	it.Recommended = true
	for _, d := range dirs {
		if d.name == keep {
			continue
		}
		it.Paths = append(it.Paths, d.path)
		it.LastUsed = maxTime(it.LastUsed, d.mtime)
	}
	it.Location = root + "/…"
	it.Name = "Cursor CachedData of old app builds (" + strconv.Itoa(len(it.Paths)) + ")"
	it.Meta = map[string]string{"kept": keep, "kept_reason": reason}
	it.Note = "V8 code caches of previous Cursor builds (one dir per update); only the installed build's commit is used and it is rebuilt if missing."
	s.publish(it, pubOpts{placeholder: true})
}

// ---------------------------------------------------------------- workspaceStorage

// cursorWorkspaceStorage handles User/workspaceStorage/<hash>: whole entries
// whose folder no longer exists (typical after deleting worktrees), and, for
// the others, the per-workspace data of extensions that are not installed
// anymore (redhat.java indexes are GBs).
func (s *scanner) cursorWorkspaceStorage() {
	root := s.appSupport("Cursor/User/workspaceStorage")
	if !s.usable(root) {
		return
	}
	entries := list(root, false)
	if len(entries) == 0 {
		return
	}
	installed, knowAll := s.cursorInstalledExtensions()

	orphans := s.newItem("cursor-workspace-storage-orphans", core.CatIDE, "", core.RiskModerate)
	orphans.ID = itemID(orphans.Kind, root)
	orphans.ProcessGuard = procCursor
	orphans.Recommended = true
	deadExt := map[string]*core.Item{}

	for _, e := range entries {
		if s.ctx.Err() != nil {
			return
		}
		if !e.dir {
			continue
		}
		target, ok := workspaceTarget(filepath.Join(e.path, "workspace.json"))
		if !ok {
			continue
		}
		ex := pathExistence(target)
		if ex == existNo {
			orphans.Paths = append(orphans.Paths, e.path)
			orphans.LastUsed = maxTime(orphans.LastUsed, newestShallow(e.path))
			continue
		}
		if !knowAll {
			continue
		}
		for _, c := range list(e.path, false) {
			if !c.dir || !reExtID.MatchString(c.name) || installed[strings.ToLower(c.name)] {
				continue
			}
			it := deadExt[strings.ToLower(c.name)]
			if it == nil {
				it = s.newItem("cursor-workspace-dead-extension-data", core.CatIDE, "", core.RiskSafe)
				it.ID = itemID(it.Kind, root+"/*/"+c.name)
				it.ProcessGuard = procCursor
				it.Recommended = true
				it.Meta = map[string]string{"extension": c.name}
				deadExt[strings.ToLower(c.name)] = it
			}
			it.Paths = append(it.Paths, c.path)
			it.LastUsed = maxTime(it.LastUsed, c.mtime)
		}
	}
	if n := len(orphans.Paths); n > 0 {
		orphans.Location = root + "/…"
		orphans.Name = "Cursor workspace state of deleted folders (" + strconv.Itoa(n) + ")"
		orphans.Note = "Per-workspace UI state, indexes and legacy chat data of folders/worktrees that no longer exist; recreated empty if the folder is opened again."
		orphans.Meta = map[string]string{"workspaces": strconv.Itoa(n)}
		dirs := append([]string(nil), orphans.Paths...)
		orphans.Recheck = func(context.Context) error {
			for _, d := range dirs {
				if t, ok := workspaceTarget(filepath.Join(d, "workspace.json")); ok && pathExistence(t) != existNo {
					return fmt.Errorf("folder %s exists again: rescan", t)
				}
			}
			return nil
		}
		s.publish(orphans, pubOpts{placeholder: true, newest: true})
	}
	for _, k := range sortedKeys(deadExt) {
		it := deadExt[k]
		ext := it.Meta["extension"]
		it.Location = root + "/*/" + ext
		it.Name = "Cursor workspace data of uninstalled extension " + ext + " (" + plural(len(it.Paths), "workspace", "workspaces") + ")"
		it.Note = "Workspace data (language-server indexes…) of an extension that is no longer installed in Cursor; dead data, rebuilt if the extension is reinstalled."
		s.publish(it, pubOpts{placeholder: true, newest: true})
	}
}

// reExtID matches an extension identifier "publisher.name".
var reExtID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*\.[A-Za-z0-9][A-Za-z0-9._-]*$`)

// workspaceTarget returns the local folder (or .code-workspace file) of a
// workspace.json. Remote URIs are not ok.
func workspaceTarget(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var ws struct {
		Folder    string `json:"folder"`
		Workspace string `json:"workspace"`
	}
	if json.Unmarshal(data, &ws) != nil {
		return "", false
	}
	raw := ws.Folder
	if raw == "" {
		raw = ws.Workspace
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "file" || u.Path == "" || (u.Host != "" && u.Host != "localhost") {
		return "", false
	}
	return filepath.Clean(u.Path), true
}

// cursorInstalledExtensions returns the lowercased ids of user and built-in
// extensions. ok is false when the built-in list cannot be read (Cursor.app
// missing): then nobody can say an extension is uninstalled.
func (s *scanner) cursorInstalledExtensions() (map[string]bool, bool) {
	ids := map[string]bool{}
	extDir := s.home(".cursor/extensions")
	for _, e := range list(extDir, false) {
		if id, _, ok := parseExtensionDir(e.name); ok && e.dir {
			ids[strings.ToLower(id)] = true
		}
	}
	for _, rec := range readExtensionsJSON(filepath.Join(extDir, "extensions.json")) {
		ids[strings.ToLower(rec.Identifier.ID)] = true
	}
	app := s.findApp("Cursor")
	if app == "" {
		return ids, false
	}
	pkgs, _ := filepath.Glob(filepath.Join(app, "Contents/Resources/app/extensions/*/package.json"))
	n := 0
	for _, p := range pkgs {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var pkg struct {
			Publisher string `json:"publisher"`
			Name      string `json:"name"`
		}
		if json.Unmarshal(data, &pkg) != nil || pkg.Name == "" {
			continue
		}
		ids[strings.ToLower(pkg.Publisher+"."+pkg.Name)] = true
		n++
	}
	return ids, n > 0
}

// ---------------------------------------------------------------- extensions

type extensionRecord struct {
	Identifier struct {
		ID string `json:"id"`
	} `json:"identifier"`
	Version          string `json:"version"`
	RelativeLocation string `json:"relativeLocation"`
	Location         struct {
		Path string `json:"path"`
	} `json:"location"`
}

func readExtensionsJSON(path string) []extensionRecord {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var recs []extensionRecord
	if json.Unmarshal(data, &recs) != nil {
		return nil
	}
	return recs
}

// reExtDir splits "<publisher>.<name>-<version>[-<platform>]".
var reExtDir = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9-]*\.[A-Za-z0-9][A-Za-z0-9._-]*?)-(\d+\.\d+[0-9A-Za-z.+]*)(?:-(.+))?$`)

func parseExtensionDir(name string) (id, version string, ok bool) {
	m := reExtDir.FindStringSubmatch(name)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// cursorExtensions proposes extension versions superseded by a newer install
// (listed in .obsolete, or not referenced by extensions.json while another
// version of the same extension is).
func (s *scanner) cursorExtensions() {
	root := s.home(".cursor/extensions")
	if !s.usable(root) {
		return
	}
	dirs := list(root, false)
	if len(dirs) == 0 {
		return
	}
	obsolete := map[string]bool{}
	if data, err := os.ReadFile(filepath.Join(root, ".obsolete")); err == nil {
		var m map[string]bool
		if json.Unmarshal(data, &m) == nil {
			for k, v := range m {
				if v {
					obsolete[k] = true
				}
			}
		}
	}
	recs := readExtensionsJSON(filepath.Join(root, "extensions.json"))
	referenced := map[string]bool{} // dir names
	refIDs := map[string]string{}   // lowercased id -> referenced dir
	for _, r := range recs {
		loc := r.RelativeLocation
		if loc == "" && r.Location.Path != "" {
			loc = filepath.Base(r.Location.Path)
		}
		if loc != "" {
			referenced[loc] = true
			refIDs[strings.ToLower(r.Identifier.ID)] = loc
		}
	}
	type ext struct {
		entry
		id, version string
	}
	byID := map[string][]ext{}
	for _, d := range dirs {
		if !d.dir {
			continue
		}
		id, ver, ok := parseExtensionDir(d.name)
		if !ok {
			continue
		}
		byID[strings.ToLower(id)] = append(byID[strings.ToLower(id)], ext{d, id, ver})
	}
	it := s.newItem("cursor-extension-old-versions", core.CatIDE, "", core.RiskModerate)
	it.ID = itemID(it.Kind, root)
	it.ProcessGuard = procCursor
	it.Recommended = true
	var names []string
	for _, id := range sortedKeys(byID) {
		vs := byID[id]
		sort.Slice(vs, func(i, j int) bool { return compareVersions(vs[i].version, vs[j].version) > 0 })
		for i, e := range vs {
			stale := false
			switch {
			case obsolete[e.name]:
				stale = true
			case len(referenced) > 0:
				// extensions.json is authoritative when present
				stale = !referenced[e.name] && refIDs[id] != "" && refIDs[id] != e.name
			default:
				stale = i > 0 // no registry: keep the highest version
			}
			if !stale {
				continue
			}
			it.Paths = append(it.Paths, e.path)
			it.LastUsed = maxTime(it.LastUsed, e.mtime)
			names = append(names, e.name)
		}
	}
	if len(it.Paths) == 0 {
		return
	}
	it.Location = root + "/…"
	it.Name = "Cursor superseded extension versions (" + strconv.Itoa(len(it.Paths)) + ")"
	it.Meta = map[string]string{"versions": strings.Join(names, ", ")}
	it.Note = "Older versions of extensions that were updated (marked obsolete or not referenced by extensions.json); Cursor only loads the current version."
	s.publish(it, pubOpts{placeholder: true})
}

// ---------------------------------------------------------------- ~/.cursor/projects

var cursorTranscriptDirs = []string{"agent-transcripts", "agent-tools", "terminals", "canvases", "agent-notes"}

var reDigits = regexp.MustCompile(`^\d+$`)

// cursorProjects handles ~/.cursor/projects/<encoded-folder>: whole dirs of
// folders that no longer exist, else MCP descriptor caches (regenerated) and
// agent transcripts older than transcriptAge.
func (s *scanner) cursorProjects() {
	root := s.home(".cursor/projects")
	if !s.usable(root) {
		return
	}
	entries := list(root, false)
	if len(entries) == 0 {
		return
	}
	orphans := s.newItem("cursor-agent-orphan-projects", core.CatAI, "", core.RiskModerate)
	orphans.ID = itemID(orphans.Kind, root)
	orphans.Recommended = true
	mcps := s.newItem("cursor-agent-mcp-caches", core.CatAI, "", core.RiskSafe)
	mcps.ID = itemID(mcps.Kind, root)
	old := s.newItem("cursor-agent-old-transcripts", core.CatAI, "", core.RiskCaution)
	old.ID = itemID(old.Kind, root)
	oldProjects := 0

	for _, e := range entries {
		if s.ctx.Err() != nil {
			return
		}
		if !e.dir {
			continue
		}
		ex := existUnknown
		if !reDigits.MatchString(e.name) { // numeric names are folder-less chats
			if data, err := os.ReadFile(filepath.Join(e.path, ".workspace-trusted")); err == nil {
				if p := findJSONString(data, "workspacePath"); filepath.IsAbs(p) {
					ex = pathExistence(p)
				}
			}
			if ex == existUnknown {
				_, ex = s.cursorRes.resolve(e.name)
			}
		}
		if t := newestShallow(e.path); ex == existNo && s.now.Sub(t) > liveGrace {
			orphans.Paths = append(orphans.Paths, e.path)
			orphans.LastUsed = maxTime(orphans.LastUsed, t)
			continue
		}
		if p := filepath.Join(e.path, "mcps"); isDir(p) {
			mcps.Paths = append(mcps.Paths, p)
			mcps.LastUsed = maxTime(mcps.LastUsed, newestShallow(p))
		}
		counted := false
		for _, sub := range cursorTranscriptDirs {
			p := filepath.Join(e.path, sub)
			if !isDir(p) {
				continue
			}
			t := newestShallow(p)
			if s.now.Sub(t) < transcriptAge {
				continue
			}
			old.Paths = append(old.Paths, p)
			old.LastUsed = maxTime(old.LastUsed, t)
			if !counted {
				oldProjects++
				counted = true
			}
		}
	}
	if n := len(orphans.Paths); n > 0 {
		orphans.Location = root + "/…"
		orphans.Name = "Cursor agent data of deleted folders (" + strconv.Itoa(n) + ")"
		orphans.Note = "Agent transcripts, terminals, canvases and MCP caches of folders/worktrees that no longer exist (folder decoded from the directory name)."
		s.publish(orphans, pubOpts{placeholder: true, newest: true})
	}
	if n := len(mcps.Paths); n > 0 {
		mcps.Location = root + "/*/mcps"
		mcps.Name = "Cursor agent MCP descriptor caches (" + strconv.Itoa(n) + ")"
		mcps.Note = "Per-project copies of MCP tool descriptors; regenerated from the MCP servers on the next agent run."
		s.publish(mcps, pubOpts{placeholder: true})
	}
	if len(old.Paths) > 0 {
		old.Location = root + "/…"
		old.Name = "Cursor agent transcripts > 30d (" + plural(oldProjects, "project", "projects") + ")"
		old.Note = "Agent transcripts, tool outputs, terminals and canvases untouched for 30+ days; not regenerated."
		s.publish(old, pubOpts{placeholder: true})
	}
}
