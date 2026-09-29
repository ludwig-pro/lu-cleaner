package jsdev

import (
	"strings"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
)

const staleAfter = 14 * 24 * time.Hour

// keep_latest: the N newest unreferenced versions of each manager are never
// preselected (a version just installed and used through `nvm use` in
// shells that are not running now looks unreferenced); older unreferenced
// ones still are.
func TestNodeVersionsKeepLatestUnreferenced(t *testing.T) {
	for _, c := range []struct {
		keep       int
		v18Latest  bool // nvm v18.20.4: older than the unreferenced v20.11.1
		v20Latest  bool // nvm v20.11.1: newest unreferenced nvm version
		fnmMiseLat bool // fnm v20.18.0 / mise v20.20.1: only unreferenced version of their manager
	}{
		{0, false, true, true}, // keep_latest unset: at least 1
		{1, false, true, true},
		{2, true, true, true},
	} {
		w := newWorld(t)
		w.env.KeepLatest = c.keep
		items, _ := collect(t, w.p, w.env)
		check := func(name string, latest bool) {
			t.Helper()
			it := byName(items, name)
			if it == nil {
				t.Fatalf("keep %d: %s missing", c.keep, name)
			}
			if it.NoRecommend != latest {
				t.Errorf("keep %d: %s NoRecommend = %v, want %v", c.keep, name, it.NoRecommend, latest)
			}
			has := strings.Contains(it.Meta["kept_because"], "keep_latest")
			if has != latest {
				t.Errorf("keep %d: %s kept_because = %q", c.keep, name, it.Meta["kept_because"])
			}
			if latest && (it.Recommended || core.Recommend(it, w.now.Add(365*24*time.Hour), staleAfter)) {
				t.Errorf("keep %d: %s is preselected although it is one of the newest unreferenced versions", c.keep, name)
			}
			if !latest && it.Warn == "" && !core.Recommend(it, w.now, staleAfter) {
				t.Errorf("keep %d: %s (older unreferenced) should still be recommended", c.keep, name)
			}
			if !it.Selectable {
				t.Errorf("keep %d: %s must stay selectable", c.keep, name)
			}
		}
		check("node v18.20.4 (nvm)", c.v18Latest)
		check("node v20.11.1 (nvm)", c.v20Latest)
		check("node v20.18.0 (fnm)", c.fnmMiseLat)
		check("node v20.20.1 (mise)", c.fnmMiseLat)
		// Referenced versions are never flagged by keep_latest (they have their own reasons).
		for _, name := range []string{"node v20.19.6 (nvm)", "node v22.22.0 (fnm)", "node v24.20.0 (mise)"} {
			if it := byName(items, name); it == nil || strings.Contains(it.Meta["kept_because"], "keep_latest") {
				t.Errorf("keep %d: %s flagged by keep_latest: %v", c.keep, name, it)
			}
		}
	}
}

// The same version installed in two roots of one manager counts once.
func TestMarkLatestDistinctVersions(t *testing.T) {
	v := func(s string) version { x, _ := parseVersion(s); return x }
	a := &nodeInstall{manager: "fnm", root: "/a", ver: v("22.1.0")}
	b := &nodeInstall{manager: "fnm", root: "/b", ver: v("22.1.0")}
	c := &nodeInstall{manager: "fnm", root: "/a", ver: v("20.1.0")}
	d := &nodeInstall{manager: "nvm", root: "/n", ver: v("18.1.0")}
	ref := &nodeInstall{manager: "fnm", root: "/a", ver: v("24.1.0"), defaults: []string{"fnm default"}}
	s := &scanner{env: &core.Env{KeepLatest: 1}}
	s.markLatest([]*nodeInstall{c, a, ref, d, b})
	if !a.latest || !b.latest || c.latest || !d.latest || ref.latest {
		t.Errorf("latest: a=%v b=%v c=%v d=%v ref=%v, want true true false true false", a.latest, b.latest, c.latest, d.latest, ref.latest)
	}
}

// Without the process list, an "unreferenced" version may still be running:
// no node version may be preselected, even a stale one.
func TestNodeVersionsFailClosedWithoutProcessList(t *testing.T) {
	w := newWorld(t)
	delete(w.runner.out, "/bin/ps -axww -o args=")
	w.env.KeepLatest = 1
	items, _ := collect(t, w.p, w.env)
	got := byKind(items, "node-version")
	if len(got) == 0 {
		t.Fatal("no node-version items")
	}
	for _, it := range got {
		if !it.NoRecommend || it.Recommended {
			t.Errorf("%s: NoRecommend = %v, Recommended = %v without process list", it.Name, it.NoRecommend, it.Recommended)
		}
		later := w.now.Add(365 * 24 * time.Hour) // every version is stale by then
		if core.Recommend(it, later, staleAfter) {
			t.Errorf("%s: preselected without process list", it.Name)
		}
		if it.Meta["in_use"] == "" {
			t.Errorf("%s: in_use meta missing", it.Name)
		}
	}
	// With the process list, a stale older unreferenced version is recommended
	// (the veto above comes from the unknown process list only).
	w2 := newWorld(t)
	items2, _ := collect(t, w2.p, w2.env)
	if it := byName(items2, "node v18.20.4 (nvm)"); it == nil || it.NoRecommend || !core.Recommend(it, w2.now, staleAfter) {
		t.Errorf("with process list, v18.20.4 (nvm) should be recommended: %+v", it)
	}
}
