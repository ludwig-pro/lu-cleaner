package apple

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

// now is the reference time of every fixture.
var now = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

func ago(d time.Duration) time.Time { return now.Add(-d) }

// fakeRunner answers commands from a table ("name arg arg" → stdout).
type fakeRunner struct {
	mu    sync.Mutex
	out   map[string]string
	bins  map[string]bool
	calls []string
}

func newFakeRunner(bins ...string) *fakeRunner {
	f := &fakeRunner{out: map[string]string{}, bins: map[string]bool{}}
	for _, b := range bins {
		f.bins[b] = true
	}
	return f
}

func (f *fakeRunner) Output(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	key := strings.Join(append([]string{name}, args...), " ")
	f.mu.Lock()
	f.calls = append(f.calls, key)
	out, ok := f.out[key]
	f.mu.Unlock()
	if !ok {
		return nil, errors.New("fake: unknown command " + key)
	}
	return []byte(out), nil
}

func (f *fakeRunner) LookPath(name string) (string, error) {
	if f.bins[name] {
		return "/usr/bin/" + name, nil
	}
	return "", exec.ErrNotFound
}

// fixture is a fake home (symlinks resolved so the safety guard agrees).
type fixture struct {
	t      *testing.T
	home   string
	sys    string // fake system root (/Applications, /Library, /System)
	runner *fakeRunner
	env    *core.Env
	guard  *safety.Guard
	prov   *Provider
	alive  []string // processes reported as running
}

func newFixture(t *testing.T, bins ...string) *fixture {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sys, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, home: home, sys: sys, runner: newFakeRunner(bins...)}
	tmp := filepath.Join(home, "T")
	os.MkdirAll(tmp, 0o755)
	f.guard = safety.New(home, tmp, nil, nil)
	f.env = &core.Env{
		Home:      home,
		TmpDir:    tmp,
		Now:       now,
		Runner:    f.runner,
		Protected: f.guard.Protected,
		Logf:      t.Logf,
	}
	f.prov = &Provider{systemRoot: sys}
	f.prov.running = func(names ...string) []string {
		var hit []string
		for _, n := range names {
			for _, a := range f.alive {
				if a == n {
					hit = append(hit, n)
				}
			}
		}
		return hit
	}
	return f
}

// p returns home/<rel>.
func (f *fixture) p(rel string) string { return filepath.Join(f.home, rel) }

// file creates a file of size bytes with mtime.
func (f *fixture) file(rel string, size int, mtime time.Time) string {
	f.t.Helper()
	p := rel
	if !filepath.IsAbs(rel) {
		p = f.p(rel)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
		f.t.Fatal(err)
	}
	setTime(f.t, p, mtime)
	return p
}

func (f *fixture) dir(rel string, mtime time.Time) string {
	f.t.Helper()
	p := rel
	if !filepath.IsAbs(rel) {
		p = f.p(rel)
	}
	if err := os.MkdirAll(p, 0o755); err != nil {
		f.t.Fatal(err)
	}
	setTime(f.t, p, mtime)
	return p
}

// plist writes an XML property list whose <dict> body is given.
func (f *fixture) plist(rel, body string) string {
	f.t.Helper()
	p := rel
	if !filepath.IsAbs(rel) {
		p = f.p(rel)
	}
	os.MkdirAll(filepath.Dir(p), 0o755)
	doc := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
` + body + `
</dict>
</plist>
`
	if err := os.WriteFile(p, []byte(doc), 0o644); err != nil {
		f.t.Fatal(err)
	}
	return p
}

func setTime(t *testing.T, p string, mtime time.Time) {
	t.Helper()
	if mtime.IsZero() {
		return
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// ageTree sets every mtime below root (children first) to mtime.
func ageTree(t *testing.T, root string, mtime time.Time) {
	t.Helper()
	var paths []string
	filepath.Walk(root, func(p string, _ os.FileInfo, err error) error {
		if err == nil {
			paths = append(paths, p)
		}
		return nil
	})
	for i := len(paths) - 1; i >= 0; i-- {
		os.Chtimes(paths[i], mtime, mtime)
	}
}

// scanResult holds every emitted version of every item.
type scanResult struct {
	final map[string]*core.Item
	order []string // first emission order
	emits map[string][]*core.Item
}

func (r *scanResult) byKind(kind string) []*core.Item {
	var out []*core.Item
	for _, id := range r.order {
		if it := r.final[id]; it.Kind == kind {
			out = append(out, it)
		}
	}
	return out
}

func (r *scanResult) one(t *testing.T, kind string) *core.Item {
	t.Helper()
	items := r.byKind(kind)
	if len(items) != 1 {
		t.Fatalf("want 1 %s item, got %d: %v", kind, len(items), names(items))
	}
	return items[0]
}

func (r *scanResult) ids() []string {
	ids := append([]string(nil), r.order...)
	sort.Strings(ids)
	return ids
}

func names(items []*core.Item) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.Name)
	}
	return out
}

func (f *fixture) scan() *scanResult {
	f.t.Helper()
	r := &scanResult{final: map[string]*core.Item{}, emits: map[string][]*core.Item{}}
	var mu sync.Mutex
	err := f.prov.Scan(context.Background(), f.env, func(it *core.Item) {
		mu.Lock()
		defer mu.Unlock()
		if _, ok := r.final[it.ID]; !ok {
			r.order = append(r.order, it.ID)
		}
		r.final[it.ID] = it
		r.emits[it.ID] = append(r.emits[it.ID], it)
	})
	if err != nil {
		f.t.Fatal(err)
	}
	f.checkInvariants(r)
	return r
}

// checkInvariants verifies properties every emitted item must have.
func (f *fixture) checkInvariants(r *scanResult) {
	t := f.t
	t.Helper()
	cats := map[core.Category]bool{}
	for _, c := range f.prov.Categories() {
		cats[c] = true
	}
	for id, it := range r.final {
		if it.Sizing {
			t.Errorf("%s: final version still sizing", id)
		}
		if !strings.HasPrefix(id, "apple:"+it.Kind+":") {
			t.Errorf("%s: id must be apple:<kind>:<key>", id)
		}
		if it.Provider != "apple" || !cats[it.Category] {
			t.Errorf("%s: provider %q / category %q not declared", id, it.Provider, it.Category)
		}
		if it.Name == "" || it.Note == "" {
			t.Errorf("%s: name and note are required", id)
		}
		if it.Method == core.MethodCommand && len(it.Command) == 0 {
			t.Errorf("%s: command method without command", id)
		}
		if it.Recommended && it.Risk >= core.RiskCaution {
			t.Errorf("%s: caution items must never be recommended", id)
		}
		if len(it.Paths) > 0 && it.Path != "" {
			t.Errorf("%s: group item with Path", id)
		}
		// A placeholder (Sizing) may only come first.
		for i, e := range r.emits[id] {
			if e.Sizing && i != 0 {
				t.Errorf("%s: sizing placeholder emitted after a measured version", id)
			}
		}
		if it.Method == core.MethodDelete || it.Method == core.MethodTrash {
			for _, p := range it.Targets() {
				if f.env.IsProtected(p) {
					t.Errorf("%s: proposes protected path %s", id, p)
				}
				if err := f.guard.Check(p, safety.Options{AllowGitRepo: it.AllowGitRepo}); err != nil {
					t.Errorf("%s: target refused by the safety guard: %v", id, err)
				}
			}
		}
	}
}
