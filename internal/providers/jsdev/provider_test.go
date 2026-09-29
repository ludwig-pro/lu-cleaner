package jsdev

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
	"github.com/ludwig-pro/lu-cleaner/internal/providers/catalog"
	"github.com/ludwig-pro/lu-cleaner/internal/safety"
	"golang.org/x/sys/unix"
)

// ------------------------------------------------------------------ fakes

type fakeRunner struct {
	mu    sync.Mutex
	out   map[string]string // "name arg..." -> stdout
	look  map[string]string // LookPath results
	calls []string
}

func (f *fakeRunner) Output(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	key := strings.TrimSpace(name + " " + strings.Join(args, " "))
	f.mu.Lock()
	f.calls = append(f.calls, key)
	f.mu.Unlock()
	if o, ok := f.out[key]; ok {
		return []byte(o), nil
	}
	return nil, errors.New("fake: no output for " + key)
}

func (f *fakeRunner) LookPath(name string) (string, error) {
	if p, ok := f.look[name]; ok {
		return p, nil
	}
	return "", exec.ErrNotFound
}

// ------------------------------------------------------------------ fixture helpers

type fx struct {
	t    *testing.T
	home string
	tmp  string
	now  time.Time
}

func (f *fx) abs(rel string) string {
	if strings.HasPrefix(rel, "$TMPDIR/") {
		return filepath.Join(f.tmp, strings.TrimPrefix(rel, "$TMPDIR/"))
	}
	return filepath.Join(f.home, rel)
}

func (f *fx) file(rel string, size int) string {
	p := f.abs(rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
		f.t.Fatal(err)
	}
	return p
}

func (f *fx) text(rel, content string) string {
	p := f.abs(rel)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
	return p
}

func (f *fx) dir(rel string) string {
	p := f.abs(rel)
	if err := os.MkdirAll(p, 0o755); err != nil {
		f.t.Fatal(err)
	}
	return p
}

func (f *fx) link(target, rel string) string {
	p := f.abs(rel)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.Symlink(target, p); err != nil {
		f.t.Fatal(err)
	}
	return p
}

// age sets the mtime of rel (symlinks themselves, not their target).
func (f *fx) age(rel string, d time.Duration) {
	p := f.abs(rel)
	ts := unix.NsecToTimespec(f.now.Add(-d).UnixNano())
	if err := unix.UtimesNanoAt(unix.AT_FDCWD, p, []unix.Timespec{ts, ts}, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		f.t.Fatal(err)
	}
}

func ms(t time.Time) string { return fmt.Sprint(t.UnixMilli()) }

// collect runs the provider and returns the final items by ID.
func collect(t *testing.T, p *Provider, env *core.Env) (map[string]*core.Item, int) {
	t.Helper()
	var mu sync.Mutex
	got := map[string]*core.Item{}
	placeholders := 0
	err := p.Scan(context.Background(), env, func(it *core.Item) {
		mu.Lock()
		defer mu.Unlock()
		if it.Sizing {
			placeholders++
		}
		got[it.ID] = it
	})
	if err != nil {
		t.Fatal(err)
	}
	for id, it := range got {
		if it.Sizing {
			t.Errorf("%s: final item still sizing", id)
		}
	}
	return got, placeholders
}

func byName(items map[string]*core.Item, name string) *core.Item {
	for _, it := range items {
		if it.Name == name {
			return it
		}
	}
	return nil
}

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

// ------------------------------------------------------------------ the big fixture

type world struct {
	fx
	env    *core.Env
	runner *fakeRunner
	p      *Provider
	guard  *safety.Guard
	boot   time.Time
	ms     map[string]string // multishell label -> entry name
}

func newWorld(t *testing.T) *world {
	now := time.Now()
	// Real paths (t.TempDir lives under /var -> /private/var): like $HOME on a Mac.
	home, _ := filepath.EvalSymlinks(t.TempDir())
	tmp, _ := filepath.EvalSymlinks(t.TempDir())
	w := &world{fx: fx{t: t, home: home, tmp: tmp, now: now}, ms: map[string]string{}}
	w.boot = now.Add(-72 * time.Hour)
	h := w.home

	// ---- nvm: default "20", a pinned 22, a running 24, an unused 18, a 20.11.1 with a unique global.
	w.dir(".nvm/.git") // nvm is a git checkout: its version dirs must still be cleanable
	for _, v := range []string{"v18.20.4", "v20.11.1", "v20.19.6", "v22.1.0", "v24.1.0"} {
		w.file(".nvm/versions/node/"+v+"/bin/node", 4096)
		w.dir(".nvm/versions/node/" + v + "/lib/node_modules/npm")
	}
	w.dir(".nvm/versions/node/v20.11.1/lib/node_modules/eas-cli")
	w.dir(".nvm/versions/node/v18.20.4/lib/node_modules/typescript") // also installed in a kept version
	w.dir(".nvm/versions/node/v20.19.6/lib/node_modules/typescript")
	w.text(".nvm/alias/default", "20\n")
	w.text(".nvm/alias/lts/iron", "v20.20.2\n")
	w.file(".nvm/.cache/bin/node-v18.20.4-darwin-arm64/node", 1000) // catalog's job

	// ---- fnm: default v22.22.0, alias lts-latest v24.14.0, PATH node v16.20.2 (via multishell), unused v20.18.0.
	fnm := ".local/share/fnm"
	for _, v := range []string{"v16.20.2", "v20.18.0", "v22.22.0", "v24.14.0"} {
		w.file(fnm+"/node-versions/"+v+"/installation/bin/node", 4096)
		w.dir(fnm + "/node-versions/" + v + "/installation/lib/node_modules/npm")
		for _, rel := range []string{"/installation/lib/node_modules", "/installation/bin", ""} {
			w.age(fnm+"/node-versions/"+v+rel, 30*24*time.Hour) // installed a month ago
		}
	}
	w.dir(fnm + "/node-versions/.downloads")
	w.link(filepath.Join(h, fnm, "node-versions/v22.22.0/installation"), fnm+"/aliases/default")
	w.link(filepath.Join(h, fnm, "node-versions/v24.14.0/installation"), fnm+"/aliases/lts-latest")
	// npm -g leftovers in the unused version: one old (reported), one fresh (install in progress?).
	lib := fnm + "/node-versions/v20.18.0/installation/lib/node_modules"
	w.file(lib+"/.openclaw-Hd08o9WA/package.json", 2000)
	w.age(lib+"/.openclaw-Hd08o9WA", 60*24*time.Hour)
	w.file(lib+"/@anthropic-ai/.claude-code-jK9KMe16/cli.js", 2000)
	w.age(lib+"/@anthropic-ai/.claude-code-jK9KMe16", 3*24*time.Hour)
	w.file(lib+"/.fresh-ABCDEFGH/package.json", 10)

	// ---- mise: 20.20.1 (unused, with alias symlinks), 24.20.0 (pinned by a project).
	mise := ".local/share/mise/installs/node"
	w.file(mise+"/20.20.1/bin/node", 4096)
	w.file(mise+"/24.20.0/bin/node", 4096)
	w.link("./20.20.1", mise+"/20")
	w.link("./20.20.1", mise+"/lts-iron")
	w.link("./24.20.0", mise+"/24")

	// ---- projects (roots) with pins; one Plug'n'Play project.
	w.text("code/app1/.nvmrc", "22\n")
	w.text("code/group/app2/.node-version", "24.20.0\n")
	w.text("code/a/b/c/d/.nvmrc", "18\n") // too deep: ignored
	w.text("code/app1/node_modules/x/.nvmrc", "18\n")
	w.text("code/pnpapp/.pnp.cjs", "// pnp\n")
	w.text("code/app1/package.json", `{"dependencies":{"expo":"~56.0.0","react-native":"0.86.3"}}`)
	w.text("code/group/app2/package.json", `{"devDependencies":{"expo":"^56.0.0"}}`)
	w.text("code/web/package.json", `{"dependencies":{"next":"15.0.0"}}`)

	// ---- fnm multishells.
	msDir := ".local/state/fnm_multishells"
	target := filepath.Join(h, fnm, "aliases/default")
	add := func(label string, pid int, created time.Time, mtimeAge time.Duration) string {
		name := fmt.Sprintf("%d_%s", pid, ms(created))
		w.link(target, msDir+"/"+name)
		w.age(msDir+"/"+name, mtimeAge)
		w.ms[label] = name
		return name
	}
	add("preboot", 11111, w.boot.Add(-10*24*time.Hour), now.Sub(w.boot.Add(-10*24*time.Hour)))
	add("referenced", 22222, w.boot.Add(-5*24*time.Hour), now.Sub(w.boot.Add(-5*24*time.Hour)))
	add("alive", os.Getpid(), now.Add(-48*time.Hour), 48*time.Hour)
	add("dead-old", 99999, now.Add(-48*time.Hour), 48*time.Hour)
	add("process-near", 99998, now.Add(-30*time.Hour), 30*time.Hour)
	add("recent", 99997, now.Add(-time.Hour), time.Hour)
	own := add("own", 99996, now.Add(-50*time.Hour), 50*time.Hour)
	// fnm use v16 in our own shell: repoint our entry to v16 (it is the `node` on PATH).
	os.Remove(w.abs(msDir + "/" + own))
	w.link(filepath.Join(h, fnm, "node-versions/v16.20.2/installation"), msDir+"/"+own)
	w.age(msDir+"/"+own, 50*time.Hour)
	w.text(msDir+"/notes.txt", "not an entry")

	// ---- npm cache & npx.
	w.file(".npm/_cacache/content-v2/x", 5000) // catalog's job
	w.text(".npm/_npx/aaaa1111/package.json", `{"dependencies":{"foo-mcp":"^1"},"_npx":{"packages":["foo-mcp@latest"]}}`)
	w.file(".npm/_npx/aaaa1111/node_modules/foo-mcp/index.js", 3000)
	w.text(".npm/_npx/bbbb2222/package.json", `{"dependencies":{"old-tool":"^1"}}`)
	w.file(".npm/_npx/bbbb2222/node_modules/old-tool/index.js", 3000)
	for _, rel := range []string{".npm/_npx/bbbb2222/package.json", ".npm/_npx/bbbb2222/node_modules/old-tool/index.js",
		".npm/_npx/bbbb2222/node_modules/old-tool", ".npm/_npx/bbbb2222/node_modules", ".npm/_npx/bbbb2222"} {
		w.age(rel, 40*24*time.Hour)
	}
	w.link(filepath.Join(h, "nowhere"), ".npm/_npx/cccc3333") // dangling: never proposed

	// ---- pnpm stores (current v10 + stale v3).
	w.file("Library/pnpm/store/v10/files/00/aaa", 8000)
	w.dir("Library/pnpm/store/v10/index")
	w.file("Library/pnpm/store/v3/files/00/bbb", 8000)

	// ---- Yarn Berry global folder.
	w.file(".yarn/berry/cache/pkg-npm-1.0.0-abc.zip", 9000)
	w.file(".yarn/berry/metadata/npm/1234/react.json", 3000)
	w.file(".yarn/berry/store/v1/ab/abc.dat", 3000)
	w.dir(".yarn/berry/index")

	// ---- Expo.
	for _, v := range []string{"54.0.6", "56.0.2", "56.0.3"} {
		w.file(".expo/ios-simulator-app-cache/Expo-Go-"+v+".tar.app/Expo Go", 7000)
	}
	w.text(".expo/state.json", `{"auth":"secret"}`)
	w.dir(".expo/codesigning/key")

	// ---- Playwright with .links (one live, one gone).
	pw := "Library/Caches/ms-playwright"
	for _, d := range []string{"chromium-1234", "chromium-1243", "chromium_headless_shell-1243", "ffmpeg-1011"} {
		w.file(pw+"/"+d+"/bin", 6000)
	}
	core1 := w.text("code/app1/node_modules/playwright-core/browsers.json",
		`{"browsers":[{"name":"chromium","revision":"1243"},{"name":"chromium-headless-shell","revision":"1243"},{"name":"ffmpeg","revision":"1011"},{"name":"webkit","revision":"2203","revisionOverrides":{"mac12":"2009"}}]}`)
	w.text(pw+"/.links/aaa", filepath.Dir(core1))
	w.text(pw+"/.links/bbb", filepath.Join(h, "deleted-worktree/node_modules/playwright-core"))
	w.dir(pw + "/b/browser@123") // MCP profile: not a revision, never touched

	// ---- $TMPDIR signatures.
	w.file("$TMPDIR/rnsvgAbC123/out/rnsvgJSI.h", 1500)
	w.age("$TMPDIR/rnsvgAbC123", 26*time.Hour)
	w.file("$TMPDIR/pagerviewXyZ789/out/react/renderer/components/x.h", 1500)
	w.age("$TMPDIR/pagerviewXyZ789", 26*time.Hour)
	w.file("$TMPDIR/recentLibAbCdEf/out/recentLibJSI.h", 1500) // too recent
	w.dir("$TMPDIR/com.apple.fooAbCdEf/data")                  // no signature
	w.age("$TMPDIR/com.apple.fooAbCdEf", 26*time.Hour)
	w.file("$TMPDIR/-6BojbkG70SWkgUAJVw7s/ssr/0123456789abcdef0123456789abcdef01234567.js", 1200)
	w.age("$TMPDIR/-6BojbkG70SWkgUAJVw7s", 26*time.Hour)
	w.file("$TMPDIR/AbCdEfGhIjKlMnOpQrStU/other/x", 10) // nanoid name, wrong content
	w.age("$TMPDIR/AbCdEfGhIjKlMnOpQrStU", 26*time.Hour)
	w.file("$TMPDIR/jest_dx/haste-map-x", 4000) // catalog's job

	// ---- process snapshot.
	own0 := filepath.Join(h, msDir, own)
	w.runner = &fakeRunner{
		out: map[string]string{
			"/bin/ps -axww -o args=": strings.Join([]string{
				"/sbin/launchd",
				"node " + filepath.Join(h, ".npm/_npx/aaaa1111/node_modules/.bin/foo-mcp") + " --stdio",
				"/bin/zsh -l",
			}, "\n"),
			"/bin/ps -axwwE -o command=": strings.Join([]string{
				"/sbin/launchd",
				"/Applications/Cursor.app/Contents/MacOS/Cursor PATH=" + filepath.Join(h, msDir, w.ms["referenced"]) + "/bin:/usr/bin HOME=" + h,
			}, "\n"),
			// one process started ~1 min before the "process-near" entry was created
			"/bin/ps -axo etime=":                         "00:05\n01:00:00\n1-06:01:00\n",
			"/usr/sbin/lsof -nP -w -c node -a -d txt -Fn": "p123\nfcwd\nn" + filepath.Join(h, ".nvm/versions/node/v24.1.0/bin/node") + "\n",
			"pnpm store path":                             filepath.Join(h, "Library/pnpm/store/v10") + "\n",
			"watchman --no-spawn --no-pretty watch-list":  `{"version":"2024.12.02","roots":["` + filepath.Join(h, "code/app1") + `","` + filepath.Join(h, "deleted-worktree") + `"]}`,
		},
		look: map[string]string{
			"node":     filepath.Join(own0, "bin/node"),
			"pnpm":     "/usr/local/bin/pnpm",
			"watchman": "/opt/homebrew/bin/watchman",
		},
	}

	env := core.NewEnv()
	env.Home, env.TmpDir, env.Now = h, w.tmp, now
	env.Roots = []string{filepath.Join(h, "code")}
	env.Runner = w.runner
	scanGuard := safety.New(h, w.tmp, nil, nil)
	roots := env.Roots
	env.Protected = func(p string) bool {
		if scanGuard.Protected(p) {
			return true
		}
		for _, r := range roots {
			if fsx.Within(strings.ToLower(r), strings.ToLower(p)) {
				return true
			}
		}
		return false
	}
	w.env = env
	w.guard = safety.New(h, w.tmp, roots, nil)
	vars := map[string]string{"FNM_MULTISHELL_PATH": own0}
	w.p = &Provider{
		getenv:   func(k string) string { return vars[k] },
		alive:    func(pid int) bool { return pid == os.Getpid() },
		bootTime: func() time.Time { return w.boot },
	}
	return w
}

// ------------------------------------------------------------------ tests

func TestNodeVersions(t *testing.T) {
	w := newWorld(t)
	items, placeholders := collect(t, w.p, w.env)
	if placeholders == 0 {
		t.Errorf("expected Sizing placeholders before measured items")
	}
	type want struct {
		risk       core.Risk
		rec        bool
		selectable bool
		warn       string // substring ("" = no warning)
	}
	cases := map[string]want{
		"node v18.20.4 (nvm)":  {core.RiskModerate, true, true, ""},                              // unused, globals also elsewhere
		"node v20.11.1 (nvm)":  {core.RiskModerate, false, true, "installed only here: eas-cli"}, // unused but eas-cli only here
		"node v20.19.6 (nvm)":  {core.RiskCaution, false, true, "nvm default"},                   // default "20"
		"node v22.1.0 (nvm)":   {core.RiskModerate, false, true, "pinned by 1 project"},          // .nvmrc 22
		"node v24.1.0 (nvm)":   {core.RiskCaution, false, false, "running"},                      // lsof
		"node v16.20.2 (fnm)":  {core.RiskCaution, false, true, "`node` on your PATH"},           // PATH through multishell
		"node v20.18.0 (fnm)":  {core.RiskModerate, false, true, ""},                             // unused, but the newest unreferenced fnm version (keep_latest)
		"node v22.22.0 (fnm)":  {core.RiskCaution, false, true, "fnm default"},                   // alias default
		"node v24.14.0 (fnm)":  {core.RiskModerate, false, true, "pinned"},                       // pin 24.20.0 -> newest 24 + lts-latest alias
		"node v20.20.1 (mise)": {core.RiskModerate, false, true, ""},                             // unused, but the newest unreferenced mise version (keep_latest)
		"node v24.20.0 (mise)": {core.RiskModerate, false, true, "pinned by 1 project"},          // exact pin
	}
	for name, wnt := range cases {
		it := byName(items, name)
		if it == nil {
			t.Errorf("%s: missing", name)
			continue
		}
		if it.Kind != "node-version" || it.Risk != wnt.risk || it.Recommended != wnt.rec || it.Selectable != wnt.selectable {
			t.Errorf("%s: kind=%s risk=%s rec=%v selectable=%v (warn %q), want risk=%s rec=%v selectable=%v",
				name, it.Kind, it.Risk, it.Recommended, it.Selectable, it.Warn, wnt.risk, wnt.rec, wnt.selectable)
		}
		if wnt.warn == "" && it.Warn != "" || wnt.warn != "" && !strings.Contains(it.Warn, wnt.warn) {
			t.Errorf("%s: warn %q, want %q", name, it.Warn, wnt.warn)
		}
		if it.Size <= 0 || it.Note == "" || it.Meta["manager"] == "" {
			t.Errorf("%s: size=%d note=%q meta=%v", name, it.Size, it.Note, it.Meta)
		}
	}
	if n := len(byKind(items, "node-version")); n != len(cases) {
		t.Errorf("node-version items = %d, want %d", n, len(cases))
	}
	// Globals: the unique one is reported.
	if it := byName(items, "node v20.11.1 (nvm)"); it != nil && it.Meta["globals_only_here"] != "eas-cli" {
		t.Errorf("globals_only_here = %q", it.Meta["globals_only_here"])
	}
	// mise versions take their alias symlinks with them.
	it := byName(items, "node v20.20.1 (mise)")
	if it == nil || it.Path != "" || len(it.Paths) != 3 {
		t.Fatalf("mise group item = %+v", it)
	}
	for _, p := range it.Paths[1:] {
		if fi, err := os.Lstat(p); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("extra path %s should be an alias symlink", p)
		}
	}
	// fnm v22 (default) last use comes from the multishell links.
	if it := byName(items, "node v22.22.0 (fnm)"); it != nil && it.Meta["last_used_source"] != "fnm shell link" {
		t.Errorf("fnm last use: %v", it.Meta)
	}
}

func TestNpmLeftovers(t *testing.T) {
	w := newWorld(t)
	items, _ := collect(t, w.p, w.env)
	got := byKind(items, "npm-global-leftover")
	var names []string
	for _, it := range got {
		names = append(names, filepath.Base(it.Path))
		if it.Risk != core.RiskSafe || !it.Recommended || it.Size == 0 {
			t.Errorf("%s: risk=%s rec=%v size=%d", it.Name, it.Risk, it.Recommended, it.Size)
		}
	}
	sort.Strings(names)
	if strings.Join(names, ",") != ".claude-code-jK9KMe16,.openclaw-Hd08o9WA" {
		t.Errorf("leftovers = %v (the fresh one must be skipped)", names)
	}
}

func TestMultishells(t *testing.T) {
	w := newWorld(t)
	items, _ := collect(t, w.p, w.env)
	got := byKind(items, "fnm-multishells")
	if len(got) != 1 {
		t.Fatalf("want one multishell group, got %d", len(got))
	}
	it := got[0]
	var names []string
	for _, p := range it.Paths {
		names = append(names, filepath.Base(p))
	}
	sort.Strings(names)
	want := []string{w.ms["preboot"], w.ms["dead-old"]}
	sort.Strings(want)
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("stale entries = %v, want %v", names, want)
	}
	for _, keep := range []string{"referenced", "alive", "process-near", "recent", "own"} {
		for _, n := range names {
			if n == w.ms[keep] {
				t.Errorf("%s entry %s must be kept", keep, n)
			}
		}
	}
	if it.Risk != core.RiskSafe || !it.Recommended || it.Files != 2 || it.Path != "" || it.Meta["kept_in_use"] != "5" {
		t.Errorf("group item = %+v", it)
	}
}

func TestMultishellsFailClosed(t *testing.T) {
	w := newWorld(t)
	delete(w.runner.out, "/bin/ps -axwwE -o command=")
	items, _ := collect(t, w.p, w.env)
	if n := len(byKind(items, "fnm-multishells")); n != 0 {
		t.Errorf("without the process environment nothing may be proposed, got %d items", n)
	}
	// npx recommendations need the process list too.
	for _, it := range byKind(items, "npx-package") {
		if it.Recommended {
			t.Errorf("%s recommended without process list", it.Name)
		}
	}
}

func TestPackageManagers(t *testing.T) {
	w := newWorld(t)
	items, _ := collect(t, w.p, w.env)

	// npx: running one warned, old one recommended, dangling one ignored.
	npx := byKind(items, "npx-package")
	if len(npx) != 2 {
		t.Fatalf("npx items = %d", len(npx))
	}
	run, old := byName(items, "npx cache · foo-mcp@latest"), byName(items, "npx cache · old-tool")
	if run == nil || run.Warn == "" || run.Recommended || run.Risk != core.RiskModerate {
		t.Errorf("running npx = %+v", run)
	}
	if old == nil || old.Warn != "" || !old.Recommended {
		t.Errorf("old npx = %+v", old)
	}

	// pnpm: both stores, prune only for the current one.
	stores := byKind(items, "pnpm-store")
	if len(stores) != 2 {
		t.Fatalf("pnpm stores = %d", len(stores))
	}
	prune := byKind(items, "pnpm-store-prune")
	if len(prune) != 1 || prune[0].Method != core.MethodCommand || strings.Join(prune[0].Command, " ") != "pnpm store prune" ||
		!prune[0].Recommended || prune[0].Risk != core.RiskSafe || prune[0].Size != 0 || !strings.HasSuffix(prune[0].Location, "v10") ||
		prune[0].Covers != filepath.Join(prune[0].Location, "files") {
		t.Errorf("prune = %+v", prune)
	}
	for _, st := range stores {
		if st.Risk != core.RiskModerate || st.Method != core.MethodDelete || st.Recommended {
			t.Errorf("store %s: %+v", st.Name, st)
		}
	}

	// Yarn Berry: a PnP project reads the global cache at runtime.
	cache := byKind(items, "yarn-berry-cache")
	if len(cache) != 1 || cache[0].Risk != core.RiskCaution || !cache[0].NoRecommend || cache[0].Warn == "" ||
		cache[0].Meta["pnp_projects"] == "" {
		t.Errorf("berry cache = %+v", cache)
	}
	if m := byKind(items, "yarn-berry-metadata"); len(m) != 1 || m[0].Risk != core.RiskSafe {
		t.Errorf("berry metadata = %+v", m)
	}
	if s := byKind(items, "yarn-berry-store"); len(s) != 1 || len(s[0].Paths) != 2 {
		t.Errorf("berry store = %+v", s)
	}

	// watchman: one stale root.
	if wm := byKind(items, "watchman-stale-watches"); len(wm) != 1 || wm[0].Method != core.MethodCommand ||
		!strings.Contains(wm[0].Name, "1 watch") ||
		strings.Join(wm[0].Command, " ") != "watchman --no-spawn watch-del "+filepath.Join(w.home, "deleted-worktree") {
		t.Errorf("watchman = %+v", wm)
	}
}

func TestPnpmWithoutCLI(t *testing.T) {
	w := newWorld(t)
	delete(w.runner.look, "pnpm")
	delete(w.runner.look, "watchman")
	os.Remove(filepath.Join(w.home, "code/pnpapp/.pnp.cjs"))
	items, _ := collect(t, w.p, w.env)
	if n := len(byKind(items, "pnpm-store")); n != 2 {
		t.Errorf("stores must be found from known paths, got %d", n)
	}
	if n := len(byKind(items, "pnpm-store-prune")); n != 0 {
		t.Errorf("no prune without a working pnpm")
	}
	if n := len(byKind(items, "watchman-stale-watches")); n != 0 {
		t.Errorf("no watchman item without watchman")
	}
	for _, c := range w.runner.calls {
		if strings.HasPrefix(c, "pnpm") || strings.HasPrefix(c, "watchman") {
			t.Errorf("unexpected call %q", c)
		}
	}
	if c := byKind(items, "yarn-berry-cache"); len(c) != 1 || c[0].Risk != core.RiskSafe {
		t.Errorf("berry cache without PnP projects must be safe: %+v", c)
	}
}

func TestExpoAndPlaywright(t *testing.T) {
	w := newWorld(t)
	items, _ := collect(t, w.p, w.env)
	for name, want := range map[string]struct {
		rec  bool
		risk core.Risk
		warn string
	}{
		"Expo Go 56.0.2 (iOS simulator build)": {true, core.RiskSafe, ""},                                 // superseded
		"Expo Go 56.0.3 (iOS simulator build)": {false, core.RiskModerate, "SDK 56 is used by 2 project"}, // in use
		"Expo Go 54.0.6 (iOS simulator build)": {false, core.RiskModerate, ""},                            // newest 54, unused
	} {
		it := byName(items, name)
		if it == nil || it.Recommended != want.rec || it.Risk != want.risk ||
			(want.warn == "") != (it.Warn == "") || !strings.Contains(it.Warn, want.warn) {
			t.Errorf("%s: %+v", name, it)
		}
	}
	for name, rec := range map[string]bool{
		"Playwright chromium r1234":                true,
		"Playwright chromium r1243":                false,
		"Playwright chromium headless shell r1243": false,
		"Playwright ffmpeg r1011":                  false,
	} {
		it := byName(items, name)
		if it == nil || it.Recommended != rec || it.Risk != core.RiskModerate {
			t.Errorf("%s: %+v", name, it)
			continue
		}
		if needed := !rec; needed != strings.HasPrefix(it.Warn, "needed by 1 installed Playwright") {
			t.Errorf("%s: warn %q", name, it.Warn)
		}
	}
	if n := len(byKind(items, "playwright-browser")); n != 4 {
		t.Errorf("playwright items = %d", n)
	}
}

func TestTmpSignatures(t *testing.T) {
	w := newWorld(t)
	items, _ := collect(t, w.p, w.env)
	cg := byKind(items, "rn-codegen-tmp")
	if len(cg) != 1 || len(cg[0].Paths) != 2 || !cg[0].Recommended {
		t.Fatalf("codegen = %+v", cg)
	}
	for _, p := range cg[0].Paths {
		if b := filepath.Base(p); b != "rnsvgAbC123" && b != "pagerviewXyZ789" {
			t.Errorf("unexpected codegen dir %s", b)
		}
	}
	vt := byKind(items, "vitest-tmp")
	if len(vt) != 1 || filepath.Base(vt[0].Path) != "-6BojbkG70SWkgUAJVw7s" {
		t.Errorf("vitest = %+v", vt)
	}
}

// TestSafety: nothing protected, excluded, dangling or shared with the catalog
// is ever proposed, and every cleanable target passes the safety guard.
func TestSafety(t *testing.T) {
	w := newWorld(t)
	w.env.Exclude = []string{filepath.Join(w.home, ".yarn/berry/metadata")}
	items, _ := collect(t, w.p, w.env)
	seen := map[string]string{}
	for id, it := range items {
		if !strings.HasPrefix(id, "js:"+it.Kind+":") || it.Provider != "js" || it.Category != core.CatJS {
			t.Errorf("%s: bad id/provider/category", id)
		}
		if it.Name == "" || it.Note == "" {
			t.Errorf("%s: name and note required", id)
		}
		for _, p := range it.Targets() {
			if prev, dup := seen[p]; dup {
				t.Errorf("%s emitted twice (%s, %s)", p, prev, id)
			}
			seen[p] = id
			if w.env.IsProtected(p) || w.env.Excluded(p) {
				t.Errorf("%s: protected/excluded target %s", id, p)
			}
			if strings.Contains(p, ".expo/state.json") || strings.Contains(p, "codesigning") || strings.HasSuffix(p, "/.expo") {
				t.Errorf("%s: Expo credentials proposed: %s", id, p)
			}
			if it.CanClean() {
				if err := w.guard.Check(p, safety.Options{AllowGitRepo: it.AllowGitRepo}); err != nil {
					t.Errorf("%s: guard refuses %s: %v", id, p, err)
				}
			}
		}
		if it.Method == core.MethodCommand && len(it.Command) == 0 {
			t.Errorf("%s: command item without command", id)
		}
		if it.Recommended && it.Warn != "" {
			t.Errorf("%s: recommended despite warning %q (smart select would pick it)", id, it.Warn)
		}
	}
	if len(byKind(items, "yarn-berry-metadata")) != 0 {
		t.Errorf("excluded path proposed")
	}
	if _, ok := seen[filepath.Join(w.home, ".npm/_npx/cccc3333")]; ok {
		t.Errorf("dangling symlink proposed")
	}
	// The static catalog must never cover the same paths.
	var mu sync.Mutex
	cat := catalog.New()
	if err := cat.Scan(context.Background(), w.env, func(it *core.Item) {
		mu.Lock()
		defer mu.Unlock()
		for _, p := range it.Targets() {
			if id, dup := seen[p]; dup {
				t.Errorf("%s is emitted by both %s and catalog %s", p, id, it.ID)
			}
			for q, id := range seen {
				if fsx.Within(q, p) && it.CanClean() && strings.HasPrefix(it.Kind, "js-") {
					t.Errorf("catalog %s (%s) contains provider target %s (%s)", it.ID, p, q, id)
				}
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
}

// TestIDStability: two scans give the same IDs.
func TestIDStability(t *testing.T) {
	w := newWorld(t)
	a, _ := collect(t, w.p, w.env)
	b, _ := collect(t, w.p, w.env)
	ka, kb := sortedKeys(a), sortedKeys(b)
	if strings.Join(ka, "\n") != strings.Join(kb, "\n") {
		t.Errorf("IDs differ between scans:\n%v\n%v", ka, kb)
	}
	if len(ka) < 30 {
		t.Errorf("suspiciously few items: %d", len(ka))
	}
}

func TestExternalVolume(t *testing.T) {
	w := newWorld(t)
	ext := filepath.Join(w.home, "Library/Caches/ms-playwright/chromium-1234")
	w.p.devOf = func(p string) (uint64, bool) {
		if fsx.Within(p, ext) {
			return 2, true
		}
		return 1, true
	}
	items, _ := collect(t, w.p, w.env)
	it := byName(items, "Playwright chromium r1234")
	if it == nil || it.Method != core.MethodReport || it.Warn != externalWarn || it.Recommended || it.CanClean() {
		t.Errorf("external item = %+v", it)
	}
	if it2 := byName(items, "Playwright chromium r1243"); it2 == nil || it2.Method != core.MethodDelete {
		t.Errorf("internal item = %+v", it2)
	}
}

// A cache dir that is a symlink is proposed through its real directory
// (deleting the link would free nothing); a dangling one is never proposed.
func TestSymlinkedCacheResolvesToRealDir(t *testing.T) {
	w := newWorld(t)
	os.RemoveAll(filepath.Join(w.home, ".yarn/berry/cache"))
	real := filepath.Dir(w.file("elsewhere/berry-cache/x.zip", 5000))
	w.link(real, ".yarn/berry/cache")
	os.RemoveAll(filepath.Join(w.home, ".yarn/berry/metadata"))
	w.link(filepath.Join(w.home, "missing-volume/metadata"), ".yarn/berry/metadata")
	items, _ := collect(t, w.p, w.env)
	c := byKind(items, "yarn-berry-cache")
	if len(c) != 1 || c[0].Path != real || c[0].Size == 0 {
		t.Errorf("symlinked cache = %+v, want path %s", c, real)
	}
	if m := byKind(items, "yarn-berry-metadata"); len(m) != 0 {
		t.Errorf("dangling symlink proposed: %+v", m[0])
	}
}

func TestCancelled(t *testing.T) {
	w := newWorld(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.p.Scan(ctx, w.env, func(*core.Item) {}); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
}

func TestEmptyHome(t *testing.T) {
	env := core.NewEnv()
	env.Home, env.TmpDir = t.TempDir(), t.TempDir()
	env.Runner = &fakeRunner{out: map[string]string{}, look: map[string]string{}}
	p := &Provider{getenv: func(string) string { return "" }}
	items, _ := collect(t, p, env)
	if len(items) != 0 {
		t.Errorf("empty home produced %d items", len(items))
	}
}

// TestUserProtect: a user "protect" path (config) is never proposed.
func TestUserProtect(t *testing.T) {
	w := newWorld(t)
	protected := []string{
		filepath.Join(w.home, ".nvm/versions/node/v18.20.4"),
		filepath.Join(w.home, ".yarn/berry"),
	}
	g := safety.New(w.home, w.tmp, nil, protected)
	w.env.Protected = g.Protected
	items, _ := collect(t, w.p, w.env)
	for id, it := range items {
		for _, p := range it.Targets() {
			for _, pr := range protected {
				if fsx.Within(p, pr) || fsx.Within(pr, p) {
					t.Errorf("%s proposes %s (protected %s)", id, p, pr)
				}
			}
		}
	}
	if byName(items, "node v18.20.4 (nvm)") != nil || len(byKind(items, "yarn-berry-cache")) != 0 {
		t.Errorf("protected items emitted")
	}
}

// A multishell entry that is a directory (unknown layout) is never proposed.
func TestMultishellNonSymlinkKept(t *testing.T) {
	w := newWorld(t)
	name := fmt.Sprintf("12345_%s", ms(w.boot.Add(-20*24*time.Hour)))
	w.file(".local/state/fnm_multishells/"+name+"/bin/node", 10)
	items, _ := collect(t, w.p, w.env)
	for _, it := range byKind(items, "fnm-multishells") {
		for _, p := range it.Paths {
			if filepath.Base(p) == name {
				t.Errorf("directory entry proposed")
			}
		}
	}
}
