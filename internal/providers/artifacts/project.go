package artifacts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
)

// projectMarkers identify the root of a (non-git) project.
var projectMarkers = []string{
	"package.json", "pyproject.toml", "setup.py", "requirements.txt", "Cargo.toml", "go.mod", "Gemfile",
	"pubspec.yaml", "Package.swift", "*.xcodeproj", "mix.exs", "pom.xml", "build.sbt", "settings.gradle",
	"settings.gradle.kts", "build.gradle", "build.gradle.kts", "Podfile", "CMakeLists.txt", "build.zig",
	"stack.yaml", "cabal.project", "*.csproj", "deno.json",
}

// platformDirs hold the native part of a cross-platform app.
var platformDirs = map[string]bool{"android": true, "ios": true, "macos": true, "windows": true, "tvos": true, "visionos": true, "linux": true, "src-tauri": true, "electron": true}

// appMarkers identify a cross-platform app root above a platform folder.
var appMarkers = []string{"package.json", "app.json", "app.config.*", "pubspec.yaml"}

// climbPlatform goes from a native folder (android/app, ios...) to the app
// that owns it when there is one.
func climbPlatform(d string) string {
	if filepath.Base(d) == "app" && filepath.Base(filepath.Dir(d)) == "android" {
		d = filepath.Dir(d)
	}
	if platformDirs[filepath.Base(d)] {
		up := filepath.Dir(d)
		if _, ok := matchAny(appMarkers, dirNames(up)); ok {
			return up
		}
	}
	return d
}

// projectOf returns the project root of c (the git work tree when there is
// one) and the "package" directory the artifact belongs to (the app or
// workspace package, used for types, package managers and notes).
func (s *scan) projectOf(c *cand) (project, pkg string) {
	pkg = c.parent
	if len(c.rule.Markers) == 0 {
		pkg = s.nearestProject(c.parent, c.root.path)
	} else {
		pkg = climbPlatform(pkg)
	}
	if c.git != nil && fsx.Within(pkg, c.git.path) {
		return c.git.path, pkg
	}
	if c.git != nil && fsx.Within(c.path, c.git.path) {
		return c.git.path, c.git.path
	}
	return pkg, pkg
}

// nearestProject walks up from dir (to stop, inclusive) to the first
// directory holding a project marker; when none, dir itself.
func (s *scan) nearestProject(dir, stop string) string {
	for d := dir; fsx.Within(d, stop); d = filepath.Dir(d) {
		if _, ok := matchAny(projectMarkers, dirNames(d)); ok {
			return d
		}
		if d == stop {
			break
		}
	}
	return dir
}

// projInfo is cached per directory.
type projInfo struct {
	activity time.Time
	pkg      *packageJSON
	names    map[string]bool
}

type packageJSON struct {
	Name             string            `json:"name"`
	Main             any               `json:"main"`
	Module           any               `json:"module"`
	Types            any               `json:"types"`
	Typings          any               `json:"typings"`
	Exports          any               `json:"exports"`
	Bin              any               `json:"bin"`
	Files            []string          `json:"files"`
	PackageManager   string            `json:"packageManager"`
	Workspaces       any               `json:"workspaces"`
	Dependencies     map[string]string `json:"dependencies"`
	DevDependencies  map[string]string `json:"devDependencies"`
	PeerDependencies map[string]string `json:"peerDependencies"`
}

func (p *packageJSON) has(dep string) bool {
	if p == nil {
		return false
	}
	_, a := p.Dependencies[dep]
	_, b := p.DevDependencies[dep]
	_, c := p.PeerDependencies[dep]
	return a || b || c
}

// info returns (and caches) the facts about directory d.
func (s *scan) info(d string) *projInfo {
	s.projMu.Lock()
	if pi, ok := s.projects[d]; ok {
		s.projMu.Unlock()
		return pi
	}
	s.projMu.Unlock()

	pi := &projInfo{names: map[string]bool{}}
	if ents, err := os.ReadDir(d); err == nil {
		for _, e := range ents {
			n := e.Name()
			pi.names[n] = true
			if s.rs.names[n] || n == ".git" || n == ".DS_Store" || strings.HasPrefix(n, "._") || n == "Icon\r" {
				continue
			}
			if fi, err := e.Info(); err == nil && fi.ModTime().After(pi.activity) {
				pi.activity = fi.ModTime()
			}
		}
	}
	if pi.names["package.json"] {
		if b, err := readSmall(filepath.Join(d, "package.json"), 2<<20); err == nil {
			var pj packageJSON
			if json.Unmarshal(b, &pj) == nil {
				pi.pkg = &pj
			}
		}
	}
	s.projMu.Lock()
	defer s.projMu.Unlock()
	if old, ok := s.projects[d]; ok {
		return old
	}
	s.projects[d] = pi
	return pi
}

// activity is the last activity of the project (NOT of the artifact): the
// newest mtime among the non-artifact top-level entries of the project and
// package folders (sources, lockfiles, configs) and the git index / HEAD.
func (s *scan) activity(c *cand, project, pkg string) time.Time {
	t := s.info(project).activity
	for d := pkg; d != project && fsx.Within(d, project); d = filepath.Dir(d) {
		if a := s.info(d).activity; a.After(t) {
			t = a
		}
	}
	if c.git != nil {
		if gi := c.git.indexTime(); gi.After(t) {
			t = gi
		}
	}
	if t.After(s.now) {
		t = s.now
	}
	return t
}

// projectType guesses what kind of project pkg (then project) is.
func (s *scan) projectType(pkg, project string) string {
	for _, d := range []string{pkg, project} {
		if t := s.typeOf(d); t != "" {
			return t
		}
	}
	return ""
}

func (s *scan) typeOf(d string) string {
	pi := s.info(d)
	n := pi.names
	if pi.pkg != nil {
		switch {
		case pi.pkg.has("expo"):
			return "expo"
		case pi.pkg.has("react-native"):
			return "react-native"
		case n["ios"] && n["android"]:
			return "react-native"
		case pi.pkg.has("next"):
			return "next"
		case pi.pkg.has("nuxt"):
			return "nuxt"
		case pi.pkg.has("@sveltejs/kit"):
			return "sveltekit"
		case pi.pkg.has("astro"):
			return "astro"
		case pi.pkg.has("electron"):
			return "electron"
		case pi.pkg.has("vite"):
			return "vite"
		}
		return "node"
	}
	has := func(globs ...string) bool { _, ok := matchAny(globs, n); return ok }
	switch {
	case has("pubspec.yaml"):
		return "flutter"
	case has("Cargo.toml"):
		return "rust"
	case has("Package.swift"):
		return "swift"
	case has("*.xcodeproj", "*.xcworkspace", "Podfile"):
		return "ios"
	case has("settings.gradle", "settings.gradle.kts", "build.gradle", "build.gradle.kts", "gradlew"):
		if filepath.Base(d) == "android" || n["AndroidManifest.xml"] || n["app"] {
			return "android"
		}
		return "gradle"
	case has(pyMarkers...):
		return "python"
	case has("go.mod"):
		return "go"
	case has("Gemfile"):
		return "ruby"
	case has("pom.xml", "build.sbt"):
		return "jvm"
	case has("mix.exs"):
		return "elixir"
	case has("build.zig"):
		return "zig"
	case has("stack.yaml", "cabal.project", "*.cabal"):
		return "haskell"
	case has("*.csproj", "*.fsproj"):
		return "dotnet"
	case has("CMakeLists.txt"):
		return "cmake"
	}
	return ""
}

// pkgManager finds the JS package manager of pkg (walking up to project) and
// the command that reinstalls node_modules.
func (s *scan) pkgManager(pkg, project string) (pm, cmd, dir string) {
	for d := pkg; ; d = filepath.Dir(d) {
		n := s.info(d).names
		switch {
		case n["bun.lock"] || n["bun.lockb"]:
			return "bun", "bun i", d
		case n["pnpm-lock.yaml"]:
			return "pnpm", "pnpm i", d
		case n["yarn.lock"]:
			if n[".yarnrc.yml"] {
				return "yarn-berry", "yarn", d
			}
			return "yarn", "yarn", d
		case n["package-lock.json"] || n["npm-shrinkwrap.json"]:
			return "npm", "npm ci", d
		}
		if d == project || !fsx.Within(d, project) || d == "/" {
			break
		}
	}
	for _, d := range []string{pkg, project} {
		if pj := s.info(d).pkg; pj != nil && pj.PackageManager != "" {
			name := strings.SplitN(pj.PackageManager, "@", 2)[0]
			switch name {
			case "pnpm":
				return "pnpm", "pnpm i", d
			case "yarn":
				return "yarn", "yarn", d
			case "bun":
				return "bun", "bun i", d
			case "npm":
				return "npm", "npm install", d
			}
		}
	}
	return "", "npm install", pkg
}

// libraryOutput reports whether package.json of dir points into the build
// folder name (main/module/types/exports/bin/files): a library output that
// linked apps need.
func (s *scan) libraryOutput(dir, name string) bool {
	pj := s.info(dir).pkg
	if pj == nil {
		return false
	}
	var refs []string
	collectStrings(pj.Main, &refs)
	collectStrings(pj.Module, &refs)
	collectStrings(pj.Types, &refs)
	collectStrings(pj.Typings, &refs)
	collectStrings(pj.Exports, &refs)
	collectStrings(pj.Bin, &refs)
	refs = append(refs, pj.Files...)
	for _, r := range refs {
		r = strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(r)), "./")
		if r == name || strings.HasPrefix(r, name+"/") {
			return true
		}
	}
	return false
}

func collectStrings(v any, out *[]string) {
	switch t := v.(type) {
	case string:
		*out = append(*out, t)
	case []any:
		for _, x := range t {
			collectStrings(x, out)
		}
	case map[string]any:
		for _, x := range t {
			collectStrings(x, out)
		}
	}
}
