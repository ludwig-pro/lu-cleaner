package aitools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/safety"
)

// fakeRunner answers external commands from a table keyed by the command
// name (first match of a key prefix of "name arg1 arg2...").
type fakeRunner struct {
	mu    sync.Mutex
	bins  map[string]bool
	out   map[string]string
	calls []string
}

func (f *fakeRunner) Output(_ context.Context, _ string, name string, args ...string) ([]byte, error) {
	line := strings.Join(append([]string{name}, args...), " ")
	f.mu.Lock()
	f.calls = append(f.calls, line)
	f.mu.Unlock()
	keys := make([]string, 0, len(f.out))
	for k := range f.out {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	for _, k := range keys {
		if strings.HasPrefix(line, k) {
			return []byte(f.out[k]), nil
		}
	}
	return nil, errors.New("fake: unknown command " + line)
}

func (f *fakeRunner) LookPath(name string) (string, error) {
	if f.bins[name] {
		return "/usr/bin/" + name, nil
	}
	return "", errors.New("not found")
}

// fixture is a fake home directory plus the env / provider scanning it.
type fixture struct {
	t      *testing.T
	home   string
	now    time.Time
	runner *fakeRunner
	guard  *safety.Guard
	p      *Provider
	env    *core.Env
	// running is returned by the provider's process check.
	running []string
}

var fixedNow = time.Date(2026, 9, 28, 12, 0, 0, 0, time.Local)

func newFixture(t *testing.T) *fixture {
	t.Helper()
	home := t.TempDir()
	if r, err := filepath.EvalSymlinks(home); err == nil {
		home = r
	}
	tmp := t.TempDir()
	f := &fixture{
		t:      t,
		home:   home,
		now:    fixedNow,
		runner: &fakeRunner{bins: map[string]bool{}, out: map[string]string{"ps -axo comm=": ""}},
		guard:  safety.New(home, tmp, nil, nil),
	}
	f.p = &Provider{
		running:      func(names ...string) []string { return f.hit(names) },
		appDirs:      []string{"~/Applications"},
		resolverRoot: home,
	}
	f.env = &core.Env{
		Home:      home,
		TmpDir:    tmp,
		Now:       f.now,
		Runner:    f.runner,
		Logf:      func(string, ...any) {},
		Protected: f.guard.Protected,
	}
	return f
}

func (f *fixture) hit(names []string) []string {
	var out []string
	for _, n := range names {
		for _, r := range f.running {
			if strings.EqualFold(n, r) {
				out = append(out, n)
			}
		}
	}
	return out
}

func (f *fixture) path(rel string) string { return filepath.Join(f.home, rel) }

// file writes size bytes at rel, with mtime now-age (age 0 = fixture now).
func (f *fixture) file(rel string, size int, age time.Duration) string {
	f.t.Helper()
	p := f.path(rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
		f.t.Fatal(err)
	}
	f.age(rel, age)
	return p
}

// text writes content at rel.
func (f *fixture) text(rel, content string, age time.Duration) string {
	f.t.Helper()
	p := f.path(rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
	f.age(rel, age)
	return p
}

func (f *fixture) dir(rel string, age time.Duration) string {
	f.t.Helper()
	p := f.path(rel)
	if err := os.MkdirAll(p, 0o755); err != nil {
		f.t.Fatal(err)
	}
	f.age(rel, age)
	return p
}

func (f *fixture) link(rel, target string) string {
	f.t.Helper()
	p := f.path(rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Symlink(target, p); err != nil {
		f.t.Fatal(err)
	}
	return p
}

// age sets the mtime of rel to now-age.
func (f *fixture) age(rel string, age time.Duration) {
	f.t.Helper()
	ts := f.now.Add(-age)
	if err := os.Chtimes(f.path(rel), ts, ts); err != nil {
		f.t.Fatal(err)
	}
}

type scanResult struct {
	items       map[string]*core.Item // final version per ID
	placeholder map[string]bool       // IDs first emitted with Sizing=true
}

func (f *fixture) scan() scanResult {
	f.t.Helper()
	res := scanResult{items: map[string]*core.Item{}, placeholder: map[string]bool{}}
	var mu sync.Mutex
	err := f.p.Scan(context.Background(), f.env, func(it *core.Item) {
		mu.Lock()
		defer mu.Unlock()
		if it.Sizing {
			if _, seen := res.items[it.ID]; !seen {
				res.placeholder[it.ID] = true
			}
		}
		res.items[it.ID] = it
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return res
}

// byKind returns the final items of kind, sorted by ID.
func (r scanResult) byKind(kind string) []*core.Item {
	var out []*core.Item
	for _, it := range r.items {
		if it.Kind == kind {
			out = append(out, it)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (r scanResult) one(t *testing.T, kind string) *core.Item {
	t.Helper()
	its := r.byKind(kind)
	if len(its) != 1 {
		t.Fatalf("kind %s: got %d items, want 1: %v", kind, len(its), names(its))
	}
	return its[0]
}

func names(its []*core.Item) []string {
	var out []string
	for _, it := range its {
		out = append(out, it.Name+" "+strings.Join(it.Targets(), ","))
	}
	return out
}

// hasTarget reports whether it targets exactly p.
func hasTarget(it *core.Item, p string) bool {
	for _, t := range it.Targets() {
		if t == p {
			return true
		}
	}
	return false
}

// checkInvariants validates every emitted item against the provider contract.
func (f *fixture) checkInvariants(r scanResult, protected ...string) {
	f.t.Helper()
	for id, it := range r.items {
		if it.Sizing {
			f.t.Errorf("%s: final version still Sizing", id)
		}
		if it.Provider != providerID || !strings.HasPrefix(id, providerID+":"+it.Kind+":") {
			f.t.Errorf("%s: bad provider/id (%s, %s)", id, it.Provider, it.Kind)
		}
		if it.Name == "" || it.Note == "" || it.Kind == "" || it.Category == "" {
			f.t.Errorf("%s: name/note/kind/category required", id)
		}
		if it.Path != "" && len(it.Paths) > 0 {
			f.t.Errorf("%s: both Path and Paths", id)
		}
		if len(it.Paths) > 0 && it.Location == "" {
			f.t.Errorf("%s: group item without Location", id)
		}
		if it.Category != core.CatAI && it.Category != core.CatIDE {
			f.t.Errorf("%s: category %s not declared by Categories()", id, it.Category)
		}
		seen := map[string]bool{}
		for _, tg := range it.Targets() {
			if seen[tg] {
				f.t.Errorf("%s: duplicate target %s", id, tg)
			}
			seen[tg] = true
			if f.env.IsProtected(tg) {
				f.t.Errorf("%s: proposes protected path %s", id, tg)
			}
			for _, p := range protected {
				if tg == p || strings.HasPrefix(p, tg+"/") {
					f.t.Errorf("%s: target %s is or contains never-delete path %s", id, tg, p)
				}
			}
			if it.CanClean() {
				if err := f.guard.Check(tg, safety.Options{AllowGitRepo: it.AllowGitRepo}); err != nil {
					f.t.Errorf("%s: guard refuses %s: %v", id, tg, err)
				}
			}
		}
	}
	// No path may be emitted by two different items (double counting).
	owner := map[string]string{}
	for id, it := range r.items {
		for _, tg := range it.Targets() {
			if o, ok := owner[tg]; ok && o != id {
				f.t.Errorf("path %s emitted by %s and %s", tg, o, id)
			}
			owner[tg] = id
		}
	}
}

// ageTree sets the mtime of rel and everything below it to now-age.
func (f *fixture) ageTree(rel string, age time.Duration) {
	f.t.Helper()
	ts := f.now.Add(-age)
	var paths []string
	_ = filepath.Walk(f.path(rel), func(p string, _ os.FileInfo, err error) error {
		if err == nil {
			paths = append(paths, p)
		}
		return nil
	})
	// children first so that setting a child does not bump the parent afterwards
	for i := len(paths) - 1; i >= 0; i-- {
		_ = os.Chtimes(paths[i], ts, ts)
	}
}
