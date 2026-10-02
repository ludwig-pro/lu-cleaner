package aitools

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
)

// keepPolicy says which version is kept besides the symlinked / pinned /
// running ones.
type keepPolicy int

const (
	keepNewestMtime    keepPolicy = iota // updaters pre-download the next version before flipping the link
	keepHighestVersion                   // highest version number
)

// versionSet describes a "versions/<v>" directory where only some versions
// are live (the target of a bin symlink, a pinned version, the running ones).
type versionSet struct {
	kind  string
	label string // "Claude Code CLI"
	cat   core.Category
	dir   string
	// links are symlinks whose target (inside dir) selects the active version.
	links []string
	// pinFiles contain the name of a version that must be kept (e.g. .sdk-version).
	pinFiles []string
	keep     keepPolicy
	// match filters version entry names (nil = every non-hidden entry).
	match       func(name string) bool
	risk        core.Risk
	recommended bool
	guard       []string
	note        string
}

// versionedBinaries evaluates every known versions/ directory.
func (s *scanner) versionedBinaries() {
	cursorAgentCLI := s.appSupport("Cursor/User/globalStorage/anysphere.cursor-agent-worker/agent-cli")
	conductor := s.appSupport("com.conductor.app")
	sets := []versionSet{
		{
			kind: "claude-code-old-version", label: "Claude Code CLI", cat: core.CatAI,
			dir:   s.home(".local/share/claude/versions"),
			links: []string{s.home(".local/bin/claude")},
			keep:  keepNewestMtime, risk: core.RiskModerate, recommended: true,
			note: "Superseded Claude Code native build (~200 MB each); only the one ~/.local/bin/claude points to (and the newest pre-downloaded one) is kept. The updater re-downloads if needed.",
		},
		{
			kind: "claude-desktop-old-claude-code", label: "Claude desktop · bundled Claude Code", cat: core.CatAI,
			dir:  s.appSupport("Claude/claude-code"),
			keep: keepHighestVersion, risk: core.RiskSafe, recommended: true,
			note: "Old copy of Claude Code downloaded by the Claude desktop app for its Code sessions; the app only runs the newest one and re-downloads what it needs.",
		},
		{
			kind: "claude-desktop-old-claude-code-vm", label: "Claude desktop · VM Claude Code", cat: core.CatAI,
			dir:      s.appSupport("Claude/claude-code-vm"),
			pinFiles: []string{s.appSupport("Claude/claude-code-vm/.sdk-version")},
			keep:     keepHighestVersion, risk: core.RiskSafe, recommended: true,
			note: "Old Claude Code build for the Cowork VM; the version named in .sdk-version and the newest are kept, the app re-downloads what it needs.",
		},
		{
			kind: "cursor-agent-bundled-old-version", label: "Cursor · bundled cursor-agent", cat: core.CatAI,
			dir:   filepath.Join(cursorAgentCLI, ".local/share/cursor-agent/versions"),
			links: []string{filepath.Join(cursorAgentCLI, ".local/bin/cursor-agent")},
			keep:  keepNewestMtime, risk: core.RiskSafe, recommended: true,
			note: "Old cursor-agent CLI build kept by the Cursor IDE agent worker (200-600 MB each); only the version its bin/cursor-agent symlink targets is used.",
		},
		{
			kind: "cursor-agent-old-version", label: "cursor-agent CLI", cat: core.CatAI,
			dir:   s.home(".local/share/cursor-agent/versions"),
			links: []string{s.home(".local/bin/cursor-agent"), s.home(".local/bin/agent")},
			keep:  keepNewestMtime, risk: core.RiskSafe, recommended: true,
			note: "Superseded standalone cursor-agent build; only the one ~/.local/bin/cursor-agent points to is used (`cursor-agent update` re-downloads).",
		},
		{
			kind: "cursor-origin-old-version", label: "Cursor origin CLI", cat: core.CatAI,
			dir:   s.home(".local/share/cursor/origin"),
			links: []string{s.home(".local/bin/origin")},
			keep:  keepNewestMtime, risk: core.RiskSafe, recommended: true,
			note: "Superseded Cursor `origin` CLI build; only the one ~/.local/bin/origin points to is used.",
		},
		{
			kind: "conductor-old-agent-binary", label: "Conductor · bundled Claude Code", cat: core.CatAI,
			dir:   filepath.Join(conductor, "agent-binaries/claude"),
			links: []string{filepath.Join(conductor, "bin/claude")},
			keep:  keepNewestMtime, risk: core.RiskSafe, recommended: true, guard: procConductor,
			note: "Old Claude Code binary downloaded by Conductor; only the version bin/claude points to is used, Conductor re-downloads its pinned version.",
		},
		{
			kind: "conductor-old-agent-binary", label: "Conductor · bundled Codex", cat: core.CatAI,
			dir:   filepath.Join(conductor, "agent-binaries/codex"),
			links: []string{filepath.Join(conductor, "bin/codex"), filepath.Join(conductor, "bin/codex-code-mode-host")},
			keep:  keepNewestMtime, risk: core.RiskSafe, recommended: true, guard: procConductor,
			note: "Old Codex binary downloaded by Conductor; only the version bin/codex points to is used, Conductor re-downloads its pinned version.",
		},
		{
			kind: "copilot-cli-old-version", label: "GitHub Copilot CLI", cat: core.CatAI,
			dir:   s.home(".copilot/pkg/universal"),
			links: []string{s.home(".local/bin/copilot")},
			keep:  keepNewestMtime, risk: core.RiskSafe, recommended: true,
			note: "Superseded Copilot CLI package; the auto-updater only runs the newest one.",
		},
		{
			kind: "vibe-kanban-old-version", label: "vibe-kanban", cat: core.CatAI,
			dir:   s.home(".vibe-kanban/bin"),
			match: func(n string) bool { return strings.HasPrefix(n, "v") },
			keep:  keepNewestMtime, risk: core.RiskSafe, recommended: true,
			note: "Old vibe-kanban binary downloaded by npx; the newest one is kept and npx re-downloads on demand.",
		},
	}
	for _, v := range sets {
		if s.ctx.Err() != nil {
			return
		}
		s.versionSet(v)
	}
}

func (s *scanner) versionSet(v versionSet) {
	if !s.usable(v.dir) {
		return
	}
	var versions []entry
	var tmp []entry
	for _, e := range list(s.ctx, v.dir, true) {
		switch {
		case strings.HasPrefix(e.name, ".tmp"):
			tmp = append(tmp, e) // interrupted downloads
		case strings.HasPrefix(e.name, "."):
		case v.match != nil && !v.match(e.name):
		default:
			versions = append(versions, e)
		}
	}
	if len(versions) == 0 {
		return
	}
	keep := map[string]string{} // name -> reason
	for _, l := range v.links {
		if name := versionFromLink(l, v.dir); name != "" {
			keep[name] = "current (" + s.env.Pretty(l) + ")"
		}
	}
	for _, f := range v.pinFiles {
		if data, err := fsx.ReadFile(s.ctx, f); err == nil {
			if name := strings.TrimSpace(string(data)); name != "" {
				keep[name] = "pinned (" + filepath.Base(f) + ")"
			}
		}
	}
	n := s.env.KeepLatest // config keep_latest (>= 1)
	if n < 1 {
		n = 1
	}
	for _, name := range pickKeepN(versions, v.keep, n) {
		if _, ok := keep[name]; !ok {
			keep[name] = "newest"
		}
	}
	for _, e := range versions {
		if _, ok := keep[e.name]; !ok && s.runningWithin(e.path) {
			keep[e.name] = "running"
		}
	}
	kept := sortedKeys(keep)
	for _, e := range versions {
		if _, ok := keep[e.name]; ok {
			continue
		}
		it := s.newItem(v.kind, v.cat, v.label+" · "+e.name+" (old version)", v.risk)
		it.ID = itemID(v.kind, e.path)
		it.Path = e.path
		it.LastUsed = e.mtime
		it.Recommended = v.recommended
		it.ProcessGuard = v.guard
		it.Note = v.note
		it.Meta = map[string]string{"version": e.name, "kept": strings.Join(kept, ", ")}
		s.publish(it, pubOpts{placeholder: true})
	}
	for _, e := range tmp {
		if s.now.Sub(e.mtime) < tempLeftoverMinAge {
			continue
		}
		it := s.newItem(v.kind, v.cat, v.label+" · interrupted download "+e.name, core.RiskSafe)
		it.ID = itemID(v.kind, e.path)
		it.Path = e.path
		it.LastUsed = e.mtime
		it.Recommended = true
		it.ProcessGuard = v.guard
		it.Note = "Leftover of an interrupted version download; never used."
		s.publish(it, pubOpts{})
	}
}

// versionFromLink returns the first path component, below dir, of the
// symlink's target ("" when link is missing or points elsewhere).
func versionFromLink(link, dir string) string {
	t, err := os.Readlink(link)
	if err != nil {
		return ""
	}
	if !filepath.IsAbs(t) {
		t = filepath.Join(filepath.Dir(link), t)
	}
	t = filepath.Clean(t)
	if !fsx.Within(t, dir) || t == dir {
		// the link or the versions dir may sit behind another symlink
		rt, err1 := filepath.EvalSymlinks(link)
		rd, err2 := filepath.EvalSymlinks(dir)
		if err1 != nil || err2 != nil || !fsx.Within(rt, rd) || rt == rd {
			return ""
		}
		t, dir = rt, rd
	}
	rel, err := filepath.Rel(dir, t)
	if err != nil {
		return ""
	}
	return strings.SplitN(rel, string(filepath.Separator), 2)[0]
}

func pickKeep(versions []entry, policy keepPolicy) string {
	if best := pickKeepN(versions, policy, 1); len(best) == 1 {
		return best[0]
	}
	return ""
}

// pickKeepN returns the names of the n best versions according to policy.
func pickKeepN(versions []entry, policy keepPolicy, n int) []string {
	vs := append([]entry(nil), versions...)
	switch policy {
	case keepHighestVersion:
		sort.Slice(vs, func(i, j int) bool { return compareVersions(vs[i].name, vs[j].name) > 0 })
	default:
		sort.Slice(vs, func(i, j int) bool {
			if !vs[i].mtime.Equal(vs[j].mtime) {
				return vs[i].mtime.After(vs[j].mtime)
			}
			return compareVersions(vs[i].name, vs[j].name) > 0
		})
	}
	var out []string
	for i := 0; i < n && i < len(vs); i++ {
		out = append(out, vs[i].name)
	}
	return out
}

// compareVersions compares dotted/dashed version strings numerically field by
// field ("2.1.10" > "2.1.9", "2026.09.18-9a7762b" > "2026.08.31-4057e58").
func compareVersions(a, b string) int {
	fa, fb := versionFields(a), versionFields(b)
	for i := 0; i < len(fa) && i < len(fb); i++ {
		x, y := fa[i], fb[i]
		nx, errx := strconv.ParseUint(x, 10, 64)
		ny, erry := strconv.ParseUint(y, 10, 64)
		switch {
		case errx == nil && erry == nil:
			if nx != ny {
				if nx > ny {
					return 1
				}
				return -1
			}
		case errx == nil: // numbers sort after words (1.0.0 > 1.0.0-beta)
			return 1
		case erry == nil:
			return -1
		default:
			if c := strings.Compare(x, y); c != 0 {
				return c
			}
		}
	}
	switch {
	case len(fa) == len(fb):
		return 0
	case len(fa) > len(fb): // "1.2.3.1" > "1.2.3" but "1.2.3-beta" < "1.2.3"
		if _, err := strconv.ParseUint(fa[len(fb)], 10, 64); err == nil {
			return 1
		}
		return -1
	default:
		if _, err := strconv.ParseUint(fb[len(fa)], 10, 64); err == nil {
			return -1
		}
		return 1
	}
}

func versionFields(v string) []string {
	v = strings.TrimPrefix(strings.TrimPrefix(v, "v"), "V")
	return strings.FieldsFunc(v, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}
