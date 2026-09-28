package system

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/safety"
)

// fakeRunner answers commands from a table ("name arg1 arg2" -> output).
type fakeRunner struct {
	mu    sync.Mutex
	out   map[string]string
	fail  map[string]bool
	block map[string]bool // commands that hang until their context expires
	bins  map[string]bool
	calls []string
}

func newFakeRunner(bins ...string) *fakeRunner {
	f := &fakeRunner{out: map[string]string{}, fail: map[string]bool{}, block: map[string]bool{}, bins: map[string]bool{}}
	for _, b := range bins {
		f.bins[b] = true
	}
	return f
}

func (f *fakeRunner) Output(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	key := strings.Join(append([]string{name}, args...), " ")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, key)
	if f.block[key] {
		f.mu.Unlock()
		<-ctx.Done()
		f.mu.Lock()
		return nil, ctx.Err()
	}
	if f.fail[key] {
		return nil, errors.New("fake: exit status 1")
	}
	out, ok := f.out[key]
	if !ok {
		return nil, errors.New("fake: unknown command " + key)
	}
	return []byte(out), nil
}

func (f *fakeRunner) LookPath(name string) (string, error) {
	if f.bins[name] {
		return "/usr/local/bin/" + name, nil
	}
	return "", exec.ErrNotFound
}

// fixture is a fake home (symlinks resolved so the safety guard agrees).
type fixture struct {
	t      *testing.T
	home   string
	now    time.Time
	runner *fakeRunner
	guard  *safety.Guard
	p      *Provider
	env    *core.Env
}

func newFixture(t *testing.T, parts ...string) *fixture {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tmp, _ := filepath.EvalSymlinks(t.TempDir())
	f := &fixture{t: t, home: home, now: time.Now(), runner: newFakeRunner()}
	f.guard = safety.New(home, filepath.Join(tmp, "T"), nil, nil)
	only := map[string]bool{}
	for _, p := range parts {
		only[p] = true
	}
	f.p = &Provider{
		appDirs:    []string{filepath.Join(home, "Applications")},
		running:    func(...string) []string { return nil },
		getenv:     func(string) string { return "" },
		swapUsage:  func() (int64, int64, bool) { return 0, 0, false },
		updatesDir: "-",
		only:       only,
		lastUsed:   func(string) (time.Time, bool) { return time.Time{}, false },
	}
	f.env = &core.Env{
		Home:      home,
		TmpDir:    filepath.Join(tmp, "T"),
		Now:       f.now,
		Runner:    f.runner,
		Protected: f.guard.Protected,
		Logf:      t.Logf,
	}
	return f
}

// abs returns the absolute path of rel in the fake home.
func (f *fixture) abs(rel string) string { return filepath.Join(f.home, filepath.FromSlash(rel)) }

// file creates a file of size bytes (real blocks), aged by age.
func (f *fixture) file(rel string, size int, age time.Duration) string {
	f.t.Helper()
	p := f.abs(rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
		f.t.Fatal(err)
	}
	if age > 0 {
		f.age(rel, age)
	}
	return p
}

// dir creates a directory aged by age.
func (f *fixture) dir(rel string, age time.Duration) string {
	f.t.Helper()
	p := f.abs(rel)
	if err := os.MkdirAll(p, 0o755); err != nil {
		f.t.Fatal(err)
	}
	if age > 0 {
		f.age(rel, age)
	}
	return p
}

// write writes text content.
func (f *fixture) write(rel, content string) string {
	f.t.Helper()
	p := f.abs(rel)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
	return p
}

// age sets the mtime of rel to now-age.
func (f *fixture) age(rel string, age time.Duration) {
	ts := f.now.Add(-age)
	if err := os.Chtimes(f.abs(rel), ts, ts); err != nil {
		f.t.Fatal(err)
	}
}

// ageTree sets the mtime of rel and everything below to now-age.
func (f *fixture) ageTree(rel string, age time.Duration) {
	ts := f.now.Add(-age)
	filepath.Walk(f.abs(rel), func(p string, _ os.FileInfo, err error) error {
		if err == nil {
			os.Chtimes(p, ts, ts)
		}
		return nil
	})
}

// scan runs the provider and returns the final items by ID.
func (f *fixture) scan() map[string]*core.Item {
	f.t.Helper()
	var mu sync.Mutex
	got := map[string]*core.Item{}
	if err := f.p.Scan(context.Background(), f.env, func(it *core.Item) {
		mu.Lock()
		got[it.ID] = it
		mu.Unlock()
	}); err != nil {
		f.t.Fatal(err)
	}
	for id, it := range got {
		if it.Sizing {
			f.t.Errorf("%s still sizing", id)
		}
		if it.Provider != "system" {
			f.t.Errorf("%s: provider %q", id, it.Provider)
		}
		if it.Note == "" || it.Name == "" || it.Kind == "" {
			f.t.Errorf("%s: name, kind and note are required", id)
		}
		if !containsCat(f.p.Categories(), it.Category) {
			f.t.Errorf("%s: category %s not declared", id, it.Category)
		}
		f.checkGuard(it)
	}
	return got
}

// checkGuard verifies that every cleanable filesystem target would pass the
// safety guard and is not protected.
func (f *fixture) checkGuard(it *core.Item) {
	f.t.Helper()
	if !it.CanClean() || it.Method == core.MethodCommand {
		return
	}
	if it.Path != "" && len(it.Paths) > 0 {
		f.t.Errorf("%s: Path and Paths both set", it.ID)
	}
	for _, p := range it.Targets() {
		if f.env.IsProtected(p) {
			f.t.Errorf("%s: proposes protected path %s", it.ID, p)
		}
		if err := f.guard.Check(p, safety.Options{AllowGitRepo: it.AllowGitRepo}); err != nil {
			f.t.Errorf("%s: guard refuses %s: %v", it.ID, p, err)
		}
	}
}

func containsCat(cs []core.Category, c core.Category) bool {
	for _, x := range cs {
		if x == c {
			return true
		}
	}
	return false
}

// byKind returns the items of kind, sorted by ID.
func byKind(items map[string]*core.Item, kind string) []*core.Item {
	var out []*core.Item
	for _, it := range items {
		if it.Kind == kind {
			out = append(out, it)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// one returns the single item of kind (fails otherwise).
func one(t *testing.T, items map[string]*core.Item, kind string) *core.Item {
	t.Helper()
	l := byKind(items, kind)
	if len(l) != 1 {
		var kinds []string
		for _, it := range items {
			kinds = append(kinds, it.Kind)
		}
		sort.Strings(kinds)
		t.Fatalf("want 1 %s item, got %d (kinds: %v)", kind, len(l), kinds)
	}
	return l[0]
}

// bases returns the base names of the item targets, sorted.
func bases(it *core.Item) []string {
	var out []string
	for _, p := range it.Targets() {
		out = append(out, filepath.Base(p))
	}
	sort.Strings(out)
	return out
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

const day = 24 * time.Hour
