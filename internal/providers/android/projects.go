package android

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
)

// projectInfo is what the Android projects under the user's roots (and AI
// worktrees) use: Gradle wrapper distributions, NDK / build-tools /
// compileSdk / CMake versions, SDK locations and Gradle JDKs.
type projectInfo struct {
	mu         sync.Mutex
	wrappers   map[string]map[string]bool // "gradle-9.3.1-bin" -> projects
	ndk        map[string]map[string]bool // "27.1.12297006" -> projects
	buildTools map[string]map[string]bool // "36.0.0" -> projects
	compileSdk map[string]map[string]bool // "36" -> projects
	cmake      map[string]map[string]bool // "3.22.1" -> projects
	sdkDirs    map[string]bool            // sdk.dir of local.properties
	javaHomes  map[string]bool            // org.gradle.java.home
	ndkPathRef bool                       // some project pins ndk.dir / ndkPath

	roots    int  // project roots walked
	complete bool // the walk finished (no cancellation, no cap)
}

func newProjectInfo() *projectInfo {
	return &projectInfo{
		wrappers: map[string]map[string]bool{}, ndk: map[string]map[string]bool{},
		buildTools: map[string]map[string]bool{}, compileSdk: map[string]map[string]bool{},
		cmake: map[string]map[string]bool{}, sdkDirs: map[string]bool{}, javaHomes: map[string]bool{},
	}
}

// known reports whether "unused" can be trusted: at least one root was walked
// completely.
func (pi *projectInfo) known() bool { return pi != nil && pi.roots > 0 && pi.complete }

func (pi *projectInfo) put(m map[string]map[string]bool, key, proj string) {
	if key == "" {
		return
	}
	pi.mu.Lock()
	defer pi.mu.Unlock()
	if m[key] == nil {
		m[key] = map[string]bool{}
	}
	m[key][proj] = true
}

func (pi *projectInfo) flag(set func()) {
	pi.mu.Lock()
	set()
	pi.mu.Unlock()
}

// gradleVersions returns the Gradle versions ("9.3.1") used by wrappers.
func (pi *projectInfo) gradleVersions() map[string][]string {
	out := map[string][]string{}
	for dist, projs := range pi.wrappers {
		if m := reDistName.FindStringSubmatch(dist); m != nil {
			out[m[1]] = append(out[m[1]], uniqSorted(projs)...)
		}
	}
	return out
}

const (
	projectWalkMaxDirs = 250_000
)

// skipDirs are never descended into while looking for Android projects.
var skipDirs = map[string]bool{
	"node_modules": true, "build": true, "Pods": true, "DerivedData": true, "dist": true,
	"target": true, "vendor": true, "ios": true, "coverage": true, "__pycache__": true,
	"venv": true, "Library": true, "tmp": true, "out": true, "intermediates": true,
	"bower_components": true, "jspm_packages": true, "site-packages": true,
}

// hiddenAllowed are dot-directories that may contain projects (worktrees).
var hiddenAllowed = map[string]bool{".claude": true, ".worktrees": true, ".claude-worktrees": true}

// scanProjects walks env.Roots and env.WorktreeRoots (bounded depth, heavy
// directories pruned) and records what Android projects use.
func (s *scan) scanProjects() *projectInfo {
	pi := newProjectInfo()
	var roots []string
	seen := map[string]bool{}
	for _, r := range append(append([]string(nil), s.env.Roots...), s.env.WorktreeRoots...) {
		r = s.env.Expand(r)
		if seen[r] || s.env.Excluded(r) {
			continue
		}
		seen[r] = true
		if fi, err := os.Stat(r); err == nil && fi.IsDir() {
			roots = append(roots, r)
		}
	}
	pi.roots = len(roots)
	if len(roots) == 0 {
		return pi
	}
	maxDepth := s.env.MaxDepth
	if maxDepth <= 0 {
		maxDepth = 8
	}
	ctx := s.ctx
	w := &projectWalker{s: s, pi: pi, ctx: ctx, maxDepth: maxDepth, sem: make(chan struct{}, 8)}
	for _, r := range roots {
		w.wg.Add(1)
		go func(r string) { defer w.wg.Done(); w.walk(r, 0) }(r)
	}
	w.wg.Wait()
	pi.complete = !w.truncated.Load() && ctx.Err() == nil
	if !pi.complete {
		s.logf("project walk incomplete (cancelled, unreadable, or %d dirs cap)", projectWalkMaxDirs)
	}
	return pi
}

type projectWalker struct {
	s         *scan
	pi        *projectInfo
	ctx       context.Context
	maxDepth  int
	sem       chan struct{}
	wg        sync.WaitGroup
	visited   sync.Map
	dirs      atomic.Int64
	truncated atomic.Bool
}

func (w *projectWalker) walk(dir string, depth int) {
	if w.ctx.Err() != nil {
		return
	}
	if _, dup := w.visited.LoadOrStore(dir, true); dup {
		return
	}
	if w.dirs.Add(1) > projectWalkMaxDirs {
		w.truncated.Store(true)
		return
	}
	if w.s.env.Excluded(dir) {
		return
	}
	ents, err := fsx.ReadDir(w.ctx, dir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			w.truncated.Store(true)
		}
		return
	}
	names := make(map[string]bool, len(ents))
	for _, e := range ents {
		names[e.Name()] = e.IsDir()
	}
	w.inspect(dir, names)
	if depth >= w.maxDepth {
		return
	}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		n := e.Name()
		if n == "node_modules" {
			w.inspectNodeModules(dir)
			continue
		}
		if skipDirs[n] || (strings.HasPrefix(n, ".") && !hiddenAllowed[n]) {
			continue
		}
		child := filepath.Join(dir, n)
		select {
		case w.sem <- struct{}{}:
			w.wg.Add(1)
			go func() {
				defer func() { <-w.sem; w.wg.Done() }()
				w.walk(child, depth+1)
			}()
		default:
			w.walk(child, depth+1)
		}
	}
}

// projectOf maps a directory holding Gradle files to the project root shown
// to the user (the parent of an "android" folder).
func projectOf(dir string) string {
	if filepath.Base(dir) == "android" {
		return filepath.Dir(dir)
	}
	if filepath.Base(dir) == "app" && filepath.Base(filepath.Dir(dir)) == "android" {
		return filepath.Dir(filepath.Dir(dir))
	}
	return dir
}

// inspect parses the interesting files of one directory. names maps entry
// name -> isDir.
func (w *projectWalker) inspect(dir string, names map[string]bool) {
	proj := projectOf(dir)
	pi := w.pi
	if names["gradle"] {
		if c := readSmall(filepath.Join(dir, "gradle", "wrapper", "gradle-wrapper.properties")); c != "" {
			if d := wrapperDist(c); d != "" {
				pi.put(pi.wrappers, d, proj)
			}
		}
	}
	for _, f := range []string{"build.gradle", "build.gradle.kts", "gradle.properties", "app.json", "app.config.js", "app.config.ts"} {
		if isDir, ok := names[f]; ok && !isDir {
			w.versions(readSmall(filepath.Join(dir, f)), proj)
		}
	}
	if isDir, ok := names["gradle.properties"]; ok && !isDir {
		props := parseProperties(readSmall(filepath.Join(dir, "gradle.properties")))
		if v := props["org.gradle.java.home"]; filepath.IsAbs(v) {
			pi.flag(func() { pi.javaHomes[filepath.Clean(v)] = true })
		}
	}
	if isDir, ok := names["local.properties"]; ok && !isDir {
		props := parseProperties(readSmall(filepath.Join(dir, "local.properties")))
		if v := props["sdk.dir"]; filepath.IsAbs(v) {
			pi.flag(func() { pi.sdkDirs[filepath.Clean(v)] = true })
		}
		if props["ndk.dir"] != "" {
			pi.flag(func() { pi.ndkPathRef = true })
		}
	}
	// RN / Expo project whose node_modules is gone: use the RN default NDK.
	if _, hasPkg := names["package.json"]; hasPkg && names["android"] {
		if _, hasNM := names["node_modules"]; !hasNM {
			if v := rnVersionFromPackageJSON(readSmall(filepath.Join(dir, "package.json"))); v != "" {
				pi.put(pi.ndk, rnDefaultNDK(v), dir)
			}
		}
	}
}

// inspectNodeModules reads only react-native's version catalog (the NDK,
// build-tools and compileSdk an RN app builds with); libraries' own Gradle
// files and wrappers inside node_modules are never used for app builds.
func (w *projectWalker) inspectNodeModules(dir string) {
	rn := filepath.Join(dir, "node_modules", "react-native")
	if c := readSmall(filepath.Join(rn, "gradle", "libs.versions.toml")); c != "" {
		w.versions(c, dir)
		return
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if json.Unmarshal([]byte(readSmall(filepath.Join(rn, "package.json"))), &pkg) == nil && pkg.Version != "" {
		w.pi.put(w.pi.ndk, rnDefaultNDK(pkg.Version), dir)
	}
}

var (
	reDistURL   = regexp.MustCompile(`(?m)^\s*distributionUrl\s*[=:]\s*(\S+)`)
	reDistName  = regexp.MustCompile(`^gradle-(.+)-(bin|all)$`)
	reNdkVer    = regexp.MustCompile(`\bndkVersion\b[^\n]{0,120}?(\d+\.\d+\.\d+(?:\.\d+)?)`)
	reBTVer     = regexp.MustCompile(`\bbuildTools(?:Version)?\b[^\n]{0,120}?(\d+\.\d+\.\d+(?:-rc\d+)?)`)
	reSdkVer    = regexp.MustCompile(`\bcompileSdk(?:Version)?\b[^\n\d]{0,120}?(\d{2,3})\b`)
	reCMake     = regexp.MustCompile(`(?s)cmake\s*\{[^}]*?\bversion\s*[=:]?\s*["']([\d.]+)["']`)
	reCMakeProp = regexp.MustCompile(`(?m)^\s*android\.cmakeVersion\s*=\s*([\d.]+)`)
	reNdkPath   = regexp.MustCompile(`\bndkPath\b|\bndk\.dir\b`)
)

// wrapperDist returns the wrapper dists directory name ("gradle-9.3.1-bin")
// of a gradle-wrapper.properties content.
func wrapperDist(content string) string {
	m := reDistURL.FindStringSubmatch(content)
	if m == nil {
		return ""
	}
	u := unescapeProp(m[1])
	base := path.Base(u)
	base = strings.TrimSuffix(base, ".zip")
	if !reDistName.MatchString(base) {
		return ""
	}
	return base
}

// versions extracts NDK / build-tools / compileSdk / CMake versions from a
// Gradle build script, gradle.properties, app.json/app.config.* (expo-build-
// properties) or RN's libs.versions.toml: the first literal following each
// key on the same line. Tolerant: over-matching only keeps more things.
func (w *projectWalker) versions(content, proj string) {
	if content == "" {
		return
	}
	pi := w.pi
	for _, m := range reNdkVer.FindAllStringSubmatch(content, -1) {
		pi.put(pi.ndk, m[1], proj)
	}
	for _, m := range reBTVer.FindAllStringSubmatch(content, -1) {
		pi.put(pi.buildTools, m[1], proj)
	}
	for _, m := range reSdkVer.FindAllStringSubmatch(content, -1) {
		pi.put(pi.compileSdk, m[1], proj)
	}
	for _, m := range reCMake.FindAllStringSubmatch(content, -1) {
		pi.put(pi.cmake, m[1], proj)
	}
	for _, m := range reCMakeProp.FindAllStringSubmatch(content, -1) {
		pi.put(pi.cmake, m[1], proj)
	}
	if reNdkPath.MatchString(content) {
		pi.flag(func() { pi.ndkPathRef = true })
	}
}

// parseProperties parses a Java .properties file (enough for local.properties
// and gradle.properties: key=value, # comments, escaped colons).
func parseProperties(content string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(content, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || t[0] == '#' || t[0] == '!' {
			continue
		}
		i := strings.IndexAny(t, "=:")
		if i <= 0 {
			continue
		}
		// An escaped separator belongs to the key (rare): skip those lines.
		if t[i-1] == '\\' {
			continue
		}
		out[strings.TrimSpace(t[:i])] = unescapeProp(strings.TrimSpace(t[i+1:]))
	}
	return out
}

func unescapeProp(v string) string {
	r := strings.NewReplacer(`\:`, `:`, `\=`, `=`, `\\`, `\`, `\ `, ` `)
	return r.Replace(v)
}

// rnVersionFromPackageJSON returns the react-native dependency version range
// ("0.76.3", "~0.74.5"...) of a package.json.
func rnVersionFromPackageJSON(content string) string {
	var pkg struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if json.Unmarshal([]byte(content), &pkg) != nil {
		return ""
	}
	if v := pkg.Dependencies["react-native"]; v != "" {
		return v
	}
	return pkg.DevDependencies["react-native"]
}

var reRNMinor = regexp.MustCompile(`0\.(\d+)`)

// rnDefaultNDK is the NDK React Native builds with by default, per minor
// version (from RN's gradle/libs.versions.toml history).
func rnDefaultNDK(version string) string {
	m := reRNMinor.FindStringSubmatch(version)
	if m == nil {
		return ""
	}
	minor := atoi(m[1])
	switch {
	case minor >= 76:
		return "27.1.12297006"
	case minor >= 74:
		return "26.1.10909125"
	case minor == 73:
		return "25.1.8937393"
	case minor >= 71:
		return "23.1.7779620"
	case minor >= 68:
		return "21.4.7075529"
	}
	return ""
}
