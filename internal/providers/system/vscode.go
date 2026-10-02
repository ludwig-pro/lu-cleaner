package system

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
	"io/fs"
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

// vscodeFlavor describes one VS Code build sharing the same data layout.
// Cursor, Windsurf, Antigravity... are AI editors owned by the aitools
// provider.
type vscodeFlavor struct {
	key      string   // kind prefix
	name     string   // display name
	data     string   // ~/Library/Application Support/<data>
	dotDir   string   // ~/<dotDir>/extensions
	apps     []string // bundle names (without .app)
	bundleID string
	proc     string // sysx.Running pattern of the main executable
}

var vscodeFlavors = []vscodeFlavor{
	{"vscode", "VS Code", "Code", ".vscode", []string{"Visual Studio Code"}, "com.microsoft.VSCode",
		"/Visual Studio Code.app/Contents/MacOS/"},
	{"vscode-insiders", "VS Code Insiders", "Code - Insiders", ".vscode-insiders", []string{"Visual Studio Code - Insiders"}, "com.microsoft.VSCodeInsiders",
		"/Visual Studio Code - Insiders.app/Contents/MacOS/"},
	{"vscodium", "VSCodium", "VSCodium", ".vscode-oss", []string{"VSCodium"}, "com.vscodium",
		"/VSCodium.app/Contents/MacOS/"},
}

const (
	workspaceOrphanAge = 7 * 24 * time.Hour // workspaceStorage entries of deleted folders
	lsIndexAge         = 3 * 24 * time.Hour // language-server indexes of live workspaces
)

// lsIndexDirs are per-workspace extension storage dirs that only hold
// rebuildable language-server indexes (the Java LS index is 200-500 MB per
// React Native project whose android/ folder was opened).
var lsIndexDirs = map[string]bool{"redhat.java": true, "ms-vscode.cpptools": true, "vscjava.vscode-gradle": true}

func (s *scan) vscode() {
	for _, f := range vscodeFlavors {
		if s.ctx.Err() != nil {
			return
		}
		data := s.appSupport(f.data)
		ext := s.home(f.dotDir + "/extensions")
		if !isDir(data) && !isDir(ext) {
			continue
		}
		v := &vsc{s: s, f: f, data: data, ext: ext, app: s.findApp(f.apps...)}
		v.installed = v.app != "" || s.appInstalled(f.bundleID)
		v.scan()
	}
}

type vsc struct {
	s         *scan
	f         vscodeFlavor
	data, ext string
	app       string
	installed bool
}

func (v *vsc) item(kind, name string, risk core.Risk) *core.Item {
	it := v.s.newItem(v.f.key+"-"+kind, core.CatIDE, name, risk)
	it.ProcessGuard = []string{v.f.proc}
	return it
}

func (v *vsc) scan() {
	if isDir(v.data) {
		if pl := v.s.locate(v.data); pl.External || pl.Dangling || pl.Link {
			return // lives elsewhere: nothing to gain here
		}
		v.cachedData()
		v.vsixCache()
		v.caches()
		v.workspaceStorage()
		v.history()
	}
	if isDir(v.ext) {
		if pl := v.s.locate(v.ext); pl.External || pl.Dangling || pl.Link {
			return
		}
		if v.installed {
			v.oldExtensions()
		} else {
			v.leftoverExtensions()
		}
	}
}

// productCommit returns the commit of the installed app build.
func (v *vsc) productCommit() string {
	if v.app == "" {
		return ""
	}
	data, err := fsx.ReadFile(v.s.ctx, filepath.Join(v.app, "Contents/Resources/app/product.json"))
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

// cachedData: CachedData/<commit> holds V8 code caches of one app build; only
// the installed build's folder is ever read again.
func (v *vsc) cachedData() {
	root := filepath.Join(v.data, "CachedData")
	var dirs []entry
	for _, e := range list(v.s.ctx, root, false) {
		if e.dir {
			dirs = append(dirs, e)
		}
	}
	if len(dirs) == 0 {
		return
	}
	keep, reason := v.productCommit(), "commit of the installed app"
	found := false
	for _, d := range dirs {
		if d.name == keep {
			found = true
		}
	}
	if !found {
		if v.installed {
			// Installed but build not identified: keep the newest folder.
			sort.Slice(dirs, func(i, j int) bool { return dirs[i].mtime.After(dirs[j].mtime) })
			keep, reason = dirs[0].name, "newest (app build not identified)"
		} else {
			keep, reason = "", "app not installed"
		}
	}
	it := v.item("cached-data", "", core.RiskSafe)
	it.ProcessGuard = nil // other builds' folders are never opened by the running app
	it.ID = itemID(it.Kind, root)
	it.Recommended = true
	for _, d := range dirs {
		if d.name == keep {
			continue
		}
		it.Paths = append(it.Paths, d.path)
		it.LastUsed = maxTime(it.LastUsed, d.mtime)
	}
	if len(it.Paths) == 0 {
		return
	}
	it.Location = root + "/…"
	it.Name = v.f.name + " CachedData of old app builds (" + strconv.Itoa(len(it.Paths)) + ")"
	it.Meta = map[string]string{"kept": keep, "kept_reason": reason}
	it.Note = "V8 code caches of previous " + v.f.name + " builds (one folder per update, never pruned by the editor); the current build's folder is kept."
	v.s.publish(it, pubOpts{})
}

// vsixCache: downloaded .vsix packages.
func (v *vsc) vsixCache() {
	root := filepath.Join(v.data, "CachedExtensionVSIXs")
	var paths []string
	for _, e := range list(v.s.ctx, root, true) {
		if e.name != ".DS_Store" {
			paths = append(paths, e.path)
		}
	}
	if len(paths) == 0 {
		return
	}
	it := v.item("vsix-cache", v.f.name+" downloaded extension packages (.vsix)", core.RiskSafe)
	it.ID = itemID(it.Kind, root)
	it.Location = root + "/…"
	it.Paths = paths
	it.Note = "Extension packages kept after installation; downloaded again from the marketplace if an extension is reinstalled."
	v.s.publish(it, pubOpts{newest: true})
}

// caches: Chromium caches of the Electron shell, crash dumps and session logs.
func (v *vsc) caches() {
	var paths []string
	for _, rel := range []string{"Cache", "Code Cache", "GPUCache", "DawnGraphiteCache", "DawnWebGPUCache", "GrShaderCache", "ShaderCache", "GraphiteDawnCache", "Service Worker/CacheStorage"} {
		if p := filepath.Join(v.data, rel); isDir(p) {
			paths = append(paths, p)
		}
	}
	for _, e := range list(v.s.ctx, filepath.Join(v.data, "Crashpad", "completed"), false) {
		paths = append(paths, e.path)
	}
	// logs/<session>: keep the newest session (the running one, if any).
	var logs []entry
	for _, e := range list(v.s.ctx, filepath.Join(v.data, "logs"), false) {
		if e.dir {
			logs = append(logs, e)
		}
	}
	sort.Slice(logs, func(i, j int) bool { return logs[i].name > logs[j].name })
	for i, e := range logs {
		if i == 0 {
			continue
		}
		paths = append(paths, e.path)
	}
	if len(paths) == 0 {
		return
	}
	it := v.item("caches", v.f.name+" caches & old logs", core.RiskSafe)
	it.ID = itemID(it.Kind, v.data)
	it.Location = v.data + "/…"
	it.Paths = paths
	it.Note = "HTTP / code / GPU caches of the editor window, crash dumps and logs of past sessions; rebuilt automatically."
	v.s.publish(it, pubOpts{newest: true})
}

// wsState is the classification of a workspaceStorage entry.
type wsState int

const (
	wsLive    wsState = iota
	wsOrphan          // folder deleted (worktree removed, project moved...)
	wsOffline         // folder on an unmounted volume: keep
	wsUnknown         // remote, empty window, unreadable: keep
)

// workspaceTarget reads workspace.json and returns the folder / workspace
// file URI.
func workspaceTarget(ctx context.Context, path string) (string, bool) {
	data, err := fsx.ReadFile(ctx, path)
	if err != nil {
		return "", false
	}
	var w struct {
		Folder    string `json:"folder"`
		Workspace string `json:"workspace"`
	}
	if json.Unmarshal(data, &w) != nil {
		return "", false
	}
	if w.Folder != "" {
		return w.Folder, true
	}
	return w.Workspace, w.Workspace != ""
}

// classifyWorkspace decides whether a workspace URI points to a folder that
// is gone for good.
func (s *scan) classifyWorkspace(uri string) (wsState, string) {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" {
		return wsUnknown, uri
	}
	p := filepath.Clean(u.Path)
	if !filepath.IsAbs(p) {
		return wsUnknown, p
	}
	_, err = os.Stat(p)
	switch {
	case err == nil:
		return wsLive, p
	case !errors.Is(err, fs.ErrNotExist):
		return wsUnknown, p // permission, I/O...: cannot tell
	}
	if vol := volumeOf(p); vol != "" && !exists(vol) {
		return wsOffline, p
	}
	cloud := filepath.Join(s.env.Home, "Library", "CloudStorage") + "/"
	if strings.HasPrefix(p, cloud) {
		first := strings.SplitN(strings.TrimPrefix(p, cloud), "/", 2)[0]
		if !exists(cloud + first) {
			return wsOffline, p
		}
	}
	return wsOrphan, p
}

// workspaceStorage: per-workspace state (UI state, extension storage). Entries
// whose folder was deleted are orphans (AI worktrees create one per opened
// worktree); for live ones, only the heavy language-server indexes.
func (v *vsc) workspaceStorage() {
	root := filepath.Join(v.data, "User", "workspaceStorage")
	entries := list(v.s.ctx, root, false)
	if len(entries) == 0 {
		return
	}
	orphans := v.item("workspace-orphans", "", core.RiskModerate)
	orphans.ID = itemID(orphans.Kind, root)
	orphans.Recommended = true
	indexes := v.item("ls-indexes", "", core.RiskSafe)
	indexes.ID = itemID(indexes.Kind, root)
	offline := 0
	var samples []string
	for _, e := range entries {
		if v.s.ctx.Err() != nil {
			return
		}
		if !e.dir {
			continue
		}
		uri, ok := workspaceTarget(v.s.ctx, filepath.Join(e.path, "workspace.json"))
		state, target := wsUnknown, ""
		if ok {
			state, target = v.s.classifyWorkspace(uri)
		}
		last := newestShallow(v.s.ctx, e.path)
		switch state {
		case wsOrphan:
			if v.s.now.Sub(last) < workspaceOrphanAge {
				continue
			}
			orphans.Paths = append(orphans.Paths, e.path)
			orphans.LastUsed = maxTime(orphans.LastUsed, last)
			if len(samples) < 4 {
				samples = append(samples, v.s.env.Pretty(target))
			}
			continue
		case wsOffline:
			offline++
		}
		for _, c := range list(v.s.ctx, e.path, false) {
			if c.dir && lsIndexDirs[c.name] && v.s.now.Sub(c.mtime) >= lsIndexAge {
				indexes.Paths = append(indexes.Paths, c.path)
				indexes.LastUsed = maxTime(indexes.LastUsed, c.mtime)
			}
		}
	}
	if n := len(orphans.Paths); n > 0 {
		orphans.Location = root + "/…"
		orphans.Name = v.f.name + " state of deleted workspaces (" + strconv.Itoa(n) + ")"
		orphans.Meta = map[string]string{"entries": strconv.Itoa(n), "examples": strings.Join(samples, ", ")}
		if offline > 0 {
			orphans.Meta["kept_offline_volume"] = strconv.Itoa(offline)
		}
		orphans.Note = "Per-workspace UI and extension state of folders that no longer exist (removed worktrees, deleted or moved projects); recreated empty if such a folder is opened again."
		v.s.publish(orphans, pubOpts{placeholder: true})
	}
	if n := len(indexes.Paths); n > 0 {
		indexes.Location = root + "/…"
		indexes.Name = v.f.name + " language-server indexes (" + plural(n, "workspace", "workspaces") + ")"
		indexes.Note = "Java (redhat.java), C/C++ and Gradle language-server indexes stored per workspace (hundreds of MB per React Native android/ folder); rebuilt when the folder is opened again."
		v.s.publish(indexes, pubOpts{})
	}
}

// history: Local History (previous versions of saved files). Not
// regenerable: report only.
func (v *vsc) history() {
	p := filepath.Join(v.data, "User", "History")
	if !isDir(p) {
		return
	}
	it := v.item("local-history", v.f.name+" Local History", core.RiskCaution)
	it.ProcessGuard = nil
	it.Path = p
	it.Note = "Previous versions of files saved in the editor (Timeline view): useful to recover code an agent overwrote; clear it from the editor (Local History: Delete All) if needed."
	v.s.report(it, pubOpts{newest: true})
}

// reExtFolder splits "<publisher>.<name>-<version>[-<platform>]".
var reExtFolder = regexp.MustCompile(`^([a-z0-9][a-z0-9-]*\.[a-z0-9][a-z0-9._-]*?)-(\d+\.\d+\.\d+(?:-[0-9a-z.]+)?)(?:-((?:darwin|linux|alpine|win32)-(?:x64|arm64|armhf|ia32)|web|universal))?$`)

// parseExtFolder returns the extension id and version of a folder name.
func parseExtFolder(name string) (id, version string, ok bool) {
	m := reExtFolder.FindStringSubmatch(strings.ToLower(name))
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// extensionsRegistry reads extensions.json: the folder names in use.
func extensionsRegistry(ctx context.Context, dir string) (map[string]bool, bool) {
	data, err := fsx.ReadFile(ctx, filepath.Join(dir, "extensions.json"))
	if err != nil {
		return nil, false
	}
	var recs []struct {
		RelativeLocation string `json:"relativeLocation"`
		Location         any    `json:"location"`
	}
	if json.Unmarshal(data, &recs) != nil {
		return nil, false
	}
	reg := map[string]bool{}
	for _, r := range recs {
		name := r.RelativeLocation
		if name == "" {
			switch l := r.Location.(type) {
			case string:
				name = filepath.Base(l)
			case map[string]any:
				for _, k := range []string{"fsPath", "path"} {
					if s, _ := l[k].(string); s != "" {
						name = filepath.Base(s)
						break
					}
				}
			}
		}
		if name != "" {
			reg[strings.ToLower(name)] = true
		}
	}
	return reg, true
}

// oldExtensions: extension folders that the editor no longer uses — listed
// in .obsolete, or superseded by another registered version of the same
// extension. Without extensions.json, only duplicates (older versions of an
// extension also present in a newer version) are proposed.
func (v *vsc) oldExtensions() {
	obsolete := map[string]bool{}
	if data, err := fsx.ReadFile(v.s.ctx, filepath.Join(v.ext, ".obsolete")); err == nil {
		var m map[string]any
		if json.Unmarshal(data, &m) == nil {
			for k, val := range m {
				if b, ok := val.(bool); !ok || b {
					obsolete[strings.ToLower(k)] = true
				}
			}
		}
	}
	reg, haveReg := extensionsRegistry(v.s.ctx, v.ext)
	type folder struct {
		e       entry
		id, ver string
	}
	byID := map[string][]folder{}
	var folders []folder
	for _, e := range list(v.s.ctx, v.ext, false) {
		if !e.dir {
			continue
		}
		id, ver, ok := parseExtFolder(e.name)
		if !ok {
			continue
		}
		f := folder{e: e, id: id, ver: ver}
		folders = append(folders, f)
		byID[id] = append(byID[id], f)
	}
	it := v.item("old-extensions", "", core.RiskSafe)
	it.ID = itemID(it.Kind, v.ext)
	it.Recommended = true
	var names []string
	for _, f := range folders {
		lname := strings.ToLower(f.e.name)
		stale := obsolete[lname]
		if !stale {
			if haveReg {
				if !reg[lname] {
					// superseded only if another version of the same id is registered
					for _, o := range byID[f.id] {
						if o.e.name != f.e.name && reg[strings.ToLower(o.e.name)] {
							stale = true
							break
						}
					}
				}
			} else if len(byID[f.id]) > 1 {
				newest := f
				for _, o := range byID[f.id] {
					if compareVersions(o.ver, newest.ver) > 0 {
						newest = o
					}
				}
				stale = newest.e.name != f.e.name
			}
		}
		if !stale {
			continue
		}
		it.Paths = append(it.Paths, f.e.path)
		it.LastUsed = maxTime(it.LastUsed, f.e.mtime)
		names = append(names, f.e.name)
	}
	if len(it.Paths) == 0 {
		return
	}
	it.Location = v.ext + "/…"
	it.Name = v.f.name + " old extension versions (" + strconv.Itoa(len(it.Paths)) + ")"
	it.Meta = map[string]string{"folders": strings.Join(names, ", ")}
	it.Note = "Previous versions of updated extensions (or marked obsolete) that the editor does not load anymore; the current versions are kept."
	v.s.publish(it, pubOpts{placeholder: true})
}

// leftoverExtensions: extensions of an editor that is not installed anymore.
func (v *vsc) leftoverExtensions() {
	if len(list(v.s.ctx, v.ext, false)) == 0 {
		return
	}
	it := v.item("extensions-leftover", v.f.name+" extensions (app not installed)", core.RiskCaution)
	it.Path = v.ext
	it.Note = "Extensions of " + v.f.name + ", which is not installed anymore; only needed if you reinstall it (they would be downloaded again)."
	v.s.publish(it, pubOpts{placeholder: true, newest: true})
}

// ------------------------------------------------------------------ versions

// versionParts splits "1.2.3-rc4" / "2024.3" into numbers.
func versionParts(v string) []int {
	var out []int
	cur, in := 0, false
	for _, r := range v {
		if r >= '0' && r <= '9' {
			cur = cur*10 + int(r-'0')
			in = true
			continue
		}
		if in {
			out = append(out, cur)
			cur, in = 0, false
		}
	}
	if in {
		out = append(out, cur)
	}
	return out
}

// compareVersions compares two version strings numerically.
func compareVersions(a, b string) int {
	pa, pb := versionParts(a), versionParts(b)
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return strings.Compare(a, b)
}
