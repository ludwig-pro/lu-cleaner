package apple

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
)

// DerivedData folders are named "<Project>-<28 lowercase letters>" (hash of
// the workspace path), so every git worktree gets its own multi-GB folder.
var ddHashRe = regexp.MustCompile(`^(.+)-[a-z]{28}$`)

// Worktree roots of AI tools and git, used to label DerivedData folders.
var worktreeMarkers = []string{"/worktrees/", "/.claude/worktrees/", "/conductor/workspaces/", "/.worktrees/"}

func (s *scan) derivedData() {
	def := s.lib("Developer", "Xcode", "DerivedData")
	s.derivedDataRoot(def, false)
	if c := s.customDerivedData(); c != "" && c != def {
		s.derivedDataRoot(c, true)
	}
}

// customDerivedData returns Xcode's custom DerivedData location (Settings ›
// Locations), "" when default or unknown.
func (s *scan) customDerivedData() string {
	if !s.env.Has("defaults") {
		return ""
	}
	out, err := s.output(5*time.Second, "defaults", "read", "com.apple.dt.Xcode", "IDECustomDerivedDataLocation")
	if err != nil {
		return ""
	}
	p := strings.TrimSpace(string(out))
	if strings.HasPrefix(p, "~/") {
		p = filepath.Join(s.env.Home, p[2:])
	}
	if !filepath.IsAbs(p) {
		return "" // relative (per-workspace) locations cannot be scanned globally
	}
	return filepath.Clean(p)
}

func (s *scan) derivedDataRoot(root string, custom bool) {
	ents, err := fsx.ReadDir(s.ctx, root)
	if err != nil {
		return
	}
	var shared []string
	for _, e := range ents {
		if s.ctx.Err() != nil {
			return
		}
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		path := filepath.Join(root, name)
		if s.skipPath(path) {
			continue
		}
		if strings.HasSuffix(name, ".noindex") {
			// ModuleCache.noindex, SymbolCache.noindex...: the catalog owns the
			// default location; a custom location is handled here.
			if custom {
				shared = append(shared, path)
			}
			continue
		}
		s.derivedDataItem(path, name)
	}
	if len(shared) > 0 {
		it := s.item("xcode-module-cache", core.CatXcode, root)
		it.Name = "Xcode shared module cache (custom DerivedData)"
		it.Risk = core.RiskSafe
		it.Method = core.MethodDelete
		it.ProcessGuard = []string{"Xcode", "xcodebuild"}
		it.Note = "Precompiled clang/Swift modules shared by every project; recompiled by the next build (slower first build)."
		setTargets(it, shared, root)
		if !s.applyPlace(it, shared[0]) {
			return
		}
		s.guardWarn(it)
		it.LastUsed = newestMTime(shared...)
		s.sizeLater(it, shared, func(it *core.Item, m measured) { it.LastUsed = maxTime(it.LastUsed, m.newest) })
	}
}

func (s *scan) derivedDataItem(path, name string) {
	info, _ := readPlistDict(filepath.Join(path, "info.plist"))
	ws := pString(info, "WorkspacePath")
	accessed := pTime(info, "LastAccessedDate")

	project := name
	if m := ddHashRe.FindStringSubmatch(name); m != nil {
		project = m[1]
	}
	if ws != "" {
		project = strings.TrimSuffix(filepath.Base(ws), filepath.Ext(ws))
	}
	orphan := ws != "" && workspaceGone(ws)

	it := s.item("xcode-derived-data", core.CatXcode, path)
	it.Path = path
	it.Name = project
	it.Risk = core.RiskSafe
	it.Method = core.MethodDelete
	it.ProcessGuard = []string{"Xcode", "xcodebuild"}
	// Best signal of last use: Xcode's own LastAccessedDate, or the last
	// build log / build products / index write (xcodebuild runs).
	it.LastUsed = maxTime(accessed, newestMTime(
		filepath.Join(path, "Logs", "Build"),
		filepath.Join(path, "Build", "Intermediates.noindex"),
		filepath.Join(path, "Build", "Products"),
		filepath.Join(path, "Index.noindex"),
		filepath.Join(path, "info.plist"),
	))
	if it.LastUsed.IsZero() {
		it.LastUsed = childrenNewest(s.ctx, path)
	}
	setMeta(it, "hash_dir", name)
	switch {
	case ws == "":
		it.Note = "Xcode build products, index and logs (no info.plist); recreated by the next build."
	case orphan:
		it.Name = project + " (workspace gone)"
		it.Recommended = true
		setMeta(it, "workspace", ws)
		setMeta(it, "orphan", "true")
		it.Note = "Build cache of " + s.env.Pretty(ws) + ", which no longer exists (removed worktree or clone): nothing will reuse it."
	default:
		setMeta(it, "workspace", ws)
		it.Project = projectRoot(ws)
		it.Note = "Build products, index and logs of " + s.env.Pretty(ws) + "; the next build recreates them (full rebuild + indexing)."
	}
	for _, m := range worktreeMarkers {
		if strings.Contains(ws, m) {
			setMeta(it, "worktree", "true")
			break
		}
	}
	if !s.applyPlace(it, path) {
		return
	}
	s.guardWarn(it)
	s.sizeLater(it, []string{path}, nil)
}

// workspaceGone reports whether a workspace path certainly no longer exists
// (an unmounted external volume is not proof of deletion).
func workspaceGone(ws string) bool {
	if !filepath.IsAbs(ws) {
		return false
	}
	_, err := os.Lstat(ws)
	if err == nil || !errors.Is(err, fs.ErrNotExist) {
		return false
	}
	if vol := volumeRoot(ws); vol != "" {
		if _, err := os.Stat(vol); err != nil {
			return false // volume not mounted: unknown
		}
	}
	// The parent chain must be readable to trust ENOENT.
	dir := filepath.Dir(ws)
	for dir != "/" && dir != "." {
		if _, err := os.Lstat(dir); err == nil {
			break
		} else if !errors.Is(err, fs.ErrNotExist) {
			return false
		}
		dir = filepath.Dir(dir)
	}
	return true
}

// volumeRoot returns "/Volumes/<name>" for paths on another volume.
func volumeRoot(p string) string {
	for _, prefix := range []string{"/Volumes/", "/System/Volumes/Data/Volumes/"} {
		if strings.HasPrefix(p, prefix) {
			rest := p[len(prefix):]
			if i := strings.IndexByte(rest, '/'); i >= 0 {
				rest = rest[:i]
			}
			return prefix + rest
		}
	}
	return ""
}

// projectRoot guesses the project folder from a workspace path
// (".../app/ios/App.xcworkspace" → ".../app").
func projectRoot(ws string) string {
	dir := filepath.Dir(ws)
	switch filepath.Base(dir) {
	case "ios", "macos", "apple":
		return filepath.Dir(dir)
	}
	return dir
}

// sortedKeys returns the keys of m in order.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
