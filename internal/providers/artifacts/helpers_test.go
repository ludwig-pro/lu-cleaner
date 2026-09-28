package artifacts

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
	"github.com/ludwig-pro/lu-cleaner/internal/safety"
)

// now is the reference time of every fixture.
var now = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

func ago(d time.Duration) time.Time { return now.Add(-d) }

const day = 24 * time.Hour

// fakeRepo is what the fake git knows about one work tree.
type fakeRepo struct {
	ignored []string // ignored paths as listed by ls-files --others --ignored --directory ("dir/" or "file")
	tracked []string // tracked files (rel)
	listErr bool     // ls-files --others fails (check-ignore fallback)
	broken  bool     // every git command fails
	// modified is what `git diff-files --name-only` lists; nil makes the
	// command fail (activity from the source walk only).
	modified []string
	// untracked is what `git ls-files --others --exclude-standard
	// --directory` lists ("new/" for a folder).
	untracked []string
}

// fakeRunner simulates git per work tree (keyed by absolute path).
type fakeRunner struct {
	mu    sync.Mutex
	repos map[string]*fakeRepo
	noGit bool
	calls []string
}

func newFakeRunner() *fakeRunner { return &fakeRunner{repos: map[string]*fakeRepo{}} }

func (f *fakeRunner) LookPath(name string) (string, error) {
	if name == "git" && !f.noGit {
		return "/usr/bin/git", nil
	}
	return "", exec.ErrNotFound
}

// exit1 is a genuine *exec.ExitError with status 1.
func exit1() error { return exec.Command("/usr/bin/false").Run() }

func (f *fakeRunner) Output(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, dir+": "+name+" "+strings.Join(args, " "))
	r := f.repos[dir]
	f.mu.Unlock()
	if name != "git" || r == nil || r.broken {
		return nil, exec.ErrNotFound
	}
	var paths []string
	sub := ""
	for i, a := range args {
		switch a {
		case "ls-files", "check-ignore", "diff-files":
			if sub == "" {
				sub = a
			}
		case "--":
			paths = args[i+1:]
		}
	}
	has := func(flag string) bool {
		for _, a := range args {
			if a == flag {
				return true
			}
		}
		return false
	}
	switch {
	case sub == "diff-files":
		if r.modified == nil {
			return nil, exit1()
		}
		if len(r.modified) == 0 {
			return nil, nil
		}
		return []byte(strings.Join(r.modified, "\x00") + "\x00"), nil
	case sub == "ls-files" && has("--others"):
		if r.listErr {
			return nil, exit1()
		}
		if !has("--ignored") {
			return []byte(strings.Join(r.untracked, "\x00") + "\x00"), nil
		}
		return []byte(strings.Join(r.ignored, "\x00") + "\x00"), nil
	case sub == "check-ignore":
		var out []string
		for _, p := range paths {
			if r.isIgnored(p) {
				out = append(out, p)
			}
		}
		if len(out) == 0 {
			return nil, exit1()
		}
		return []byte(strings.Join(out, "\n") + "\n"), nil
	case sub == "ls-files":
		var out []string
		for _, t := range r.tracked {
			for _, p := range paths {
				if t == p || strings.HasPrefix(t, p+"/") {
					out = append(out, t)
					break
				}
			}
		}
		if len(out) == 0 {
			return nil, nil
		}
		return []byte(strings.Join(out, "\x00") + "\x00"), nil
	}
	return nil, exec.ErrNotFound
}

func (r *fakeRepo) isIgnored(p string) bool {
	for _, ig := range r.ignored {
		d := strings.TrimSuffix(ig, "/")
		if p == d || strings.HasPrefix(p, d+"/") {
			return true
		}
	}
	return false
}

// fixture is a fake home with project roots.
type fixture struct {
	t       *testing.T
	home    string
	runner  *fakeRunner
	env     *core.Env
	guard   *safety.Guard
	prov    *Provider
	inUse   map[string]string // dir -> pids
	running []string          // processes running now (ProcessGuard)
	devs    map[string]uint64 // overridden devices (external volumes)
	extra   []string
	emitted []*core.Item // emission order
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, home: home, runner: newFakeRunner(), inUse: map[string]string{}, devs: map[string]uint64{}}
	f.env = &core.Env{
		Home:     home,
		Now:      now,
		MaxDepth: 8,
		Runner:   f.runner,
		Logf:     t.Logf,
	}
	f.prov = New()
	f.prov.extra = func() []string { return f.extra }
	f.prov.cwdInside = func(dir string) string {
		var pids []string
		for d, p := range f.inUse {
			if fsx.Within(d, dir) {
				pids = append(pids, p)
			}
		}
		sort.Strings(pids)
		return strings.Join(pids, ",")
	}
	f.prov.running = func(names ...string) []string {
		var hit []string
		for _, n := range names {
			if slices.Contains(f.running, n) {
				hit = append(hit, n)
			}
		}
		return hit
	}
	f.prov.devOf = func(p string) (uint64, bool) {
		for d, dev := range f.devs {
			if fsx.Within(p, d) {
				return dev, true
			}
		}
		return statDev(p)
	}
	return f
}

// path returns an absolute path inside the fake home.
func (f *fixture) path(rel string) string { return filepath.Join(f.home, rel) }

// mkdir creates directories (home-relative).
func (f *fixture) mkdir(rels ...string) {
	f.t.Helper()
	for _, r := range rels {
		if err := os.MkdirAll(f.path(r), 0o755); err != nil {
			f.t.Fatal(err)
		}
	}
}

// file writes a file (home-relative) with content, creating parents.
func (f *fixture) file(rel, content string) {
	f.t.Helper()
	p := f.path(rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// big writes a file of n bytes (real blocks, not sparse).
func (f *fixture) big(rel string, n int) {
	f.file(rel, strings.Repeat("x", n))
}

// repo turns rel into a main git repository known by the fake git.
func (f *fixture) repo(rel string, r *fakeRepo) {
	f.mkdir(rel + "/.git")
	f.file(rel+"/.git/HEAD", "ref: refs/heads/main\n")
	f.file(rel+"/.git/index", "")
	f.runner.repos[f.path(rel)] = r
}

// worktree makes rel a linked worktree of main (a .git file).
func (f *fixture) worktree(rel, main string, r *fakeRepo) {
	name := filepath.Base(rel)
	admin := f.path(main + "/.git/worktrees/" + name)
	f.mkdir(main + "/.git/worktrees/" + name)
	f.file(main+"/.git/worktrees/"+name+"/HEAD", "ref: refs/heads/wt\n")
	f.file(main+"/.git/worktrees/"+name+"/index", "")
	// What `git worktree add` writes: the registry entry points back to the
	// checkout's .git file.
	f.file(main+"/.git/worktrees/"+name+"/gitdir", f.path(rel)+"/.git\n")
	f.file(main+"/.git/worktrees/"+name+"/commondir", "../..\n")
	f.file(rel+"/.git", "gitdir: "+admin+"\n")
	f.runner.repos[f.path(rel)] = r
}

// touchAll sets the mtime of rel and everything below it (bottom-up).
func (f *fixture) touchAll(rel string, t time.Time) {
	f.t.Helper()
	var paths []string
	_ = filepath.Walk(f.path(rel), func(p string, info os.FileInfo, err error) error {
		if err == nil && info.Mode()&os.ModeSymlink == 0 {
			paths = append(paths, p)
		}
		return nil
	})
	for i := len(paths) - 1; i >= 0; i-- {
		if err := os.Chtimes(paths[i], t, t); err != nil {
			f.t.Fatal(err)
		}
	}
}

// touch sets the mtime of one path.
func (f *fixture) touch(rel string, t time.Time) {
	f.t.Helper()
	if err := os.Chtimes(f.path(rel), t, t); err != nil {
		f.t.Fatal(err)
	}
}

// setup wires roots, the protection function and the guard like the CLI.
func (f *fixture) setup(roots, wtRoots []string, exclude ...string) {
	f.env.Roots, f.env.WorktreeRoots, f.env.Exclude = nil, nil, nil
	for _, r := range roots {
		f.env.Roots = append(f.env.Roots, f.path(r))
	}
	for _, r := range wtRoots {
		f.env.WorktreeRoots = append(f.env.WorktreeRoots, f.path(r))
	}
	for _, x := range exclude {
		f.env.Exclude = append(f.env.Exclude, f.path(x))
	}
	all := append(append([]string(nil), f.env.Roots...), f.env.WorktreeRoots...)
	f.guard = safety.New(f.home, "", all, f.env.Exclude)
	scanGuard := safety.New(f.home, "", nil, f.env.Exclude)
	f.env.Protected = func(p string) bool {
		if scanGuard.Protected(p) {
			return true
		}
		for _, r := range all {
			if fsx.Within(r, p) {
				return true
			}
		}
		return false
	}
}

// scan runs the provider and returns the final items by path (group items
// by Location) plus by ID.
func (f *fixture) scan() (byPath map[string]*core.Item, byID map[string]*core.Item) {
	f.t.Helper()
	var mu sync.Mutex
	f.emitted = nil
	byID = map[string]*core.Item{}
	err := f.prov.Scan(context.Background(), f.env, func(it *core.Item) {
		mu.Lock()
		defer mu.Unlock()
		f.emitted = append(f.emitted, it)
		byID[it.ID] = it
	})
	if err != nil {
		f.t.Fatal(err)
	}
	byPath = map[string]*core.Item{}
	for _, it := range byID {
		if it.Sizing {
			f.t.Errorf("item left sizing: %s", it.ID)
		}
		k := it.Path
		if k == "" {
			k = it.Location
		}
		if old, ok := byPath[k]; ok {
			f.t.Errorf("two items for %s: %s and %s", k, old.ID, it.ID)
		}
		byPath[k] = it
	}
	return byPath, byID
}

// rel shows item paths home-relative in failure messages.
func (f *fixture) rels(items map[string]*core.Item) []string {
	var out []string
	for p, it := range items {
		r, _ := filepath.Rel(f.home, p)
		out = append(out, r+" ("+it.Kind+")")
	}
	sort.Strings(out)
	return out
}
