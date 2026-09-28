package catalog

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
)

// Regression tests for defects found by the adversarial review.

func scanEntries(t *testing.T, env *core.Env, es ...Entry) map[string]*core.Item {
	t.Helper()
	var mu sync.Mutex
	got := map[string]*core.Item{}
	if err := (&Provider{entries: es}).Scan(context.Background(), env, func(it *core.Item) {
		mu.Lock()
		got[it.ID] = it
		mu.Unlock()
	}); err != nil {
		t.Fatal(err)
	}
	return got
}

func mkFile(t *testing.T, p string, size int, age time.Duration) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	ts := time.Now().Add(-age)
	os.Chtimes(p, ts, ts)
	os.Chtimes(filepath.Dir(p), ts, ts)
}

// keep-latest-ignored: config keep_latest raises the number of newest
// matches an Each entry keeps out; entries without KeepLatest are unchanged.
func TestKeepLatestFromConfig(t *testing.T) {
	home, _ := filepath.EvalSymlinks(t.TempDir())
	for i, v := range []string{"v1", "v2", "v3", "v4"} {
		mkFile(t, filepath.Join(home, ".fake/versions", v, "bin"), 10_000, time.Duration(4-i)*24*time.Hour)
	}
	entries := []Entry{
		{ID: "fake-versions", Category: core.CatJS, Name: "Fake versions", Paths: []string{"~/.fake/versions/*"}, Risk: core.RiskModerate, Mode: Each, KeepLatest: 1, Note: "n"},
		{ID: "fake-all", Category: core.CatJS, Name: "Fake all", Paths: []string{"~/.fake/versions/*"}, Risk: core.RiskModerate, Mode: Each, Note: "n"},
	}
	count := func(items map[string]*core.Item, id string) (n int, paths []string) {
		for k, it := range items {
			if strings.HasPrefix(k, "catalog:"+id+":") {
				n++
				paths = append(paths, filepath.Base(it.Path))
			}
		}
		return n, paths
	}
	for _, c := range []struct{ keep, want int }{{0, 3}, {1, 3}, {2, 2}, {3, 1}, {4, 0}} {
		env := core.NewEnv()
		env.Home = home
		env.KeepLatest = c.keep
		items := scanEntries(t, env, entries...)
		if n, paths := count(items, "fake-versions"); n != c.want {
			t.Errorf("keep_latest %d: %d items %v, want %d", c.keep, n, paths, c.want)
		}
		if n, _ := count(items, "fake-all"); n != 4 {
			t.Errorf("keep_latest %d: an entry without KeepLatest must list every match, got %d", c.keep, n)
		}
	}
}

// catalog-external-hidden: a match that lives on another volume is reported
// with its measured size (an unmeasured size-0 report is hidden everywhere).
func TestExternalMatchIsMeasured(t *testing.T) {
	home, _ := filepath.EvalSymlinks(t.TempDir())
	ext, _ := filepath.EvalSymlinks(t.TempDir()) // stands for /Volumes/SSD/...
	mkFile(t, filepath.Join(ext, "Yarn", "cache", "pkg.tgz"), 120_000, 0)
	if err := os.MkdirAll(filepath.Join(home, "Library/Caches"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(ext, "Yarn"), filepath.Join(home, "Library/Caches/Yarn")); err != nil {
		t.Fatal(err)
	}
	old := devOf
	defer func() { devOf = old }()
	devOf = func(p string) (uint64, error) {
		if p == ext || strings.HasPrefix(p, ext+"/") {
			return 424242, nil
		}
		return old(p)
	}
	env := core.NewEnv()
	env.Home = home
	items := scanEntries(t, env, Entry{ID: "fake-yarn", Category: core.CatJS, Name: "Yarn cache",
		Paths: []string{"~/Library/Caches/Yarn"}, Risk: core.RiskSafe, Note: "n"})
	if len(items) != 1 {
		t.Fatalf("items = %v", items)
	}
	for _, it := range items {
		if it.CanClean() || it.Method != core.MethodReport || !strings.Contains(it.Warn, "external") {
			t.Errorf("external match must be report only: %+v", it)
		}
		if it.Sizing || it.Size < 120_000 || it.Files < 1 {
			t.Errorf("external match not measured: size %d, files %d, sizing %v", it.Size, it.Files, it.Sizing)
		}
	}
}
