package jsdev

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
)

const (
	pinMaxDepth = 3     // root/a/b/c
	pinMaxDirs  = 40000 // hard bound on directories read
)

// pinRef is a Node version requested by a project file.
type pinRef struct {
	spec   spec
	source string // file that requests it
}

// projectInfo is what a shallow walk of the project roots tells us.
type projectInfo struct {
	pins []pinRef
	// pnp lists projects using Yarn Plug'n'Play (.pnp.cjs): they read the
	// global Yarn cache at runtime.
	pnp []string
	// expoSDK maps an Expo SDK major to the projects depending on it.
	expoSDK map[int][]string
}

// pinDirSkip are directory names never descended into.
var pinDirSkip = map[string]bool{
	"node_modules": true, "Pods": true, "build": true, "dist": true, "DerivedData": true,
	"target": true, "vendor": true, "Library": true, "Applications": true, "Movies": true,
	"Music": true, "Pictures": true,
}

// loadProjects walks env.Roots and env.WorktreeRoots (≤ pinMaxDepth levels)
// looking for .nvmrc, .node-version, .tool-versions and .pnp.cjs files.
func loadProjects(ctx context.Context, env *core.Env) *projectInfo {
	info := &projectInfo{expoSDK: map[int][]string{}}
	seen := map[string]bool{}
	budget := pinMaxDirs
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if ctx.Err() != nil || budget <= 0 || seen[dir] || env.Excluded(dir) {
			return
		}
		seen[dir] = true
		budget--
		ents, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		var subdirs []string
		for _, e := range ents {
			name := e.Name()
			if e.IsDir() {
				if depth < pinMaxDepth && !strings.HasPrefix(name, ".") && !pinDirSkip[name] {
					subdirs = append(subdirs, filepath.Join(dir, name))
				}
				continue
			}
			switch name {
			case ".nvmrc", ".node-version":
				p := filepath.Join(dir, name)
				if data, err := readSmall(p); err == nil {
					if line := firstLine(data); line != "" {
						info.pins = append(info.pins, pinRef{spec: parseSpec(line), source: p})
					}
				}
			case ".tool-versions":
				p := filepath.Join(dir, name)
				if data, err := readSmall(p); err == nil {
					for _, s := range toolVersionsNode(data) {
						info.pins = append(info.pins, pinRef{spec: s, source: p})
					}
				}
			case ".pnp.cjs", ".pnp.js":
				info.pnp = append(info.pnp, dir)
			case "package.json":
				if data, err := readSmall(filepath.Join(dir, name)); err == nil {
					if sdk := expoSDKOf(data); sdk > 0 {
						info.expoSDK[sdk] = append(info.expoSDK[sdk], dir)
					}
				}
			}
		}
		for _, sd := range subdirs {
			walk(sd, depth+1)
		}
	}
	roots := append(append([]string{}, env.Roots...), env.WorktreeRoots...)
	for _, r := range roots {
		if r != "" && r != env.Home {
			walk(filepath.Clean(r), 0)
		}
	}
	// Pins right in the home directory (without descending into it).
	for _, name := range []string{".nvmrc", ".node-version"} {
		p := filepath.Join(env.Home, name)
		if data, err := readSmall(p); err == nil {
			if line := firstLine(data); line != "" {
				info.pins = append(info.pins, pinRef{spec: parseSpec(line), source: p})
			}
		}
	}
	return info
}

// readSmall reads a small config file (refuses anything above 64 KiB).
func readSmall(p string) (string, error) {
	fi, err := os.Stat(p)
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() || fi.Size() > 64<<10 {
		return "", os.ErrInvalid
	}
	b, err := os.ReadFile(p)
	return string(b), err
}

// firstLine returns the first non-empty, non-comment line.
func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(l)
		if l != "" && !strings.HasPrefix(l, "#") {
			return l
		}
	}
	return ""
}

// toolVersionsNode extracts node versions from a .tool-versions file
// ("nodejs 20.11.1" for asdf, "node 20" for mise; several versions allowed).
func toolVersionsNode(data string) []spec {
	var out []spec
	for _, l := range strings.Split(data, "\n") {
		if i := strings.IndexByte(l, '#'); i >= 0 {
			l = l[:i]
		}
		f := strings.Fields(l)
		if len(f) < 2 || (f[0] != "nodejs" && f[0] != "node") {
			continue
		}
		for _, v := range f[1:] {
			if sp := parseSpec(v); sp.kind != specNone {
				out = append(out, sp)
			}
		}
	}
	return out
}

var leadingInt = regexp.MustCompile(`^[\^~>=<v ]*([0-9]+)`)

// expoSDKOf returns the major of the "expo" dependency of a package.json (0 if none).
func expoSDKOf(data string) int {
	var pj struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if json.Unmarshal([]byte(data), &pj) != nil {
		return 0
	}
	v, ok := pj.Dependencies["expo"]
	if !ok {
		v = pj.DevDependencies["expo"]
	}
	m := leadingInt.FindStringSubmatch(v)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}
