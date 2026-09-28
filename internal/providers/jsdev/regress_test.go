package jsdev

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
)

// Regression tests for the review findings of the js provider.

const stale30d = 30 * 24 * time.Hour

func berryCache(t *testing.T, items map[string]*core.Item) *core.Item {
	t.Helper()
	c := byKind(items, "yarn-berry-cache")
	if len(c) != 1 {
		t.Fatalf("berry cache items = %d", len(c))
	}
	return c[0]
}

// yarn-berry-cache-pnp: a Plug'n'Play project deeper than the pin depth
// (root/a/b/c/d) still protects the global Yarn cache.
func TestYarnCacheDeepPnPProject(t *testing.T) {
	w := newWorld(t)
	os.Remove(w.abs("code/pnpapp/.pnp.cjs"))
	w.text("code/clients/acme/web/app/.pnp.cjs", "// pnp\n")
	w.text("code/clients/acme/web/app/package.json", `{"packageManager":"yarn@4.5.0"}`)
	items, _ := collect(t, w.p, w.env)
	c := berryCache(t, items)
	if c.Risk != core.RiskCaution || !c.NoRecommend || c.Warn == "" || core.Recommend(c, w.now, stale30d/2) {
		t.Errorf("deep PnP project: cache must be caution and never recommended: %+v", c)
	}
	if !strings.Contains(c.Meta["pnp_projects"], "clients/acme/web/app") {
		t.Errorf("pnp_projects = %q", c.Meta["pnp_projects"])
	}
	// Pins keep their shallow depth: code/a/b/c/d/.nvmrc (18) is still ignored.
	for _, pin := range w.p2projects(t).pins {
		if strings.Contains(pin.source, "a/b/c/d") {
			t.Errorf("pin too deep recorded: %s", pin.source)
		}
	}
}

// p2projects runs only the project walk of the world.
func (w *world) p2projects(t *testing.T) *projectInfo {
	t.Helper()
	return loadProjects(context.Background(), w.env, w.p.maxProjectDirs)
}

// yarn-berry-cache-pnp: once a PnP project is known, a stale cache is still
// never smart-selected (it used to become moderate ★ after 14 days).
func TestYarnCachePnPStaleNeverRecommended(t *testing.T) {
	w := newWorld(t)
	w.age(".yarn/berry/cache/pkg-npm-1.0.0-abc.zip", stale30d)
	w.age(".yarn/berry/cache", stale30d)
	items, _ := collect(t, w.p, w.env)
	c := berryCache(t, items)
	if core.Recommend(c, w.now, 14*24*time.Hour) {
		t.Errorf("stale cache used by a PnP project recommended: %+v", c)
	}
}

// PnP projects in in-repo worktree folders (.worktrees, .claude/worktrees)
// are found although hidden folders are skipped otherwise.
func TestYarnCachePnPInRepoWorktree(t *testing.T) {
	for _, rel := range []string{"code/repo/.worktrees/feat/.pnp.cjs", "code/repo/.claude/worktrees/x/.pnp.js"} {
		t.Run(rel, func(t *testing.T) {
			w := newWorld(t)
			os.Remove(w.abs("code/pnpapp/.pnp.cjs"))
			w.text(rel, "// pnp\n")
			items, _ := collect(t, w.p, w.env)
			if c := berryCache(t, items); c.Risk != core.RiskCaution {
				t.Errorf("PnP project in %s missed: %+v", rel, c)
			}
		})
	}
}

// Without a PnP project the cache is safe only when the search was complete.
func TestYarnCacheIncompleteSearch(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(w *world)
		why   string
	}{
		{"capped", func(w *world) { w.p.maxProjectDirs = 2 }, "capped"},
		{"unreadable", func(w *world) {
			d := w.dir("code/locked")
			if err := os.Chmod(d, 0); err != nil {
				w.t.Fatal(err)
			}
			w.t.Cleanup(func() { os.Chmod(d, 0o755) })
		}, "unreadable folder ~/code/locked"},
		{"excluded", func(w *world) { w.env.Exclude = []string{w.abs("code/web")} }, "excluded folder ~/code/web"},
		{"explicit-roots", func(w *world) { w.env.ExplicitRoots = true }, "command line"},
		{"no-roots", func(w *world) { w.env.Roots = nil }, "no project folder"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			os.Remove(w.abs("code/pnpapp/.pnp.cjs"))
			tc.setup(w)
			items, _ := collect(t, w.p, w.env)
			c := berryCache(t, items)
			if c.Risk != core.RiskModerate || !strings.Contains(c.Meta["pnp_search"], tc.why) {
				t.Errorf("incomplete search (%s): risk %s, pnp_search %q", tc.name, c.Risk, c.Meta["pnp_search"])
			}
		})
	}
	// Complete search, no PnP project: a pure cache.
	w := newWorld(t)
	os.Remove(w.abs("code/pnpapp/.pnp.cjs"))
	items, _ := collect(t, w.p, w.env)
	if c := berryCache(t, items); c.Risk != core.RiskSafe || c.Meta["pnp_search"] != "" {
		t.Errorf("complete search without PnP: %+v", c)
	}
}

// pnpm-global-virtual-store: a store whose links/ is used by project
// node_modules is caution and never recommended, even when stale.
func TestPnpmGlobalVirtualStore(t *testing.T) {
	w := newWorld(t)
	gvs := "Library/pnpm/store/v11"
	w.file(gvs+"/files/00/ccc", 8000)
	w.file(gvs+"/links/@scope/pkg/1.0.0/abc/node_modules/@scope/pkg/index.js", 3000)
	w.link(w.abs("code/app1"), gvs+"/projects/ade6b8f0")
	w.link("../../../../../code/web", gvs+"/projects/bb12cd34") // relative, like pnpm writes it
	w.link(w.abs("code/gone"), gvs+"/projects/dead0000")        // removed project: ignored
	for _, rel := range []string{gvs + "/files/00/ccc", gvs + "/files/00", gvs + "/files", gvs + "/links", gvs + "/projects", gvs} {
		w.age(rel, stale30d)
	}
	// A classic store with only a dangling registration keeps the old policy.
	classic := "Library/pnpm/store/v9"
	w.file(classic+"/files/00/ddd", 8000)
	w.dir(classic + "/links")
	w.link(w.abs("code/gone2"), classic+"/projects/dead1111")
	for _, rel := range []string{classic + "/files/00/ddd", classic + "/files/00", classic + "/files", classic + "/links", classic + "/projects", classic} {
		w.age(rel, stale30d)
	}

	items, _ := collect(t, w.p, w.env)
	st := byName(items, "pnpm store v11")
	if st == nil {
		t.Fatal("v11 store not found")
	}
	if st.Risk != core.RiskCaution || !st.NoRecommend || !strings.Contains(st.Warn, "global virtual store used by 2 project(s)") ||
		core.Recommend(st, w.now, 14*24*time.Hour) || strings.Contains(st.Note, "keep working") {
		t.Errorf("GVS store = %+v", st)
	}
	if u := st.Meta["gvs_projects"]; !strings.Contains(u, "code/app1") || !strings.Contains(u, "code/web") || strings.Contains(u, "gone") {
		t.Errorf("gvs_projects = %q", u)
	}
	old := byName(items, "pnpm store v9")
	if old == nil || old.Risk != core.RiskModerate || old.NoRecommend || old.Warn != "" || !core.Recommend(old, w.now, 14*24*time.Hour) {
		t.Errorf("classic stale store = %+v", old)
	}

	// An install that only adds links (files/ untouched) keeps the store fresh.
	w.age(gvs+"/links", time.Hour)
	items, _ = collect(t, w.p, w.env)
	if st := byName(items, "pnpm store v11"); st == nil || w.now.Sub(st.LastUsed) > 2*time.Hour {
		t.Errorf("LastUsed ignores links/: %+v", st)
	}
}

// pnpm-prune-double-count: the prune command never adds the store size a
// second time to the totals.
func TestPnpmPruneNotDoubleCounted(t *testing.T) {
	w := newWorld(t)
	items, _ := collect(t, w.p, w.env)
	var store *core.Item
	for _, st := range byKind(items, "pnpm-store") {
		if st.Meta["current"] != "" {
			store = st
		}
	}
	prune := byKind(items, "pnpm-store-prune")
	if store == nil || len(prune) != 1 || store.Size == 0 {
		t.Fatalf("store %+v, prune %+v", store, prune)
	}
	if got := core.Total([]*core.Item{store, prune[0]}); got != store.Freed() {
		t.Errorf("store + prune total = %d, want %d (store counted once)", got, store.Freed())
	}
	if top := core.TopLevel([]*core.Item{store, prune[0]}); len(top) != 1 || top[0] != store {
		t.Errorf("prune not covered by the store: %v", top)
	}
	if got := core.Total([]*core.Item{prune[0]}); got != 0 {
		t.Errorf("prune alone counts %d bytes (whole store as gain)", got)
	}
	if !prune[0].AlwaysShow || prune[0].Meta["size"] == "" {
		t.Errorf("prune = %+v", prune[0])
	}
}

// watchman-del-all(-recommended): only the watches of deleted roots are
// removed, never the live ones (running Metro / jest --watch).
func TestWatchmanOnlyStaleRoots(t *testing.T) {
	w := newWorld(t)
	live := w.abs("code/app1")
	gone1, gone2 := w.abs("deleted-worktree"), w.abs("old/wt-2")
	locked := w.dir("locked")
	hidden := filepath.Join(locked, "proj") // unknown (EACCES), never "missing"
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })
	list := `{"version":"2024.12.02","roots":["` + live + `","` + gone2 + `","` + hidden + `","` + gone1 + `"]}`
	w.runner.out["watchman --no-spawn --no-pretty watch-list"] = list

	items, _ := collect(t, w.p, w.env)
	wm := byKind(items, "watchman-stale-watches")
	if len(wm) != 1 {
		t.Fatalf("watchman items = %d", len(wm))
	}
	it := wm[0]
	cmds := append([][]string{it.Command}, it.PostCommands...)
	var got []string
	for _, c := range cmds {
		got = append(got, strings.Join(c, " "))
	}
	want := []string{"watchman --no-spawn watch-del " + gone1, "watchman --no-spawn watch-del " + gone2}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("commands:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, c := range got {
		if strings.Contains(c, "watch-del-all") || strings.Contains(c, live) || strings.Contains(c, hidden) {
			t.Errorf("live or unknown watch removed: %s", c)
		}
	}
	if !strings.Contains(it.Name, "2 watch") || it.Recheck == nil {
		t.Errorf("item = %+v", it)
	}

	// Recheck: fine as scanned; refuses when a root is back or already dropped.
	ctx := context.Background()
	if err := it.Recheck(ctx); err != nil {
		t.Errorf("recheck: %v", err)
	}
	w.runner.out["watchman --no-spawn --no-pretty watch-list"] = `{"roots":["` + live + `","` + gone1 + `"]}`
	if err := it.Recheck(ctx); err == nil || !strings.Contains(err.Error(), "already gone") {
		t.Errorf("recheck with a dropped watch: %v", err)
	}
	w.runner.out["watchman --no-spawn --no-pretty watch-list"] = list
	w.dir("deleted-worktree")
	if err := it.Recheck(ctx); err == nil || !strings.Contains(err.Error(), "exists again") {
		t.Errorf("recheck with a re-created root: %v", err)
	}
}

func TestPathMissing(t *testing.T) {
	d, _ := filepath.EvalSymlinks(t.TempDir())
	if pathMissing(d) {
		t.Errorf("existing dir reported missing")
	}
	if !pathMissing(filepath.Join(d, "a/b/c")) {
		t.Errorf("missing path under a readable dir not reported")
	}
	locked := filepath.Join(d, "locked")
	os.Mkdir(locked, 0o755)
	os.Chmod(locked, 0)
	t.Cleanup(func() { os.Chmod(locked, 0o755) })
	if pathMissing(filepath.Join(locked, "x")) {
		t.Errorf("path under an unreadable dir reported missing")
	}
	f := filepath.Join(d, "file")
	os.WriteFile(f, nil, 0o644)
	if pathMissing(filepath.Join(f, "x")) {
		t.Errorf("ENOTDIR reported missing")
	}
	if pathMissing("/Volumes/lu-cleaner-no-such-volume-9f3a/dev/app") {
		t.Errorf("path on an unmounted volume reported missing")
	}
}

// A registered global-virtual-store project that cannot be reached (EACCES,
// unmounted volume) is not "gone": the store stays protected and `pnpm store
// prune`, which would treat it as removed, is not recommended.
func TestPnpmPruneUnreachableProject(t *testing.T) {
	w := newWorld(t)
	locked := w.dir("locked")
	w.link(filepath.Join(locked, "proj"), "Library/pnpm/store/v10/projects/0123abcd")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })
	items, _ := collect(t, w.p, w.env)
	st := byName(items, "pnpm store v10")
	if st == nil || st.Risk != core.RiskCaution || !st.NoRecommend {
		t.Errorf("store with an unreachable registered project = %+v", st)
	}
	prune := byKind(items, "pnpm-store-prune")
	if len(prune) != 1 || prune[0].Recommended || !prune[0].NoRecommend || !strings.Contains(prune[0].Warn, "unreachable") ||
		core.Recommend(prune[0], w.now, 14*24*time.Hour) {
		t.Errorf("prune = %+v", prune)
	}
}

// Re-verification of yarn-berry-cache-pnp: roots the Plug'n'Play search used
// to skip silently, or searched less deeply than the artifacts scanner.

// A root equal to the home folder is not walked: the search is incomplete
// (it was "complete" as soon as another root, e.g. a worktree home, existed).
func TestYarnCacheHomeRootIncomplete(t *testing.T) {
	w := newWorld(t)
	os.Remove(w.abs("code/pnpapp/.pnp.cjs"))
	w.text("myapp/.pnp.cjs", "// pnp\n")
	w.env.Roots = []string{w.home}
	w.env.WorktreeRoots = []string{w.dir(".codex/worktrees")}
	items, _ := collect(t, w.p, w.env)
	c := berryCache(t, items)
	if c.Risk == core.RiskSafe || !strings.Contains(c.Meta["pnp_search"], "home folder") {
		t.Errorf("home root: risk %s, pnp_search %q", c.Risk, c.Meta["pnp_search"])
	}
}

// The extra project folders of the artifacts scanner are searched too (not
// with explicit roots, which are the whole scope).
func TestYarnCachePnPExtraRoot(t *testing.T) {
	w := newWorld(t)
	os.Remove(w.abs("code/pnpapp/.pnp.cjs"))
	w.text(".gemini/antigravity/scratch/app/.pnp.cjs", "// pnp\n")
	items, _ := collect(t, w.p, w.env)
	if c := berryCache(t, items); c.Risk != core.RiskCaution || !strings.Contains(c.Meta["pnp_projects"], "antigravity/scratch/app") {
		t.Errorf("PnP project in an extra root missed: %+v", c)
	}
	w.env.ExplicitRoots = true
	items, _ = collect(t, w.p, w.env)
	if c := berryCache(t, items); c.Risk != core.RiskModerate || c.Meta["pnp_projects"] != "" {
		t.Errorf("explicit roots: extra root searched or search complete: %+v", c)
	}
}

// A linked worktree checkout gets the full depth again (artifacts scanner
// rule): a project 4 levels below a worktree that is itself 5 levels deep.
func TestYarnCachePnPDeepLinkedWorktree(t *testing.T) {
	w := newWorld(t)
	os.Remove(w.abs("code/pnpapp/.pnp.cjs"))
	w.text("code/a/b/c/d/wt/.git", "gitdir: "+w.abs("code/a/b/c/d/repo/.git/worktrees/wt")+"\n")
	w.text("code/a/b/c/d/wt/x/y/z/app/.pnp.cjs", "// pnp\n")
	items, _ := collect(t, w.p, w.env)
	if c := berryCache(t, items); c.Risk != core.RiskCaution {
		t.Errorf("PnP project deep in a linked worktree missed: %+v", c)
	}
}

// The same folder given twice (symlink, other case) is walked once: a
// project is not counted twice.
func TestYarnCachePnPRootsDeduplicated(t *testing.T) {
	w := newWorld(t)
	alias := w.link(w.abs("code"), "code-alias")
	w.env.Roots = append(w.env.Roots, alias)
	items, _ := collect(t, w.p, w.env)
	c := berryCache(t, items)
	if !strings.Contains(c.Warn, "by 1 Plug'n'Play") {
		t.Errorf("warn = %q (pnp_projects %q)", c.Warn, c.Meta["pnp_projects"])
	}
}

// pnpm-prune-double-count, re-verified: whatever the selection order, the
// store deletion is never replaced by prune (with Covers equal to the store
// path, the first of the two won on the identical target).
func TestPnpmPruneNeverReplacesStore(t *testing.T) {
	w := newWorld(t)
	items, _ := collect(t, w.p, w.env)
	var store *core.Item
	for _, st := range byKind(items, "pnpm-store") {
		if st.Meta["current"] != "" {
			store = st
		}
	}
	prune := byKind(items, "pnpm-store-prune")
	if store == nil || len(prune) != 1 {
		t.Fatalf("store %+v, prune %+v", store, prune)
	}
	for _, order := range [][]*core.Item{{store, prune[0]}, {prune[0], store}} {
		if top := core.TopLevel(order); len(top) != 1 || top[0] != store {
			t.Errorf("TopLevel(%s, %s) = %v", order[0].Name, order[1].Name, top)
		}
		if got := core.Total(order); got != store.Freed() {
			t.Errorf("total (%s first) = %d, want %d", order[0].Name, got, store.Freed())
		}
	}
	if top := core.TopLevel(prune); len(top) != 1 {
		t.Errorf("prune alone dropped: %v", top)
	}
}

// pathMissing: a symlink that dangles on the way (unmounted volume behind a
// link) or a /Volumes/X folder that is not a mount point means "unknown".
func TestPathMissingLinksAndVolumes(t *testing.T) {
	d, _ := filepath.EvalSymlinks(t.TempDir())
	gone := filepath.Join(d, "no-such-volume", "dev")
	// ~/ext -> /Volumes/USB/dev with the volume unplugged.
	ext := filepath.Join(d, "ext")
	if err := os.Symlink(gone, ext); err != nil {
		t.Fatal(err)
	}
	if pathMissing(filepath.Join(ext, "proj")) {
		t.Errorf("path behind a dangling symlink reported missing")
	}
	if pathMissing(ext) {
		t.Errorf("dangling symlink itself reported missing")
	}
	// A link to an existing readable folder: the missing part is known.
	real := filepath.Join(d, "real")
	os.Mkdir(real, 0o755)
	os.Symlink(real, filepath.Join(d, "alias"))
	if !pathMissing(filepath.Join(d, "alias", "proj")) {
		t.Errorf("missing path behind a live symlink not reported")
	}
	// /Volumes/X left as a plain folder (not a mount point).
	old := volumesDir
	volumesDir = d
	t.Cleanup(func() { volumesDir = old })
	os.Mkdir(filepath.Join(d, "USB"), 0o755)
	if pathMissing(filepath.Join(d, "USB", "dev", "app")) {
		t.Errorf("path on a leftover volume folder reported missing")
	}
}
