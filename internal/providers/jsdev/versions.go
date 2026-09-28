package jsdev

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// version is an installed Node.js version (always a full x.y.z triple).
type version struct {
	Major, Minor, Patch int
}

func (v version) String() string { return fmt.Sprintf("v%d.%d.%d", v.Major, v.Minor, v.Patch) }

func (v version) less(o version) bool {
	if v.Major != o.Major {
		return v.Major < o.Major
	}
	if v.Minor != o.Minor {
		return v.Minor < o.Minor
	}
	return v.Patch < o.Patch
}

// parseVersion parses "v18.20.4" / "18.20.4" (an installed version directory name).
func parseVersion(s string) (version, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return version{}, false
	}
	var n [3]int
	for i, p := range parts {
		x, err := strconv.Atoi(p)
		if err != nil || x < 0 {
			return version{}, false
		}
		n[i] = x
	}
	return version{n[0], n[1], n[2]}, true
}

type specKind int

const (
	specNone      specKind = iota // "system", unparseable: references nothing
	specExact                     // 20.19.5
	specPrefix                    // 20, 20.19
	specNewest                    // node, stable, latest
	specNewestLTS                 // lts/*
)

// spec is a version request as found in .nvmrc, .node-version, .tool-versions,
// nvm aliases or mise/asdf/volta configs.
type spec struct {
	kind  specKind
	parts []int
	raw   string
}

// ltsCodenames maps Node LTS codenames to their major version.
var ltsCodenames = map[string]int{
	"argon": 4, "boron": 6, "carbon": 8, "dubnium": 10, "erbium": 12, "fermium": 14,
	"gallium": 16, "hydrogen": 18, "iron": 20, "jod": 22, "krypton": 24,
}

// parseSpec understands the formats accepted by nvm, fnm, mise, asdf and volta.
// Semver ranges are reduced to their major version (conservative: it keeps
// the newest installed version of that major).
func parseSpec(raw string) spec {
	s := strings.ToLower(strings.TrimSpace(raw))
	sp := spec{raw: strings.TrimSpace(raw)}
	if i := strings.IndexAny(s, " \t#"); i >= 0 {
		s = s[:i]
	}
	switch s {
	case "", "system", "iojs", "io.js", "none":
		return sp
	case "node", "stable", "latest", "current", "newest":
		sp.kind = specNewest
		return sp
	case "lts/*", "lts", "lts/latest", "lts-latest", "lts/-1":
		sp.kind = specNewestLTS
		return sp
	}
	for _, pre := range []string{"lts/", "lts-"} {
		if strings.HasPrefix(s, pre) {
			if m, ok := ltsCodenames[strings.TrimPrefix(s, pre)]; ok {
				sp.kind, sp.parts = specPrefix, []int{m}
				return sp
			}
			sp.kind = specNewestLTS // unknown (future) codename: be conservative
			return sp
		}
	}
	ranged := strings.TrimLeft(s, "^~>=<")
	isRange := ranged != s
	s = strings.TrimPrefix(ranged, "v")
	for strings.HasSuffix(s, ".x") || strings.HasSuffix(s, ".*") {
		s = s[:len(s)-2]
	}
	if s == "x" || s == "*" {
		sp.kind = specNewest
		return sp
	}
	fields := strings.Split(s, ".")
	if len(fields) > 3 {
		return sp
	}
	for _, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil || n < 0 {
			return spec{raw: sp.raw}
		}
		sp.parts = append(sp.parts, n)
	}
	switch {
	case isRange:
		sp.kind, sp.parts = specPrefix, sp.parts[:1]
	case len(sp.parts) == 3:
		sp.kind = specExact
	default:
		sp.kind = specPrefix
	}
	return sp
}

func (sp spec) matches(v version) bool {
	switch sp.kind {
	case specExact, specPrefix:
		vals := []int{v.Major, v.Minor, v.Patch}
		for i, p := range sp.parts {
			if vals[i] != p {
				return false
			}
		}
		return true
	case specNewest:
		return true
	case specNewestLTS:
		return v.Major >= 4 && v.Major%2 == 0
	}
	return false
}

// best returns the index of the newest version matching sp (the one nvm/fnm
// would pick), or -1.
func (sp spec) best(vs []version) int {
	idx := -1
	for i, v := range vs {
		if sp.matches(v) && (idx < 0 || vs[idx].less(v)) {
			idx = i
		}
	}
	return idx
}

// bestOrMajor is best, falling back to the newest version of the same major
// when an exact version is not installed (conservative for defaults and pins).
func (sp spec) bestOrMajor(vs []version) int {
	if i := sp.best(vs); i >= 0 || sp.kind != specExact {
		return i
	}
	return spec{kind: specPrefix, parts: sp.parts[:1]}.best(vs)
}

// sortedKeys returns the keys of m, sorted.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
