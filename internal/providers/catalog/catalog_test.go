package catalog

import (
	"context"
	"go/build"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/safety"
)

// TestDataFilesAreCompiled catches data files silently excluded by Go's
// filename build constraints (data_js.go = GOOS js, data_android.go = GOOS android…).
func TestDataFilesAreCompiled(t *testing.T) {
	files, _ := filepath.Glob("data_*.go")
	ctx := build.Default
	ctx.GOOS = "darwin"
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		ok, err := ctx.MatchFile(".", f)
		if err != nil || !ok {
			t.Errorf("%s is excluded from darwin builds by its name — rename it (e.g. data_%sdev.go)", f, strings.TrimSuffix(strings.TrimPrefix(f, "data_"), ".go"))
		}
	}
}

// TestEntriesAreValid checks every data_*.go entry for consistency and safety.
func TestEntriesAreValid(t *testing.T) {
	home := "/Users/tester"
	g := safety.New(home, "/var/folders/xx/yyyy/T", nil, nil)
	env := &core.Env{Home: home, TmpDir: "/var/folders/xx/yyyy/T"}
	ids := map[string]bool{}
	cats := map[core.Category]bool{}
	for _, c := range core.Categories {
		cats[c.ID] = true
	}
	for _, e := range Entries() {
		if e.ID == "" || strings.ToLower(e.ID) != e.ID || strings.ContainsAny(e.ID, " _/") {
			t.Errorf("%q: id must be kebab-case", e.ID)
		}
		if ids[e.ID] {
			t.Errorf("%s: duplicate id", e.ID)
		}
		ids[e.ID] = true
		if !cats[e.Category] {
			t.Errorf("%s: unknown category %q", e.ID, e.Category)
		}
		if e.Name == "" || e.Note == "" {
			t.Errorf("%s: name and note are required", e.ID)
		}
		if len(e.Paths) == 0 && e.Method != core.MethodCommand {
			t.Errorf("%s: no paths", e.ID)
		}
		if e.Method == core.MethodCommand && len(e.Command) == 0 {
			t.Errorf("%s: command method without command", e.ID)
		}
		if e.Method == core.MethodWorktree {
			t.Errorf("%s: worktrees belong to the worktrees provider", e.ID)
		}
		for _, p := range e.Paths {
			if !strings.HasPrefix(p, "~/") && !strings.HasPrefix(p, "$TMPDIR/") {
				t.Errorf("%s: path %q must start with ~/ or $TMPDIR/", e.ID, p)
			}
			if strings.Contains(p, "**") {
				t.Errorf("%s: path %q: ** is not supported", e.ID, p)
			}
			if _, err := filepath.Match(filepath.Base(p), "x"); err != nil {
				t.Errorf("%s: bad glob %q: %v", e.ID, p, err)
			}
			abs := env.Expand(p)
			if e.Method != core.MethodCommand && e.Method != core.MethodReport && !strings.ContainsAny(abs, "*?[") && g.Protected(abs) {
				t.Errorf("%s: path %q is protected by the safety guard", e.ID, p)
			}
		}
		if e.KeepLatest > 0 && e.Mode != Each {
			t.Errorf("%s: KeepLatest requires Mode Each", e.ID)
		}
	}
}

func TestScanFixture(t *testing.T) {
	home := t.TempDir()
	mk := func(rel string, size int, age time.Duration) {
		p := filepath.Join(home, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, make([]byte, size), 0o644)
		if age > 0 {
			ts := time.Now().Add(-age)
			os.Chtimes(p, ts, ts)
			os.Chtimes(filepath.Dir(p), ts, ts)
		}
	}
	mk(".fake/cache/a/blob", 200_000, 0)
	mk(".fake/cache/b/blob", 100_000, 0)
	mk(".fake/versions/v1/bin", 50_000, 72*time.Hour)
	mk(".fake/versions/v2/bin", 50_000, 48*time.Hour)
	mk(".fake/versions/v3/bin", 50_000, time.Hour)

	p := &Provider{entries: []Entry{
		{ID: "fake-cache", Category: core.CatJS, Name: "Fake cache", Paths: []string{"~/.fake/cache/*"}, Risk: core.RiskSafe, Note: "n"},
		{ID: "fake-versions", Category: core.CatJS, Name: "Fake versions", Paths: []string{"~/.fake/versions/*"}, Risk: core.RiskModerate, Mode: Each, KeepLatest: 1, Note: "n"},
		{ID: "fake-missing", Category: core.CatJS, Name: "Missing", Paths: []string{"~/.nope/*"}, Risk: core.RiskSafe, Note: "n"},
	}}
	env := core.NewEnv()
	env.Home = home
	var mu sync.Mutex
	got := map[string]*core.Item{}
	if err := p.Scan(context.Background(), env, func(it *core.Item) {
		mu.Lock()
		got[it.ID] = it
		mu.Unlock()
	}); err != nil {
		t.Fatal(err)
	}
	g := got["catalog:fake-cache"]
	if g == nil || len(g.Paths) != 2 || g.Size < 300_000 || g.Sizing {
		t.Fatalf("group item = %+v", g)
	}
	n := 0
	for id, it := range got {
		if strings.HasPrefix(id, "catalog:fake-versions:") {
			n++
			if strings.HasSuffix(it.Path, "v3") {
				t.Errorf("KeepLatest should keep the newest version out")
			}
		}
	}
	if n != 2 {
		t.Errorf("want 2 version items, got %d", n)
	}
	if _, ok := got["catalog:fake-missing"]; ok {
		t.Errorf("missing paths must not produce items")
	}
}
