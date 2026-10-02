package jsdev

import (
	"context"
	"encoding/json"
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

// ------------------------------------------------------------------ npx

const npxStaleAfter = 14 * 24 * time.Hour

// npmCacheDirs returns the npm cache dirs ($npm_config_cache, ~/.npm).
func (s *scanner) npmCacheDirs() []string {
	return uniqDirs(s.envDir("npm_config_cache"), s.envDir("NPM_CONFIG_CACHE"), s.home(".npm"))
}

// npx emits one item per ~/.npm/_npx/<hash> install, named after the package.
func (s *scanner) npx() {
	for _, cache := range s.npmCacheDirs() {
		root := filepath.Join(cache, "_npx")
		for _, e := range listDir(s.ctx, root) {
			if s.ctx.Err() != nil {
				return
			}
			p := filepath.Join(root, e.Name())
			if !e.IsDir() || !s.allowed(p) {
				continue
			}
			pkgs := npxPackages(p)
			label := joinLimit(pkgs, 2)
			if label == "" {
				label = e.Name()
			}
			it := s.base("npx-package", p, "npx cache · "+label, core.RiskModerate)
			it.Path = p
			it.LastUsed = newestMtime(p, filepath.Join(p, "node_modules"), filepath.Join(p, "node_modules", ".package-lock.json"),
				filepath.Join(p, "package-lock.json"))
			it.Note = "Package installed on the fly by `npx` (often an MCP server started by an AI tool); the next `npx` call reinstalls it."
			if len(pkgs) > 0 {
				it.Meta["packages"] = strings.Join(pkgs, ", ")
			}
			if n := s.procs.usesDir(p); n > 0 {
				it.Warn = "used by " + itoa(n) + " running process(es) (MCP server?) — deleting it breaks them until restarted"
			} else if s.procs.ok && !it.LastUsed.IsZero() && s.env.Now.Sub(it.LastUsed) >= npxStaleAfter {
				it.Recommended = true
			}
			s.sized(it, []string{p}, false)
		}
	}
}

// npxPackages reads <dir>/package.json: _npx.packages, else dependency names.
func npxPackages(dir string) []string {
	data, err := readSmall(filepath.Join(dir, "package.json"))
	if err != nil {
		return nil
	}
	var pj struct {
		Dependencies map[string]string `json:"dependencies"`
		Npx          struct {
			Packages []string `json:"packages"`
		} `json:"_npx"`
	}
	if json.Unmarshal([]byte(data), &pj) != nil {
		return nil
	}
	if len(pj.Npx.Packages) > 0 {
		return pj.Npx.Packages
	}
	return sortedKeys(pj.Dependencies)
}

// ------------------------------------------------------------------ pnpm

var storeMajor = regexp.MustCompile(`^v[0-9]+$`)

// pnpm emits the pnpm content-addressable stores (one per store major:
// v3 = pnpm 7-9, v10, v11...) and, when the pnpm CLI works, `pnpm store prune`.
func (s *scanner) pnpm() {
	current := ""
	if s.env.Has("pnpm") {
		if out, err := output(s.ctx, s.env, s.env.Home, cmdTimeout, "pnpm", "store", "path"); err == nil {
			lines := strings.Split(strings.TrimSpace(string(out)), "\n")
			line := strings.TrimSpace(lines[len(lines)-1])
			if filepath.IsAbs(line) && fsx.IsDir(filepath.Clean(line)) {
				current = filepath.Clean(line)
			}
		}
	}
	var parents []string
	if current != "" {
		parents = append(parents, filepath.Dir(current))
	}
	parents = append(parents,
		s.envDir("PNPM_HOME", "store"),
		s.home("Library", "pnpm", "store"),
		s.envDir("XDG_DATA_HOME", "pnpm", "store"),
		s.home(".local", "share", "pnpm", "store"),
		s.home(".pnpm-store"),
	)
	seen := map[string]bool{}
	var stores []string
	addStore := func(p string) {
		real, err := filepath.EvalSymlinks(p)
		if err != nil || seen[real] || !isRealDir(real) {
			return
		}
		seen[real] = true
		stores = append(stores, p)
	}
	for _, parent := range uniqDirs(parents...) {
		for _, e := range listDir(s.ctx, parent) {
			if storeMajor.MatchString(e.Name()) {
				addStore(filepath.Join(parent, e.Name()))
			}
		}
	}
	if current != "" {
		addStore(current)
	}
	curReal, _ := filepath.EvalSymlinks(current)
	for _, st := range stores {
		if s.ctx.Err() != nil {
			return
		}
		if !s.allowed(st) {
			continue
		}
		real, _ := filepath.EvalSymlinks(st)
		isCurrent := current != "" && real == curReal
		it := s.base("pnpm-store", st, "pnpm store "+filepath.Base(st), core.RiskModerate)
		it.Path = st
		// links/ and projects/ too: global-virtual-store installs may only
		// touch them.
		it.LastUsed = newestMtime(st, filepath.Join(st, "index"), filepath.Join(st, "index.db"), filepath.Join(st, "files"),
			filepath.Join(st, "links"), filepath.Join(st, "projects"))
		it.Note = "pnpm content-addressable store; the next `pnpm install` re-downloads what it needs. Installed node_modules keep working (APFS clones or hardlinks)."
		gvs := pnpmGlobalVirtualStore(s.ctx, st)
		if gvs.used {
			// Project node_modules are symlinks into <store>/links: deleting
			// the store leaves them dangling.
			it.Risk = core.RiskCaution
			it.NoRecommend = true
			users := "its projects"
			if len(gvs.projects) > 0 {
				users = itoa(len(gvs.projects)) + " project(s)"
				it.Meta["gvs_projects"] = joinLimit(prettyAll(s.env, gvs.projects), 3)
			}
			it.Warn = "global virtual store used by " + users + ": their node_modules break until `pnpm install`"
			it.Note = "pnpm store with a global virtual store (links/): project node_modules are symlinks into it, so deleting it breaks them until `pnpm install`. Prefer `pnpm store prune`, which keeps what registered projects use."
		}
		if isCurrent {
			it.Meta["current"] = "true (pnpm store path)"
		}
		if done := s.sized(it, []string{st}, false); done != nil && isCurrent {
			s.pnpmPrune(done, gvs)
		}
	}
}

// pnpmGVS describes a pnpm global virtual store (pnpm ≥ 10.12
// enableGlobalVirtualStore): <store>/links holds the packages that project
// node_modules symlink to, <store>/projects registers those projects.
type pnpmGVS struct {
	used     bool     // links/ not empty, or live registered projects
	projects []string // registered projects that exist or cannot be checked
	// unreachable are registered projects that cannot be checked (unmounted
	// volume, EACCES, TCC): `pnpm store prune` may drop what they use.
	unreachable []string
}

// pnpmGlobalVirtualStore inspects st/links and st/projects. Anything it
// cannot read counts as used (fail closed).
func pnpmGlobalVirtualStore(ctx context.Context, st string) pnpmGVS {
	var g pnpmGVS
	ents, err := fsx.ReadDir(ctx, filepath.Join(st, "links"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		g.used = true
	}
	for _, e := range ents {
		if !strings.HasPrefix(e.Name(), ".") {
			g.used = true
			break
		}
	}
	projDir := filepath.Join(st, "projects")
	ents, err = fsx.ReadDir(ctx, projDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		g.used = true
	}
	for _, e := range ents {
		p := filepath.Join(projDir, e.Name())
		if e.Type()&os.ModeSymlink == 0 {
			continue
		}
		target := p
		if t, err := os.Readlink(p); err == nil {
			if !filepath.IsAbs(t) {
				t = filepath.Join(projDir, t)
			}
			target = filepath.Clean(t)
		}
		if _, err := os.Stat(p); err != nil {
			// Only a surely missing project (ENOENT under a readable parent)
			// is gone; EACCES, TCC or an unmounted volume keep it registered.
			if pathMissing(target) {
				continue
			}
			g.unreachable = append(g.unreachable, target)
		}
		g.projects = append(g.projects, target)
		g.used = true
	}
	return g
}

// pnpmPrune emits the `pnpm store prune` command item for the active store.
// What prune frees is unknown (only unreferenced packages), so the item has
// no size: the whole store would be a wild upper bound and would inflate the
// totals.
//
// Covers is the content folder INSIDE the store, never the store itself:
// core.TopLevel then always drops prune when the store deletion is selected
// too, whatever the selection order. With Covers equal to the store path,
// the first of the two in the list would win on the identical target, and a
// selected store deletion could silently be replaced by prune (the TUI would
// also count the store as wiped by prune).
func (s *scanner) pnpmPrune(store *core.Item, gvs pnpmGVS) {
	it := s.base("pnpm-store-prune", store.Path, "pnpm store prune", core.RiskSafe)
	it.Method = core.MethodCommand
	it.Command = []string{"pnpm", "store", "prune"}
	it.Location = store.Path
	it.Covers = filepath.Join(store.Path, "files")
	it.AlwaysShow = true
	it.Recommended = true
	it.LastUsed = store.LastUsed
	it.Note = "Removes only the packages no project references any more (the gain is unknown before running it; at most the whole store)."
	it.Meta["store"] = store.Path
	it.Meta["size"] = "unknown: prune frees only unreferenced packages (whole store: " + fsx.Bytes(store.Freed()) + ")"
	if n := len(gvs.unreachable); n > 0 {
		// pnpm treats a registered project it cannot reach as removed.
		it.Recommended = false
		it.NoRecommend = true
		it.Warn = itoa(n) + " registered project(s) unreachable (unmounted volume?): prune may drop the packages they use"
		it.Meta["unreachable_projects"] = joinLimit(prettyAll(s.env, gvs.unreachable), 3)
	}
	if store.Method == core.MethodReport {
		it.Method, it.Command, it.Selectable, it.Recommended, it.Warn = core.MethodReport, nil, false, false, store.Warn
	}
	s.emit(it)
}

// ------------------------------------------------------------------ yarn berry

// yarnGlobalFolders returns Yarn Berry global folders ($YARN_GLOBAL_FOLDER,
// globalFolder in ~/.yarnrc.yml, default ~/.yarn/berry).
func (s *scanner) yarnGlobalFolders() []string {
	cands := []string{s.envDir("YARN_GLOBAL_FOLDER")}
	if data, err := readSmall(s.home(".yarnrc.yml")); err == nil {
		for _, l := range strings.Split(data, "\n") {
			l = strings.TrimSpace(l)
			if v, ok := strings.CutPrefix(l, "globalFolder:"); ok {
				v = strings.Trim(strings.TrimSpace(v), `"'`)
				v = strings.ReplaceAll(v, "${HOME}", s.env.Home)
				v = s.env.Expand(v)
				if filepath.IsAbs(v) {
					cands = append(cands, v)
				}
			}
		}
	}
	cands = append(cands, s.home(".yarn", "berry"))
	return uniqDirs(cands...)
}

// warmYarnBerry measures the Yarn Berry global folders while the project
// walk that finds the Plug'n'Play projects runs.
func (s *scanner) warmYarnBerry() {
	for _, gf := range s.yarnGlobalFolders() {
		var paths []string
		for _, sub := range []string{"cache", "metadata", "store", "index"} {
			if p := filepath.Join(gf, sub); isDirOrLink(p) && s.allowed(p) {
				paths = append(paths, p)
			}
		}
		s.warm(paths)
	}
}

func (s *scanner) yarnBerry() {
	pnp := s.projects.pnp
	for _, gf := range s.yarnGlobalFolders() {
		if s.ctx.Err() != nil {
			return
		}
		// Global zip cache.
		if p := filepath.Join(gf, "cache"); isDirOrLink(p) && s.allowed(p) {
			it := s.base("yarn-berry-cache", p, "Yarn Berry global cache", core.RiskSafe)
			it.Note = "Yarn Berry global zip cache; the next `yarn install` re-downloads what it needs (projects using node_modules keep working)."
			switch {
			case len(pnp) > 0:
				// With the default enableGlobalCache, PnP projects run straight
				// from these zips: for them it is the install, not a cache.
				it.Risk = core.RiskCaution
				it.NoRecommend = true
				it.Warn = "used at runtime by " + itoa(len(pnp)) + " Plug'n'Play project(s): they break until `yarn install` (needs network)"
				it.Note = "Yarn Berry global zip cache. Plug'n'Play projects read these zips at runtime: they need `yarn install` again after cleaning."
				it.Meta["pnp_projects"] = joinLimit(prettyAll(s.env, pnp), 3)
			case s.projects.pnpIncomplete != "":
				// "No PnP project found" means nothing when the search was
				// partial: never rate the cache a pure cache then.
				it.Risk = core.RiskModerate
				it.Note += " Plug'n'Play projects (which read these zips at runtime) may exist where the search did not look."
				it.Meta["pnp_search"] = "incomplete: " + s.projects.pnpIncomplete
			}
			it.Path = p
			it.LastUsed = newestMtime(p)
			s.sized(it, []string{p}, true)
		}
		// Registry metadata (packuments): not removed by `yarn cache clean`.
		if p := filepath.Join(gf, "metadata"); isDirOrLink(p) && s.allowed(p) {
			it := s.base("yarn-berry-metadata", p, "Yarn Berry registry metadata", core.RiskSafe)
			it.Path = p
			it.Note = "npm registry metadata cached by Yarn Berry (not removed by `yarn cache clean`); re-fetched on the next resolution."
			s.sized(it, []string{p}, true)
		}
		// hardlinks-global content store (+ legacy index/).
		var store []string
		for _, sub := range []string{"store", "index"} {
			if p := filepath.Join(gf, sub); isDirOrLink(p) && s.allowed(p) {
				store = append(store, p)
			}
		}
		if len(store) > 0 {
			it := s.base("yarn-berry-store", store[0], "Yarn Berry hardlink store", core.RiskModerate)
			if len(store) == 1 {
				it.Path = store[0]
			} else {
				it.Paths = store
				it.Location = s.env.Pretty(gf) + "/…"
			}
			it.Note = "Content store of `nmMode: hardlinks-global`; files still hardlinked into node_modules survive (only the rest is freed). Refilled by the next `yarn install`."
			s.sized(it, store, true)
		}
	}
}

func prettyAll(env *core.Env, paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = env.Pretty(p)
	}
	sort.Strings(out)
	return out
}
