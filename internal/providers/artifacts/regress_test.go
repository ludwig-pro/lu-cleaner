package artifacts

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
)

// Regression tests of the adversarial review (one per finding id).

// artifact-deletes-dirty-linked-worktree: a linked worktree checked out as
// dist/ (GitHub Pages pattern) is a checkout, not a build output.
func TestLinkedWorktreeNamedLikeArtifactIsNotProposed(t *testing.T) {
	old := ignoredMin
	ignoredMin = 1
	defer func() { ignoredMin = old }()

	f := newFixture(t)
	// dist/ IS a linked worktree (gh-pages) with uncommitted work.
	f.repo("src/site", &fakeRepo{ignored: []string{"dist/", "node_modules/"}})
	f.file("src/site/package.json", "{}")
	f.worktree("src/site/dist", "src/site", &fakeRepo{modified: []string{"index.html"}})
	f.file("src/site/dist/index.html", "<html>edited")
	f.file("src/site/dist/about.html", "<html>new")
	f.file("src/site/dist/app.js.map", "{}") // looks like strong build output
	f.big("src/site/dist/big.bin", 64<<10)
	// build/ holds a linked worktree the walk never enters (build/site).
	f.repo("src/site2", &fakeRepo{ignored: []string{"build/"}})
	f.file("src/site2/package.json", "{}")
	f.file("src/site2/build/index.html", "<html>")
	f.worktree("src/site2/build/site", "src/site2", &fakeRepo{})
	f.file("src/site2/build/site/index.html", "<html>")
	// dist/ holds a full clone.
	f.repo("src/site3", &fakeRepo{ignored: []string{"dist/"}})
	f.file("src/site3/package.json", "{}")
	f.file("src/site3/dist/index.html", "<html>")
	f.file("src/site3/dist/clone/.git/HEAD", "ref: refs/heads/main\n")
	f.file("src/site3/dist/clone/notes.md", "work")
	// A plain node_modules for the Recheck.
	f.file("src/site3/node_modules/a.js", "a")
	f.runner.repos[f.path("src/site3")].ignored = append(f.runner.repos[f.path("src/site3")].ignored, "node_modules/")
	f.setup([]string{"src"}, nil)
	f.touchAll("src", ago(40*day))
	items, _ := f.scan()

	for _, rel := range []string{"src/site/dist", "src/site2/build", "src/site2/build/site"} {
		if it := items[f.path(rel)]; it != nil && it.CanClean() {
			t.Errorf("%s is (or holds) a git worktree: must never be cleanable (%s, warn %q)", rel, it.Kind, it.Warn)
		}
	}
	if it := items[f.path("src/site3/dist")]; it == nil || it.CanClean() || core.Recommend(it, now, 14*day) || !strings.Contains(it.Warn, "git repository (dist/clone)") {
		t.Errorf("dist holding a clone must be report-only: %+v", it)
	}
	nm := items[f.path("src/site3/node_modules")]
	if nm == nil || nm.Recheck == nil {
		t.Fatalf("node_modules: %+v", nm)
	}
	if err := nm.Recheck(context.Background()); err != nil {
		t.Errorf("Recheck of a plain node_modules: %v", err)
	}
	f.file("src/site3/node_modules/.git", "gitdir: /elsewhere\n") // became a checkout after the scan
	if err := nm.Recheck(context.Background()); err == nil {
		t.Errorf("Recheck must refuse a target that now holds a .git entry")
	}
}

// nogit-source-build-deleted / artifacts-nongit-build-source-deleted: outside
// git, *.js / *.html / assets do not prove a folder is build output.
func TestNonGitBuildFoldersNeedGeneratorEvidence(t *testing.T) {
	f := newFixture(t)
	weak := map[string][]string{
		"src/nogit/build":     {"make.js", "data.bin"},
		"src/lib-nogit/dist":  {"index.mjs", "data.bin"},
		"src/vue2/build":      {"build.js", "webpack.prod.conf.js", "utils.js", "data.bin"},
		"src/electron/build":  {"notarize.js", "entitlements.mac.plist", "icon.icns", "data.bin"},
		"src/wp/dist":         {"index.html", "data.bin"},
		"src/assets-only/out": {"assets/logo.png", "index.html", "data.bin"},
		"src/dated/build":     {"release-20240101.js", "assets/notes-24-01-01.js", "data.bin"},
	}
	strong := map[string][]string{
		"src/vite/dist":  {"index.html", "assets/index-B1a2C3d4.js", "data.bin"},
		"src/cra/build":  {"asset-manifest.json", "index.html", "data.bin"},
		"src/maps/dist":  {"index.js", "index.js.map", "data.bin"},
		"src/wpack/dist": {"main.3f2a1b9c.js", "data.bin"},
	}
	for _, set := range []map[string][]string{weak, strong} {
		for dir, files := range set {
			f.file(dir+"/../package.json", `{"name":"x"}`)
			for _, n := range files {
				if n == "data.bin" {
					f.big(dir+"/"+n, 2<<20) // big enough for smart select
				} else {
					f.file(dir+"/"+n, "// hand-written")
				}
			}
		}
	}
	f.setup([]string{"src"}, nil)
	f.touchAll("src", ago(9*day))
	items, _ := f.scan()
	for dir := range weak {
		it := items[f.path(dir)]
		if it == nil {
			t.Errorf("%s: missing (it must be shown, as caution)", dir)
			continue
		}
		if it.Risk != core.RiskCaution || !it.NoRecommend || it.Recommended || core.Recommend(it, now, 14*day) ||
			!strings.Contains(it.Warn, "not under git") || it.Meta["evidence"] != "weak" {
			t.Errorf("%s: weak evidence outside git must be caution, never preselected: risk=%s rec=%v warn=%q meta=%v",
				dir, it.Risk, it.Recommended, it.Warn, it.Meta)
		}
	}
	for dir := range strong {
		it := items[f.path(dir)]
		if it == nil {
			t.Errorf("%s: missing", dir)
			continue
		}
		if it.Risk != core.RiskSafe || it.Warn != "" || !core.Recommend(it, now, 14*day) {
			t.Errorf("%s: generator output must stay safe: risk=%s warn=%q", dir, it.Risk, it.Warn)
		}
		if it.Recommended {
			t.Errorf("%s: generic names outside git must not be force-recommended", dir)
		}
	}
}

func TestHashedAsset(t *testing.T) {
	for name, want := range map[string]bool{
		"main.3f2a1b9c.js":      true,
		"787.a1b2c3d4.chunk.js": true,
		"main.3f2a1b9c.css":     true,
		"index-B1a2C3d4.js":     true,
		"vendor-D_2x-a9c.mjs":   true,
		"app-settings.js":       false,
		"my-component.js":       false,
		"jquery.min.js":         false,
		"index.js":              false,
		"webpack.prod.conf.js":  false,
		"chart-v2020.js":        false,
		"logo-12345678.png":     false,
		"deadbeefcafe.js":       false, // no name part
		"report.abcdefab.js":    false, // no digit
		"index-abcdefgh.js":     false, // no digit
		// Dates and timestamps are not hashes (reverify: a hand-written
		// build/release-20240101.js made the folder "strong", safe, ★).
		"release-20240101.js": false,
		"notes-24-01-01.js":   false,
		"app.20240101.js":     false,
		"app.1700000000.js":   false,
		"index-3f2a1b9c.js":   true, // Rollup 2 / Vite 2: hex after a dash
	} {
		if got := hashedAsset(name); got != want {
			t.Errorf("hashedAsset(%q) = %v, want %v", name, got, want)
		}
	}
}

// own-deletion-bumps-project-activity: deleting one artifact bumps its
// parent folder's mtime; the project must not look active because of it.
func TestOwnDeletionDoesNotMakeProjectActive(t *testing.T) {
	f := newFixture(t)
	f.file("src/rnapp/package.json", `{"dependencies":{"react-native":"0.81.0"}}`)
	f.file("src/rnapp/android/build.gradle", "x")
	f.file("src/rnapp/android/settings.gradle", "x")
	f.file("src/rnapp/android/.gradle/8.0/x", "x")
	f.file("src/rnapp/android/app/build.gradle", "x")
	f.big("src/rnapp/android/app/build/intermediates/x", 2<<20)
	f.file("src/rnapp/ios/Podfile", "x")
	f.file("src/rnapp/ios/Pods/x", "x")
	// Monorepo in git: deleting apps/app/node_modules bumps apps/app (an
	// entry of apps/, walked from the package up to the repository).
	f.repo("src/mono", &fakeRepo{ignored: []string{"apps/app/ios/Pods/"}, modified: []string{}})
	f.file("src/mono/package.json", `{"workspaces":["apps/*"]}`)
	f.file("src/mono/apps/app/package.json", "{}")
	f.file("src/mono/apps/app/ios/Podfile", "x")
	f.file("src/mono/apps/app/ios/Pods/x", "x")
	f.setup([]string{"src"}, nil)
	f.touchAll("src", ago(600*day))

	// What a partial clean does: the parents of the deleted artifacts move.
	if err := os.RemoveAll(f.path("src/rnapp/android/.gradle")); err != nil {
		t.Fatal(err)
	}
	f.touch("src/rnapp/android", ago(time.Hour))
	f.touch("src/mono/apps/app", ago(time.Hour))

	items, _ := f.scan()
	for _, rel := range []string{"src/rnapp/ios/Pods", "src/rnapp/android/app/build", "src/mono/apps/app/ios/Pods"} {
		it := items[f.path(rel)]
		if it == nil {
			t.Errorf("missing %s: %v", rel, f.rels(items))
			continue
		}
		if !it.LastUsed.Equal(ago(600 * day)) {
			t.Errorf("%s: LastUsed = %s, want %s (folder mtimes moved by our own deletions)", rel, it.LastUsed, ago(600*day))
		}
		if !core.Recommend(it, now, 14*day) {
			t.Errorf("%s: stale artifact must stay recommended", rel)
		}
	}
}

// processguard-artifacts-not-warned: an artifact guarded by a running
// process would be skipped: it must carry the warning (out of smart select).
func TestProcessGuardRunningWarns(t *testing.T) {
	f := newFixture(t)
	f.repo("src/app", &fakeRepo{ignored: []string{"ios/Pods/", "ios/build/", "node_modules/"}})
	f.file("src/app/package.json", "{}")
	f.file("src/app/node_modules/a.js", "a")
	f.file("src/app/ios/Podfile", "x")
	f.big("src/app/ios/Pods/x", 2<<20)
	f.big("src/app/ios/build/XCBuildData/x", 2<<20)
	f.setup([]string{"src"}, nil)
	f.touchAll("src", ago(60*day))
	f.running = []string{"xcodebuild"}
	items, _ := f.scan()
	for _, rel := range []string{"src/app/ios/Pods", "src/app/ios/build"} {
		it := items[f.path(rel)]
		if it == nil || !strings.Contains(it.Warn, "xcodebuild is running") || it.Recommended || core.Recommend(it, now, 14*day) {
			t.Errorf("%s: want the running warning and no recommendation: %+v", rel, it)
		}
	}
	if nm := items[f.path("src/app/node_modules")]; nm == nil || nm.Warn != "" {
		t.Errorf("node_modules has no process guard: %+v", nm)
	}

	f.running = nil
	items, _ = f.scan()
	if it := items[f.path("src/app/ios/Pods")]; it == nil || it.Warn != "" || !core.Recommend(it, now, 14*day) {
		t.Errorf("without xcodebuild the stale Pods are recommended: %+v", it)
	}
}

// yarn-leftover-substring: a commented-out enableGlobalCache line is not
// the effective setting.
func TestYarnLeftoverNeedsEffectiveGlobalCache(t *testing.T) {
	f := newFixture(t)
	f.repo("src/pnp", &fakeRepo{ignored: []string{".yarn/cache/"}})
	f.file("src/pnp/package.json", "{}")
	f.file("src/pnp/.yarnrc.yml", "nodeLinker: pnp\n# enableGlobalCache: true\nenableGlobalCache: false\n")
	f.file("src/pnp/.pnp.cjs", "// pnp")
	f.file("src/pnp/.yarn/cache/pkg.zip", "zip")
	f.setup([]string{"src"}, nil)
	f.touchAll("src", ago(2*day))
	items, _ := f.scan()
	it := items[f.path("src/pnp/.yarn/cache")]
	if it == nil {
		t.Fatalf("missing yarn cache: %v", f.rels(items))
	}
	if it.Meta["leftover"] == "true" || it.Recommended || core.Recommend(it, now, 14*day) {
		t.Errorf("project cache in use must not be a leftover: meta=%v rec=%v note=%q", it.Meta, it.Recommended, it.Note)
	}
}

func TestYarnGlobalCache(t *testing.T) {
	for rc, want := range map[string]bool{
		"enableGlobalCache: true\n":                             true,
		"enableGlobalCache: true # shared\n":                    true,
		"enableGlobalCache: \"true\"\r\n":                       true,
		"enableGlobalCache: 'true'\n":                           true,
		"nodeLinker: node-modules\nenableGlobalCache: true":     true,
		"# enableGlobalCache: true\n":                           false,
		"# enableGlobalCache: true\nenableGlobalCache: false\n": false,
		"enableGlobalCache: true\nenableGlobalCache: false\n":   false,
		"packageExtensions:\n  enableGlobalCache: true\n":       false,
		"enableGlobalCacheX: true\n":                            false,
		"enableGlobalCache: truex\n":                            false,
		"":                                                      false,
		"enableGlobalCache: false\n# enableGlobalCache: true\n": false,
	} {
		if got := yarnGlobalCache([]byte(rc)); got != want {
			t.Errorf("yarnGlobalCache(%q) = %v, want %v", rc, got, want)
		}
	}
}

// artifact-activity-ignores-deep-edits: an edit deep in the sources (no
// commit, no top-level change) keeps the project active.
func TestDeepEditsKeepProjectActive(t *testing.T) {
	f := newFixture(t)
	// No git: the bounded source walk finds it.
	f.file("src/plain/package.json", "{}")
	f.file("src/plain/package-lock.json", "{}")
	f.file("src/plain/node_modules/a.js", "a")
	f.file("src/plain/src/components/Button.tsx", "export {}")
	// Git: diff-files lists the uncommitted edit, deeper than the walk goes.
	f.repo("src/gitapp", &fakeRepo{
		ignored:  []string{"node_modules/"},
		modified: []string{"src/a/b/c/d/e/f/g/Button.tsx"},
	})
	f.file("src/gitapp/package.json", "{}")
	f.file("src/gitapp/node_modules/a.js", "a")
	f.file("src/gitapp/src/a/b/c/d/e/f/g/Button.tsx", "export {}")
	// Git: tracked sources in folders named like artifacts (bin/,
	// scripts/build/) are sources all the same; so are new files there.
	f.repo("src/gitbin", &fakeRepo{
		ignored:   []string{"node_modules/"},
		modified:  []string{"bin/cli.js"},
		untracked: []string{"scripts/build/new.js"},
	})
	f.file("src/gitbin/package.json", "{}")
	f.file("src/gitbin/node_modules/a.js", "a")
	f.file("src/gitbin/bin/cli.js", "tracked, edited")
	f.file("src/gitbin/scripts/build/new.js", "new")
	// Git: a new untracked folder (git lists it collapsed), deeper than the
	// source walk goes; an untracked artifact is not activity.
	f.repo("src/gitnew", &fakeRepo{
		ignored:   []string{"node_modules/"},
		modified:  []string{},
		untracked: []string{"src/a/b/c/d/e/f/feature/", "packages/x/node_modules/"},
	})
	f.file("src/gitnew/package.json", "{}")
	f.file("src/gitnew/node_modules/a.js", "a")
	f.file("src/gitnew/src/a/b/c/d/e/f/feature/deep/x.ts", "new")
	f.file("src/gitnew/packages/x/node_modules/y.js", "installed")
	// Git, clean tree: git is authoritative, the artifacts' own writes and
	// ignored files are not activity.
	f.repo("src/gitclean", &fakeRepo{ignored: []string{"node_modules/"}, modified: []string{}})
	f.file("src/gitclean/package.json", "{}")
	f.file("src/gitclean/node_modules/a.js", "a")
	f.file("src/gitclean/src/a.ts", "a")
	f.file("src/gitclean/logs/ignored.log", "written by a tool")
	f.setup([]string{"src"}, nil)
	f.touchAll("src", ago(40*day))
	for _, r := range []string{"src/gitapp", "src/gitbin", "src/gitnew", "src/gitclean"} {
		f.touch(r+"/.git/index", ago(40*day))
		f.touch(r+"/.git/HEAD", ago(40*day))
	}
	f.touch("src/plain/src/components/Button.tsx", ago(time.Hour))
	f.touch("src/gitapp/src/a/b/c/d/e/f/g/Button.tsx", ago(time.Hour))
	f.touch("src/gitbin/bin/cli.js", ago(3*time.Hour))
	f.touch("src/gitbin/scripts/build/new.js", ago(2*time.Hour))
	f.touch("src/gitnew/src/a/b/c/d/e/f/feature/deep/x.ts", ago(2*time.Hour))
	f.touch("src/gitnew/packages/x/node_modules/y.js", ago(time.Minute))
	f.touch("src/gitclean/node_modules/a.js", ago(time.Minute))
	f.touch("src/gitclean/logs/ignored.log", ago(time.Minute))
	items, _ := f.scan()

	for rel, want := range map[string]time.Time{
		"src/plain/node_modules":    ago(time.Hour),
		"src/gitapp/node_modules":   ago(time.Hour),
		"src/gitbin/node_modules":   ago(2 * time.Hour),
		"src/gitnew/node_modules":   ago(2 * time.Hour),
		"src/gitclean/node_modules": ago(40 * day),
	} {
		it := items[f.path(rel)]
		if it == nil {
			t.Errorf("missing %s: %v", rel, f.rels(items))
			continue
		}
		if !it.LastUsed.Equal(want) {
			t.Errorf("%s: LastUsed = %s, want %s", rel, it.LastUsed, want)
		}
		if active := now.Sub(want) < day; active == core.Recommend(it, now, 14*day) {
			t.Errorf("%s: recommend=%v with project active=%v", rel, !active, active)
		}
	}
}

// Roots given on the command line are the whole scope.
func TestExplicitRootsOnly(t *testing.T) {
	f := newFixture(t)
	f.file("src/app/package.json", "{}")
	f.file("src/app/node_modules/a.js", "a")
	f.file("other/app/package.json", "{}")
	f.file("other/app/node_modules/a.js", "a")
	f.file("Documents/Codex/chat/package.json", "{}")
	f.file("Documents/Codex/chat/node_modules/a.js", "a")
	f.file(".codex/worktrees/x/app/package.json", "{}")
	f.file(".codex/worktrees/x/app/node_modules/a.js", "a")
	f.setup([]string{"src"}, []string{".codex/worktrees"})

	items, _ := f.scan()
	if len(items) != 3 {
		t.Errorf("default scope: want src, worktree and extra roots, got %v", f.rels(items))
	}
	f.env.ExplicitRoots = true
	items, _ = f.scan()
	if len(items) != 1 || items[f.path("src/app/node_modules")] == nil {
		t.Errorf("explicit roots: only src must be scanned, got %v", f.rels(items))
	}
}

// TestRealGitDirtyActivity checks the git status command line and its
// parsing against a real git binary, in a throwaway repository.
func TestRealGitDirtyActivity(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	f := newFixture(t)
	f.env.Runner = core.ExecRunner{}
	repo := f.path("src/app")
	f.file("src/app/.gitignore", "node_modules/\n")
	f.file("src/app/package.json", "{}")
	f.file("src/app/node_modules/a.js", "a")
	f.file("src/app/src/deep/x/y/Button.tsx", "export {}")
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("add", ".")
	git("commit", "-q", "-m", "init")
	f.setup([]string{"src"}, nil)
	f.touchAll("src", ago(40*day))

	items, _ := f.scan()
	if it := items[f.path("src/app/node_modules")]; it == nil || !it.LastUsed.Equal(ago(40*day)) {
		t.Fatalf("clean tree: %+v", it)
	}
	// An uncommitted edit deep in the sources; an untracked folder.
	f.file("src/app/src/deep/x/y/Button.tsx", "export const edited = 1")
	f.touch("src/app/src/deep/x/y/Button.tsx", ago(time.Hour))
	f.file("src/app/src/new/z.ts", "new")
	f.touch("src/app/src/new/z.ts", ago(3*time.Hour))
	items, _ = f.scan()
	if it := items[f.path("src/app/node_modules")]; it == nil || !it.LastUsed.Equal(ago(time.Hour)) {
		t.Errorf("dirty tree: want LastUsed %s, got %+v", ago(time.Hour), it)
	}
}

// Reverify of artifact-deletes-dirty-linked-worktree: the checkouts the
// first fix missed.
//   - a linked worktree (a .git FILE) below a generic output, of a
//     repository the scan never meets: the size walk only reported .git
//     directories;
//   - a bare repository as the artifact itself, or named *.git inside one;
//   - a checkout whose .git cannot be used (garbage file): its folders were
//     judged "outside git", where generator output is enough.
func TestCheckoutsInsideGenericOutputs(t *testing.T) {
	old := ignoredMin
	ignoredMin = 1
	defer func() { ignoredMin = old }()

	f := newFixture(t)
	// Not in git; dist/ is generator output but holds dist/deploy, a linked
	// worktree of a repository outside the roots, with uncommitted work.
	f.file("src/proj/package.json", "{}")
	f.file("src/proj/dist/app.js.map", "{}")
	f.big("src/proj/dist/big.bin", 2<<20)
	f.file("src/proj/dist/deploy/.git", "gitdir: /elsewhere/repo/.git/worktrees/deploy\n")
	f.file("src/proj/dist/deploy/index.html", "<html>uncommitted work")
	// In git: build/ (ignored) is itself a bare repository; out/ (ignored)
	// holds one.
	f.repo("src/bare", &fakeRepo{ignored: []string{"build/", "out/", "logs/"}, modified: []string{}})
	f.file("src/bare/package.json", "{}")
	f.file("src/bare/build/HEAD", "ref: refs/heads/main\n")
	f.mkdir("src/bare/build/objects", "src/bare/build/refs/heads")
	f.file("src/bare/build/index.html", "<html>")
	f.file("src/bare/out/index.html", "<html>")
	f.file("src/bare/out/mirror.git/HEAD", "ref: refs/heads/main\n")
	f.mkdir("src/bare/out/mirror.git/objects", "src/bare/out/mirror.git/refs")
	// An ignored folder no rule knows, holding a linked worktree deeper
	// than the walk goes.
	f.file("src/bare/logs/a/b/c/d/e/f/g/wt/.git", "gitdir: /elsewhere/repo/.git/worktrees/wt\n")
	f.big("src/bare/logs/a/big.log", 1<<20)
	// A garbage .git file: a checkout all the same.
	f.file("src/broken/package.json", "{}")
	f.file("src/broken/.git", "garbage\n")
	f.file("src/broken/build/asset-manifest.json", "{}")
	f.big("src/broken/build/data.bin", 2<<20)
	f.file("src/broken/node_modules/a.js", "a")
	f.setup([]string{"src"}, nil)
	f.touchAll("src", ago(40*day))
	items, _ := f.scan()

	if it := items[f.path("src/proj/dist")]; it == nil || it.CanClean() || core.Recommend(it, now, 14*day) ||
		!strings.Contains(it.Warn, "dist/deploy") {
		t.Errorf("dist holding a linked worktree must be report-only: %+v", it)
	}
	if it := items[f.path("src/bare/build")]; it != nil && it.CanClean() {
		t.Errorf("a bare repository is never an artifact: %+v", it)
	}
	if it := items[f.path("src/bare/out")]; it == nil || it.CanClean() || !strings.Contains(it.Warn, "out/mirror.git") {
		t.Errorf("out holding a bare repository must be report-only: %+v", it)
	}
	if it := items[f.path("src/bare/logs")]; it == nil || it.CanClean() || !strings.Contains(it.Warn, "logs/a/b/c/d/e/f/g/wt") {
		t.Errorf("ignored folder holding a linked worktree must be report-only: %+v", it)
	}
	if it := items[f.path("src/broken/build")]; it != nil && it.CanClean() {
		t.Errorf("build of a checkout whose .git is unusable was judged outside git: risk=%s meta=%v", it.Risk, it.Meta)
	}
	if items[f.path("src/broken/node_modules")] == nil {
		t.Errorf("node_modules of that checkout must still be found: %v", f.rels(items))
	}
}

// Reverify: CMake FetchContent / ExternalProject clones are expected in a
// build tree (regenerated by the next configure), other clones are not.
func TestCMakeFetchContentClonesAllowed(t *testing.T) {
	f := newFixture(t)
	f.repo("src/cpp", &fakeRepo{ignored: []string{"build/", "build-x/"}, modified: []string{}})
	f.file("src/cpp/CMakeLists.txt", "x")
	for _, b := range []string{"build", "build-x"} {
		f.file("src/cpp/"+b+"/CMakeCache.txt", "x")
		f.big("src/cpp/"+b+"/obj.o", 1<<20)
	}
	f.file("src/cpp/build/_deps/fmt-src/.git/HEAD", "ref: refs/heads/master\n")
	f.file("src/cpp/build/json-prefix/src/json/.git/HEAD", "ref: refs/heads/master\n")
	f.file("src/cpp/build-x/tools/mine/.git/HEAD", "ref: refs/heads/master\n")
	f.setup([]string{"src"}, nil)
	f.touchAll("src", ago(40*day))
	items, _ := f.scan()
	if it := items[f.path("src/cpp/build")]; it == nil || !it.CanClean() || it.Warn != "" {
		t.Errorf("build tree with FetchContent / ExternalProject clones: %+v", it)
	}
	if it := items[f.path("src/cpp/build-x")]; it == nil || it.CanClean() || !strings.Contains(it.Warn, "build-x/tools/mine") {
		t.Errorf("other clones keep a build tree report-only: %+v", it)
	}
}

// Reverify: an ignored checkout inside the repository is never an ignored
// folder, but its own artifacts are found (hidden ones are only walked by
// the git phase).
func TestIgnoredHiddenCheckoutIsWalked(t *testing.T) {
	old := ignoredMin
	ignoredMin = 1
	defer func() { ignoredMin = old }()

	f := newFixture(t)
	f.repo("src/app", &fakeRepo{ignored: []string{".deploy/"}, modified: []string{}})
	f.file("src/app/package.json", "{}")
	f.worktree("src/app/.deploy", "src/app", &fakeRepo{ignored: []string{"node_modules/"}, modified: []string{}})
	f.file("src/app/.deploy/package.json", "{}")
	f.big("src/app/.deploy/node_modules/a.js", 1<<20)
	f.setup([]string{"src"}, nil)
	f.touchAll("src", ago(40*day))
	items, _ := f.scan()
	if it := items[f.path("src/app/.deploy")]; it != nil {
		t.Errorf("a checkout is not an ignored folder: %+v", it)
	}
	if it := items[f.path("src/app/.deploy/node_modules")]; it == nil || !it.CanClean() {
		t.Errorf("node_modules of the ignored checkout: %v", f.rels(items))
	}
}
