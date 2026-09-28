package artifacts

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/clean"
	"github.com/ludwig-pro/lu-cleaner/internal/core"
)

// rnMonorepo builds a React Native / Expo monorepo in git plus a few
// neighbours (no git, symlinks, excluded, AI worktrees).
func rnMonorepo(t *testing.T) *fixture {
	f := newFixture(t)
	f.repo("src/mono", &fakeRepo{
		ignored: []string{
			".claude/worktrees/", "node_modules/", "apps/app/node_modules/", "apps/app/.expo/",
			"apps/app/ios/Pods/", "apps/app/ios/build/", "apps/app/android/build/", "apps/app/android/.gradle/",
			"apps/app/android/app/build/", "apps/app/android/app/.cxx/", "apps/web/dist/",
			"packages/lib/node_modules/", ".DS_Store",
		},
		tracked: []string{
			"package.json", "packages/lib/dist/index.js", "vendored/node_modules/committed.js", "docs/build/readme.md",
			"tools/package.json",
		},
	})
	f.file("src/mono/package.json", `{"name":"mono","private":true,"workspaces":["apps/*","packages/*"]}`)
	f.file("src/mono/yarn.lock", "# yarn lockfile v1\n")
	f.big("src/mono/node_modules/react/index.js", 8192)
	f.mkdir("src/mono/node_modules/@scope")
	// Workspace symlink: must never be followed nor double counted.
	if err := os.Symlink("../../packages/lib", f.path("src/mono/node_modules/@scope/lib")); err != nil {
		t.Fatal(err)
	}
	f.file("src/mono/apps/app/package.json", `{"name":"app","dependencies":{"expo":"~54.0.0","react-native":"0.81.0"}}`)
	f.file("src/mono/apps/app/app.json", `{"expo":{}}`)
	f.file("src/mono/apps/app/node_modules/x/index.js", "x")
	f.file("src/mono/apps/app/node_modules/x/package.json", "{}")
	f.file("src/mono/apps/app/node_modules/x/node_modules/y/index.js", "nested")
	f.file("src/mono/apps/app/.expo/xcodebuild.log", "log")
	f.file("src/mono/apps/app/ios/Podfile", "platform :ios")
	f.file("src/mono/apps/app/ios/Podfile.lock", "  - ReactCodegen (from `build/generated/ios/ReactCodegen`)\n")
	f.file("src/mono/apps/app/ios/App.xcodeproj/project.pbxproj", "x")
	f.file("src/mono/apps/app/ios/Pods/Manifest.lock", "x")
	f.file("src/mono/apps/app/ios/build/generated/ios/x.h", "x")
	f.file("src/mono/apps/app/android/build.gradle", "x")
	f.file("src/mono/apps/app/android/settings.gradle", "x")
	f.file("src/mono/apps/app/android/gradlew", "x")
	f.file("src/mono/apps/app/android/build/tmp/x", "x")
	f.file("src/mono/apps/app/android/.gradle/8.0/x", "x")
	f.file("src/mono/apps/app/android/app/build.gradle", "x")
	f.big("src/mono/apps/app/android/app/build/intermediates/classes.dex", 16384)
	f.file("src/mono/apps/app/android/app/.cxx/Debug/x", "x")
	f.file("src/mono/apps/web/package.json", `{"dependencies":{"vite":"6"}}`)
	f.file("src/mono/apps/web/dist/index.html", "<html>")
	f.file("src/mono/packages/lib/package.json", `{"name":"lib","main":"dist/index.js"}`)
	f.file("src/mono/packages/lib/dist/index.js", "tracked")
	f.file("src/mono/packages/lib/node_modules/z.js", "z")
	f.file("src/mono/docs/build/readme.md", "source folder named build")
	f.file("src/mono/tools/package.json", "{}")
	f.file("src/mono/tools/build/index.html", "untracked but not ignored")
	f.file("src/mono/vendored/package.json", "{}")
	f.file("src/mono/vendored/node_modules/committed.js", "committed")

	// In-repo Claude Code worktree.
	f.worktree("src/mono/.claude/worktrees/feat", "src/mono", &fakeRepo{ignored: []string{"node_modules/"}})
	f.file("src/mono/.claude/worktrees/feat/package.json", "{}")
	f.file("src/mono/.claude/worktrees/feat/node_modules/a.js", "a")
	f.file("src/mono/.claude/settings.local.json", "{}")

	// No git at all.
	f.file("src/plain/package.json", `{"name":"plain"}`)
	f.file("src/plain/package-lock.json", "{}")
	f.file("src/plain/build/index.html", "<html>")
	f.file("src/plain/dist/notes.txt", "not an output")
	f.big("src/plain/node_modules/dep/index.js", 8192)
	f.big("store/shared.js", 65536)
	if err := os.Link(f.path("store/shared.js"), f.path("src/plain/node_modules/dep/shared.js")); err != nil {
		t.Fatal(err)
	}
	f.file("src/notes/build/readme.md", "no marker")
	f.file("src/.hidden/proj/package.json", "{}")
	f.file("src/.hidden/proj/node_modules/a.js", "a")
	f.file("src/excluded/package.json", "{}")
	f.file("src/excluded/node_modules/a.js", "a")
	if err := os.Symlink(f.path("src/mono"), f.path("src/linked")); err != nil {
		t.Fatal(err)
	}
	f.file("src/plain/node_modules_link_target/package.json", "{}")

	// Codex worktree of mono.
	f.worktree(".codex/worktrees/abcd/mono", "src/mono", &fakeRepo{ignored: []string{"node_modules/"}})
	f.file(".codex/worktrees/abcd/mono/package.json", "{}")
	f.file(".codex/worktrees/abcd/mono/bun.lock", "")
	f.file(".codex/worktrees/abcd/mono/node_modules/b.js", "b")

	f.setup([]string{"src"}, []string{".codex/worktrees"}, "src/excluded")

	f.touchAll("src", ago(40*day))
	f.touchAll(".codex", ago(40*day))
	f.touch("src/mono/.git/index", ago(30*day))
	f.touch("src/mono/node_modules", now) // artifact activity must not count
	f.touch("src/plain/package.json", ago(2*time.Hour))
	f.inUse[f.path("src/plain")] = "4242"
	return f
}

func TestScanReactNativeMonorepo(t *testing.T) {
	f := rnMonorepo(t)
	items, _ := f.scan()

	type want struct {
		kind string
		risk core.Risk
		name string
	}
	wants := map[string]want{
		"src/mono/node_modules":                        {"node_modules", core.RiskModerate, "mono › node_modules"},
		"src/mono/apps/app/node_modules":               {"node_modules", core.RiskModerate, "mono › apps/app/node_modules"},
		"src/mono/apps/app/.expo":                      {"expo", core.RiskSafe, "mono › apps/app/.expo"},
		"src/mono/apps/app/ios/Pods":                   {"ios-pods", core.RiskModerate, "mono › apps/app/ios/Pods"},
		"src/mono/apps/app/ios/build":                  {"ios-build", core.RiskModerate, "mono › apps/app/ios/build"},
		"src/mono/apps/app/android/build":              {"android-build", core.RiskSafe, "mono › apps/app/android/build"},
		"src/mono/apps/app/android/.gradle":            {"android-gradle", core.RiskSafe, "mono › apps/app/android/.gradle"},
		"src/mono/apps/app/android/app/build":          {"android-build", core.RiskSafe, "mono › apps/app/android/app/build"},
		"src/mono/apps/app/android/app/.cxx":           {"android-cxx", core.RiskSafe, "mono › apps/app/android/app/.cxx"},
		"src/mono/apps/web/dist":                       {"dist", core.RiskSafe, "mono › apps/web/dist"},
		"src/mono/packages/lib/node_modules":           {"node_modules", core.RiskModerate, "mono › packages/lib/node_modules"},
		"src/mono/vendored/node_modules":               {"node_modules", core.RiskNever, "mono › vendored/node_modules"},
		"src/mono/.claude/worktrees/feat/node_modules": {"node_modules", core.RiskModerate, "claude:feat › node_modules"},
		"src/plain/build":                              {"js-build", core.RiskSafe, "plain › build"},
		"src/plain/node_modules":                       {"node_modules", core.RiskModerate, "plain › node_modules"},
		".codex/worktrees/abcd/mono/node_modules":      {"node_modules", core.RiskModerate, "codex:mono › node_modules"},
	}
	for rel, w := range wants {
		it := items[f.path(rel)]
		if it == nil {
			t.Errorf("missing %s; got %v", rel, f.rels(items))
			continue
		}
		if it.Kind != w.kind || it.Risk != w.risk || it.Name != w.name {
			t.Errorf("%s: got kind=%s risk=%s name=%q, want %s %s %q", rel, it.Kind, it.Risk, it.Name, w.kind, w.risk, w.name)
		}
		if it.Category != core.CatArtifacts || it.Provider != "artifacts" {
			t.Errorf("%s: category/provider %s/%s", rel, it.Category, it.Provider)
		}
	}
	if len(items) != len(wants) {
		t.Errorf("got %d items, want %d: %v", len(items), len(wants), f.rels(items))
	}
	for _, rel := range []string{
		"src/mono/packages/lib/dist", "src/mono/docs/build", "src/mono/tools/build", "src/plain/dist",
		"src/notes/build", "src/.hidden/proj/node_modules", "src/excluded/node_modules",
		"src/linked/node_modules", "src/mono/apps/app/node_modules/x/node_modules", "src/mono/.claude/worktrees",
	} {
		if it := items[f.path(rel)]; it != nil {
			t.Errorf("%s must not be emitted (%s)", rel, it.Kind)
		}
	}

	// Committed node_modules: its placeholder is replaced by a report.
	if v := items[f.path("src/mono/vendored/node_modules")]; v != nil {
		if v.CanClean() || v.Method != core.MethodReport || !strings.Contains(v.Warn, "tracked by git") {
			t.Errorf("committed node_modules must be report-only: %+v", v)
		}
	}

	nm := items[f.path("src/mono/node_modules")]
	if nm.Project != f.path("src/mono") || !reflect.DeepEqual(nm.RequireSibling, []string{"package.json"}) {
		t.Errorf("node_modules project/siblings: %s %v", nm.Project, nm.RequireSibling)
	}
	if nm.Meta["package_manager"] != "yarn" || !strings.Contains(nm.Note, "`yarn`") {
		t.Errorf("package manager: %v / %s", nm.Meta, nm.Note)
	}
	if nm.Meta["git"] != "ignored" {
		t.Errorf("git meta: %v", nm.Meta)
	}
	// LastUsed = project activity (git index 30 days ago), not the artifact mtime (now).
	if got := nm.LastUsed; !got.Equal(ago(30 * day)) {
		t.Errorf("LastUsed = %s, want %s", got, ago(30*day))
	}
	if !core.Recommend(nm, now, 14*day) {
		t.Errorf("stale moderate node_modules should be recommended")
	}
	// The workspace symlink is not followed: root node_modules holds only react.
	if nm.Files > 5 {
		t.Errorf("root node_modules counted %d files (symlink followed?)", nm.Files)
	}

	appNM := items[f.path("src/mono/apps/app/node_modules")]
	if appNM.Meta["project_type"] != "expo" || appNM.Meta["package"] != "apps/app" || !strings.Contains(appNM.Note, "workspace root") {
		t.Errorf("app node_modules meta/note: %v %q", appNM.Meta, appNM.Note)
	}
	pods := items[f.path("src/mono/apps/app/ios/Pods")]
	if pods.Meta["project_type"] != "expo" || !reflect.DeepEqual(pods.ProcessGuard, []string{"xcodebuild"}) {
		t.Errorf("pods: %v %v", pods.Meta, pods.ProcessGuard)
	}
	iosb := items[f.path("src/mono/apps/app/ios/build")]
	if iosb.Meta["codegen"] != "true" || !strings.Contains(iosb.Note, "pod install") {
		t.Errorf("ios/build codegen: %v %q", iosb.Meta, iosb.Note)
	}
	ab := items[f.path("src/mono/apps/app/android/app/build")]
	if !ab.Recommended || ab.Size < 16384 {
		t.Errorf("idle safe android build must be recommended and sized: rec=%v size=%d", ab.Recommended, ab.Size)
	}

	// Worktree tools.
	cx := items[f.path(".codex/worktrees/abcd/mono/node_modules")]
	if cx.Meta["in_worktree"] != "codex" || cx.Meta["package_manager"] != "bun" || !strings.Contains(cx.Note, "`bun i`") {
		t.Errorf("codex worktree: %v %q", cx.Meta, cx.Note)
	}
	if cl := items[f.path("src/mono/.claude/worktrees/feat/node_modules")]; cl.Meta["in_worktree"] != "claude" || cl.Project != f.path("src/mono/.claude/worktrees/feat") {
		t.Errorf("claude worktree: %v %s", cl.Meta, cl.Project)
	}

	// No git: content decides, npm lockfile, hardlinks, in use.
	pb := items[f.path("src/plain/build")]
	if pb.Meta["git"] != "none" || !strings.HasPrefix(pb.Warn, "in use: process 4242") || core.Recommend(pb, now, 14*day) {
		t.Errorf("plain build: meta=%v warn=%q", pb.Meta, pb.Warn)
	}
	pn := items[f.path("src/plain/node_modules")]
	if pn.Meta["package_manager"] != "npm" || !strings.Contains(pn.Note, "`npm ci`") {
		t.Errorf("plain pm: %v %q", pn.Meta, pn.Note)
	}
	if pn.Reclaim <= 0 || pn.Reclaim >= pn.Size || !strings.Contains(pn.Note, "only") {
		t.Errorf("hardlinked node_modules: size=%d reclaim=%d note=%q", pn.Size, pn.Reclaim, pn.Note)
	}
	if !pn.LastUsed.Equal(ago(2 * time.Hour)) {
		t.Errorf("plain LastUsed %s", pn.LastUsed)
	}
}

func TestPlaceholdersStreamBeforeSizes(t *testing.T) {
	f := rnMonorepo(t)
	f.scan()
	seen := map[string]bool{}
	for _, it := range f.emitted {
		if it.Sizing {
			seen[it.ID] = true
			continue
		}
		if it.Method != core.MethodReport && !seen[it.ID] {
			t.Errorf("%s emitted measured without a placeholder first", it.ID)
		}
	}
	if len(seen) == 0 {
		t.Fatal("no placeholder emitted")
	}
}

func TestIDStability(t *testing.T) {
	f := rnMonorepo(t)
	_, first := f.scan()
	_, second := f.scan()
	var a, b []string
	for id := range first {
		a = append(a, id)
	}
	for id := range second {
		b = append(b, id)
	}
	sort.Strings(a)
	sort.Strings(b)
	if !reflect.DeepEqual(a, b) {
		t.Errorf("IDs differ between scans:\n%v\n%v", a, b)
	}
	want := "artifacts:node_modules:" + f.path("src/mono/node_modules")
	if first[want] == nil {
		t.Errorf("missing stable id %s", want)
	}
}

// Every cleanable item must pass the executor's last-moment checks (guard,
// RequireSibling, not a repo...). Dry run: nothing is deleted.
func TestItemsPassExecutorChecks(t *testing.T) {
	f := rnMonorepo(t)
	others(f)
	items, _ := f.scan()
	var sel []*core.Item
	for _, it := range items {
		if it.CanClean() {
			sel = append(sel, it)
		}
	}
	sum := clean.Run(context.Background(), sel, clean.Options{
		DryRun: true, Force: true, Guard: f.guard, Runner: f.runner, Home: f.home, NoHistory: true,
	}, nil)
	for _, r := range sum.Results {
		if r.Status != clean.StatusDryRun {
			t.Errorf("%s: %s %s %s", r.Item.Name, r.Status, r.Message, r.Error)
		}
	}
	if len(sum.Results) == 0 {
		t.Fatal("nothing checked")
	}
}

// others adds non-JS ecosystems to a fixture.
func others(f *fixture) {
	f.file("src/py/pyproject.toml", "[project]")
	f.file("src/py/pkg/__pycache__/a.pyc", "x")
	f.file("src/py/tests/__pycache__/b.pyc", "x")
	f.file("src/py/.pytest_cache/v/x", "x")
	f.file("src/py/.venv/pyvenv.cfg", "home = /usr/bin\nversion = 3.12.4\n")
	f.file("src/py/.venv/lib/python3.12/site-packages/__pycache__/c.pyc", "x")
	f.file("src/py2/main.py", "print()")
	f.file("src/py2/.venv/pyvenv.cfg", "version = 3.11\n")
	f.file("src/py2/tools/myenv/pyvenv.cfg", "version = 3.11\n")
	f.file("src/py2/cachedir/CACHEDIR.TAG", cacheDirSignature+"\n# cache\n")
	f.file("src/py2/fakecache/CACHEDIR.TAG", "not a signature")
	f.file("src/rb/Gemfile", "source")
	f.file("src/rb/vendor/bundle/ruby/3.3.0/gems/x", "x")
	f.file("src/rb/vendor/other/lib.rb", "vendored source")
	f.file("src/rs/Cargo.toml", "[package]")
	f.file("src/rs/target/CACHEDIR.TAG", cacheDirSignature)
	f.file("src/rs/target/debug/x", "x")
	f.file("src/ws/ios/target/Widget.swift", "an Xcode target folder named target")
	f.file("src/swift/Package.swift", "// swift")
	f.file("src/swift/.build/debug/x", "x")
	f.file("src/xc/App.xcodeproj/project.pbxproj", "x")
	f.file("src/xc/build/XCBuildData/x", "x")
	f.file("src/xc/build-notes/x", "x")
	f.file("src/cm/CMakeLists.txt", "x")
	f.file("src/cm/build-macos/CMakeCache.txt", "x")
	f.file("src/cm/build-apple/whisper.xcframework/Info.plist", "x")
	f.file("src/plain/tmp-build/x", "x")
	f.file("src/cov/package.json", "{}")
	f.file("src/cov/coverage/lcov.info", "x")
	f.file("src/cov2/package.json", "{}")
	f.file("src/cov2/coverage/src.ts", "a source folder named coverage")
	f.extra = []string{"tmp-build", "src", "../etc"}
}

func TestOtherEcosystems(t *testing.T) {
	f := newFixture(t)
	f.file("src/plain/package.json", "{}")
	others(f)
	f.repo("src/berry", &fakeRepo{
		ignored: []string{".yarn/cache/", ".yarn/unplugged/", "tmp-build/"},
		tracked: []string{".yarnrc.yml", ".yarn/releases/yarn-4.cjs"},
	})
	f.file("src/berry/package.json", "{}")
	f.file("src/berry/.yarnrc.yml", "nodeLinker: node-modules\nenableGlobalCache: true\n")
	f.file("src/berry/.yarn/cache/pkg.zip", "zip")
	f.file("src/berry/.yarn/unplugged/native/x", "x")
	f.file("src/berry/.yarn/releases/yarn-4.cjs", "tracked")
	f.file("src/berry/tmp-build/x", "x")
	f.repo("src/zero", &fakeRepo{tracked: []string{".yarn/cache/pkg.zip"}})
	f.file("src/zero/package.json", "{}")
	f.file("src/zero/.yarnrc.yml", "")
	f.file("src/zero/.yarn/cache/pkg.zip", "zero-install: committed")
	f.setup([]string{"src"}, nil)
	f.touchAll("src", ago(3*day))
	items, _ := f.scan()

	wants := map[string]struct {
		kind string
		risk core.Risk
	}{
		"src/py/pkg/__pycache__":    {"python-cache", core.RiskSafe}, // group: keyed by Location below
		"src/py/.venv":              {"python-venv", core.RiskModerate},
		"src/py2/.venv":             {"python-venv", core.RiskCaution},
		"src/py2/tools/myenv":       {"python-venv", core.RiskCaution},
		"src/py2/cachedir":          {"cachedir-tag", core.RiskSafe},
		"src/rb/vendor/bundle":      {"ruby-vendor-bundle", core.RiskModerate},
		"src/rs/target":             {"rust-target", core.RiskSafe},
		"src/swift/.build":          {"swiftpm-build", core.RiskSafe},
		"src/xc/build":              {"xcode-build", core.RiskSafe},
		"src/cm/build-macos":        {"cmake-build", core.RiskModerate},
		"src/plain/tmp-build":       {"extra-artifact", core.RiskModerate},
		"src/cov/coverage":          {"coverage", core.RiskSafe},
		"src/berry/.yarn/cache":     {"yarn-cache", core.RiskModerate},
		"src/berry/.yarn/unplugged": {"yarn-unplugged", core.RiskModerate},
		"src/berry/tmp-build":       {"extra-artifact", core.RiskModerate},
	}
	group := items[f.path("src/py")+"/…"]
	if group == nil || group.Kind != "python-cache" || len(group.Paths) != 3 || !strings.Contains(group.Name, "(3 dirs)") {
		t.Errorf("python cache group: %+v", group)
	} else {
		items[f.path("src/py/pkg/__pycache__")] = group
		delete(items, f.path("src/py")+"/…")
	}
	for rel, w := range wants {
		it := items[f.path(rel)]
		if it == nil {
			t.Errorf("missing %s; got %v", rel, f.rels(items))
			continue
		}
		if it.Kind != w.kind || it.Risk != w.risk {
			t.Errorf("%s: kind=%s risk=%s, want %s %s", rel, it.Kind, it.Risk, w.kind, w.risk)
		}
	}
	for _, rel := range []string{
		"src/zero/.yarn/cache", "src/berry/.yarn/releases", "src/rb/vendor", "src/rb/vendor/other",
		"src/ws/ios/target", "src/xc/build-notes", "src/cm/build-apple", "src/cov2/coverage",
		"src/py2/fakecache", "src/py/.venv/lib/python3.12/site-packages/__pycache__",
	} {
		if it := items[f.path(rel)]; it != nil {
			t.Errorf("%s must not be emitted (%s)", rel, it.Kind)
		}
	}
	if len(items) != len(wants) {
		t.Errorf("got %d items, want %d: %v", len(items), len(wants), f.rels(items))
	}
	yc := items[f.path("src/berry/.yarn/cache")]
	if yc != nil && (!reflect.DeepEqual(yc.RequireSibling, []string{"../.yarnrc.yml"}) || yc.Meta["leftover"] != "true" || !yc.Recommended) {
		t.Errorf("yarn cache: siblings=%v meta=%v", yc.RequireSibling, yc.Meta)
	}
	if vb := items[f.path("src/rb/vendor/bundle")]; vb != nil && !reflect.DeepEqual(vb.RequireSibling, []string{"../Gemfile"}) {
		t.Errorf("vendor/bundle siblings: %v", vb.RequireSibling)
	}
	if v := items[f.path("src/py/.venv")]; v != nil && v.Meta["python"] != "3.12.4" {
		t.Errorf("venv meta: %v", v.Meta)
	}
}

func TestIgnoredDirs(t *testing.T) {
	old := ignoredMin
	ignoredMin = 32 << 10
	defer func() { ignoredMin = old }()

	f := newFixture(t)
	f.repo("src/app", &fakeRepo{ignored: []string{
		".qa-cache/", "output/", "small/", ".claude/", ".vscode/", "node_modules/", "clones/",
	}})
	f.file("src/app/package.json", "{}")
	f.big("src/app/node_modules/a.js", 4096)
	// Hidden agent cache: not walked in phase 1, walked for nested artifacts.
	f.big("src/app/.qa-cache/e2e/App.log", 64<<10)
	f.file("src/app/.qa-cache/staging/package.json", "{}")
	f.big("src/app/.qa-cache/staging/node_modules/big.js", 128<<10)
	f.big("src/app/output/campaign.mp4", 64<<10)
	f.big("src/app/small/x", 1024)
	f.big("src/app/.claude/cache", 64<<10)
	f.big("src/app/.vscode/cache", 64<<10)
	f.big("src/app/clones/c1/data", 64<<10)
	f.big("src/app/clones/dump.bin", 64<<10)
	f.mkdir("src/app/clones/c1/.git")
	f.setup([]string{"src"}, nil)
	f.touchAll("src", ago(10*day))
	f.touch("src/app/.qa-cache/e2e/App.log", ago(5*day))
	items, _ := f.scan()

	qa := items[f.path("src/app/.qa-cache")]
	if qa == nil {
		t.Fatalf("missing .qa-cache; got %v", f.rels(items))
	}
	if qa.Kind != "ignored-dir" || qa.Risk != core.RiskCaution || qa.Recommended || core.Recommend(qa, now, 0) {
		t.Errorf(".qa-cache: kind=%s risk=%s rec=%v", qa.Kind, qa.Risk, qa.Recommended)
	}
	if qa.Name != "app › .qa-cache (git-ignored)" || !strings.Contains(qa.Note, "not a known artifact") {
		t.Errorf(".qa-cache name/note: %q %q", qa.Name, qa.Note)
	}
	nested := items[f.path("src/app/.qa-cache/staging/node_modules")]
	if nested == nil || nested.Kind != "node_modules" {
		t.Fatalf("nested node_modules of .qa-cache not emitted: %v", f.rels(items))
	}
	if qa.Size < nested.Size+64<<10 || qa.Meta["artifacts_inside"] == "" {
		t.Errorf(".qa-cache size %d must include nested %d (+residual): %v", qa.Size, nested.Size, qa.Meta)
	}
	if !qa.LastUsed.Equal(ago(5 * day)) {
		t.Errorf(".qa-cache LastUsed = newest write %s, got %s", ago(5*day), qa.LastUsed)
	}
	if out := items[f.path("src/app/output")]; out == nil || out.Kind != "ignored-dir" {
		t.Errorf("output/ not reported")
	}
	if cl := items[f.path("src/app/clones")]; cl == nil || cl.CanClean() || !strings.Contains(cl.Warn, "git checkout") {
		t.Errorf("folder holding a checkout must be report-only: %+v", cl)
	}
	for _, rel := range []string{"src/app/small", "src/app/.claude", "src/app/.vscode"} {
		if it := items[f.path(rel)]; it != nil {
			t.Errorf("%s must not be reported", rel)
		}
	}
	// Total never double counts the nested artifact.
	var list []*core.Item
	for _, it := range items {
		list = append(list, it)
	}
	top := core.TopLevel(list)
	for _, it := range top {
		if it.Path == nested.Path {
			t.Errorf("nested artifact kept at top level")
		}
	}
}

func TestExternalVolumeIsReportOnly(t *testing.T) {
	f := newFixture(t)
	f.file("ext/proj/package.json", "{}")
	f.file("ext/proj/node_modules/a.js", "a")
	f.devs[f.path("ext")] = 999
	f.setup([]string{"ext"}, nil)
	items, _ := f.scan()
	it := items[f.path("ext/proj/node_modules")]
	if it == nil {
		t.Fatalf("missing item: %v", f.rels(items))
	}
	if it.Method != core.MethodReport || it.CanClean() || it.Warn != "on external volume — no internal gain" {
		t.Errorf("external item: method=%s warn=%q", it.Method, it.Warn)
	}
}

func TestWithoutGitGenericNamesAreSkipped(t *testing.T) {
	f := newFixture(t)
	f.runner.noGit = true
	f.repo("src/app", &fakeRepo{ignored: []string{"dist/", "node_modules/"}})
	f.file("src/app/package.json", "{}")
	f.file("src/app/dist/index.js", "x")
	f.file("src/app/node_modules/a.js", "a")
	f.setup([]string{"src"}, nil)
	items, _ := f.scan()
	if items[f.path("src/app/dist")] != nil {
		t.Errorf("dist in a repo must not be proposed when git state is unknown")
	}
	nm := items[f.path("src/app/node_modules")]
	if nm == nil || !nm.CanClean() || nm.Meta["git"] != "unknown" {
		t.Errorf("node_modules must still be proposed: %+v", nm)
	}
	for _, c := range f.runner.calls {
		t.Errorf("git called while unavailable: %s", c)
	}
}

func TestCheckIgnoreFallback(t *testing.T) {
	f := newFixture(t)
	f.repo("src/app", &fakeRepo{listErr: true, ignored: []string{"dist/"}})
	f.file("src/app/package.json", "{}")
	f.file("src/app/dist/index.js", "x")
	f.file("src/other/package.json", "{}")
	f.repo("src/other", &fakeRepo{listErr: true})
	f.file("src/other/dist/index.js", "x")
	f.setup([]string{"src"}, nil)
	items, _ := f.scan()
	if it := items[f.path("src/app/dist")]; it == nil || it.Meta["git"] != "ignored" {
		t.Errorf("check-ignore fallback: %v", f.rels(items))
	}
	if items[f.path("src/other/dist")] != nil {
		t.Errorf("dist not ignored must not be proposed")
	}
}

func TestBrokenGitRejectsGenericKeepsDeps(t *testing.T) {
	f := newFixture(t)
	f.repo("src/app", &fakeRepo{broken: true})
	f.file("src/app/package.json", "{}")
	f.file("src/app/dist/index.js", "x")
	f.file("src/app/node_modules/a.js", "a")
	f.setup([]string{"src"}, nil)
	items, _ := f.scan()
	if items[f.path("src/app/dist")] != nil {
		t.Errorf("dist must not be proposed when git fails")
	}
	if items[f.path("src/app/node_modules")] == nil {
		t.Errorf("node_modules must be proposed")
	}
}

func TestProtectedPathsAreNeverProposed(t *testing.T) {
	f := newFixture(t)
	f.file("src/app/package.json", "{}")
	f.file("src/app/node_modules/a.js", "a")
	f.file("src/keep/package.json", "{}")
	f.file("src/keep/node_modules/a.js", "a")
	f.setup([]string{"src"}, nil)
	prot := f.env.Protected
	f.env.Protected = func(p string) bool { return prot(p) || strings.HasPrefix(p, f.path("src/keep")) }
	items, _ := f.scan()
	if items[f.path("src/keep/node_modules")] != nil {
		t.Errorf("protected path proposed")
	}
	if items[f.path("src/app/node_modules")] == nil {
		t.Errorf("regular path missing")
	}
	for p, it := range items {
		if f.env.IsProtected(p) && it.CanClean() {
			t.Errorf("%s is protected", p)
		}
	}
}

func TestDuplicateRootsAndNesting(t *testing.T) {
	f := newFixture(t)
	f.file("src/app/package.json", "{}")
	f.file("src/app/node_modules/a.js", "a")
	f.file("src/app/sub/package.json", "{}")
	f.file("src/app/sub/node_modules/b.js", "b")
	if err := os.Symlink(f.path("src"), f.path("alias")); err != nil {
		t.Fatal(err)
	}
	// Same root twice (via a symlink) and a root nested in another one.
	f.setup([]string{"src", "alias", "src/app/sub"}, nil)
	items, byID := f.scan()
	if len(items) != 2 || len(byID) != 2 {
		t.Errorf("want 2 items, got %v", f.rels(items))
	}
}

func TestMaxDepth(t *testing.T) {
	f := newFixture(t)
	f.file("src/a/b/c/package.json", "{}")
	f.file("src/a/b/c/node_modules/x.js", "x")
	f.setup([]string{"src"}, nil)
	f.env.MaxDepth = 3
	items, _ := f.scan()
	if len(items) != 0 {
		t.Errorf("depth 4 artifact found with MaxDepth 3: %v", f.rels(items))
	}
	f.env.MaxDepth = 4
	items, _ = f.scan()
	if len(items) != 1 {
		t.Errorf("depth 4 artifact missing with MaxDepth 4: %v", f.rels(items))
	}
}

func TestCancelledScan(t *testing.T) {
	f := rnMonorepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() { done <- f.prov.Scan(ctx, f.env, func(*core.Item) {}) }()
	select {
	case err := <-done:
		if err == nil {
			t.Errorf("want ctx error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("scan ignores cancellation")
	}
}

func TestLibraryOutputIsModerate(t *testing.T) {
	f := newFixture(t)
	f.repo("src/lib", &fakeRepo{ignored: []string{"build/", "dist/"}})
	f.file("src/lib/package.json", `{"main":"build/index.js","exports":{".":{"types":"./build/index.d.ts"}}}`)
	f.file("src/lib/build/index.js", "x")
	f.file("src/lib/dist/index.js", "x")
	f.file("src/lib/android/build.gradle", "x")
	f.setup([]string{"src"}, nil)
	f.touchAll("src", ago(60*day))
	items, _ := f.scan()
	b := items[f.path("src/lib/build")]
	if b == nil || b.Risk != core.RiskModerate || b.Meta["library_output"] != "true" || b.Recommended {
		t.Errorf("library build output: %+v", b)
	}
	if d := items[f.path("src/lib/dist")]; d == nil || d.Risk != core.RiskSafe {
		t.Errorf("unreferenced dist should stay safe: %+v", d)
	}
}

func TestBinariesAndXcframeworkWarn(t *testing.T) {
	f := newFixture(t)
	f.repo("src/app", &fakeRepo{ignored: []string{"build/"}})
	f.file("src/app/package.json", "{}")
	f.file("src/app/build/app-release.apk", "apk")
	f.file("src/w/CMakeLists.txt", "x")
	f.file("src/w/build-apple/CMakeCache.txt", "x")
	f.file("src/w/build-apple/whisper.xcframework/Info.plist", "x")
	f.setup([]string{"src"}, nil)
	f.touchAll("src", ago(60*day))
	items, _ := f.scan()
	if b := items[f.path("src/app/build")]; b == nil || !strings.Contains(b.Warn, "app-release.apk") || core.Recommend(b, now, 14*day) {
		t.Errorf("apk warn: %+v", b)
	}
	if c := items[f.path("src/w/build-apple")]; c == nil || !strings.Contains(c.Warn, "whisper.xcframework") {
		t.Errorf("xcframework warn: %+v", c)
	}
}

// ---------------------------------------------------------------- units

func TestDecide(t *testing.T) {
	gen := &rule{Kind: "dist", Generic: true}
	strong := &rule{Kind: "xcode-build", Generic: true, ContentBeatsIgnore: true}
	free := &rule{Kind: "extra-artifact", Generic: true, FreeOutsideGit: true}
	dep := &rule{Kind: "node_modules"}
	g := &gitRoot{}
	cases := []struct {
		name    string
		c       cand
		ok      bool
		reasonC string
	}{
		{"deps outside git", cand{rule: dep, tracked: -1, ignored: -1}, true, ""},
		{"deps tracked", cand{rule: dep, git: g, tracked: 1, trackedSample: "x"}, false, "tracked"},
		{"deps unknown git", cand{rule: dep, git: g, tracked: -1, ignored: -1}, true, ""},
		{"generic ignored", cand{rule: gen, git: g, tracked: 0, ignored: 1}, true, ""},
		{"generic not ignored", cand{rule: gen, git: g, tracked: 0, ignored: 0, contentOK: true}, false, "not ignored"},
		{"generic unknown", cand{rule: gen, git: g, tracked: -1, ignored: 1}, false, "unknown"},
		{"generic outside git with content", cand{rule: gen, tracked: -1, ignored: -1, contentOK: true}, true, ""},
		{"generic outside git no content", cand{rule: gen, tracked: -1, ignored: -1}, false, "no build output"},
		{"strong content untracked", cand{rule: strong, git: g, tracked: 0, ignored: 0, contentOK: true}, true, ""},
		{"strong content tracked", cand{rule: strong, git: g, tracked: 1, contentOK: true}, false, "tracked"},
		{"extra outside git", cand{rule: free, tracked: -1, ignored: -1}, true, ""},
		{"extra in git not ignored", cand{rule: free, git: g, tracked: 0, ignored: 0}, false, "not ignored"},
	}
	s := &scan{}
	for _, tc := range cases {
		ok, why := s.decide(&tc.c)
		if ok != tc.ok || !strings.Contains(why, tc.reasonC) {
			t.Errorf("%s: got %v %q", tc.name, ok, why)
		}
	}
}

func TestRuleHelpers(t *testing.T) {
	rs := newRuleSet([]string{"tmp-build", "", "..", "a/b", ".git", "tmp-build"})
	if n := len(rs.candidates("tmp-build")); n != 1 {
		t.Errorf("extra rule registered %d times", n)
	}
	for _, bad := range []string{"..", "a/b", ".git"} {
		if len(rs.candidates(bad)) != 0 {
			t.Errorf("invalid extra name %q accepted", bad)
		}
	}
	var kinds []string
	for _, r := range rs.candidates("build") {
		kinds = append(kinds, r.Kind)
	}
	if want := []string{"cmake-build", "ios-build", "android-build", "flutter-build", "js-build"}; !reflect.DeepEqual(kinds, want) {
		t.Errorf("build rules order %v, want %v", kinds, want)
	}
	if k := rs.candidates("build-ios-sim"); len(k) != 1 || k[0].Kind != "cmake-build" {
		t.Errorf("glob rule: %v", k)
	}
	seen := map[string]bool{}
	for _, r := range rules {
		key := r.Kind + "/" + strings.Join(r.Names, ",") + "/" + r.Sub
		if seen[key] {
			t.Errorf("duplicate rule %s", key)
		}
		seen[key] = true
		if r.Note == "" || r.Kind == "" || len(r.Names) == 0 {
			t.Errorf("incomplete rule %+v", r)
		}
		if r.Generic && len(r.Content) == 0 && !r.FreeOutsideGit && r.NeedContent {
			t.Errorf("rule %s needs content but has none", r.Kind)
		}
	}

	yarn := &rule{Sub: "cache", Markers: []string{".yarnrc.yml"}}
	if got := yarn.requireSibling(); !reflect.DeepEqual(got, []string{"../.yarnrc.yml"}) {
		t.Errorf("requireSibling = %v", got)
	}
	ab := &rule{Kind: "android-build", AltKind: "gradle-build"}
	if ab.kindFor("/p/android/app/build") != "android-build" || ab.kindFor("/p/lib/build") != "gradle-build" {
		t.Errorf("android kindFor")
	}
	ib := &rule{Kind: "ios-build", AltKind: "xcode-build"}
	if ib.kindFor("/p/ios/build") != "ios-build" || ib.kindFor("/p/build") != "xcode-build" {
		t.Errorf("ios kindFor")
	}
	if got := unquoteGit(`"caf\303\251/a\"b\\c\tx"`); got != "café/a\"b\\c\tx" {
		t.Errorf("unquoteGit = %q", got)
	}
	if got := unquoteGit("plain name"); got != "plain name" {
		t.Errorf("unquoteGit plain = %q", got)
	}
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "app/android/app"), 0o755)
	os.WriteFile(filepath.Join(dir, "app/package.json"), []byte("{}"), 0o644)
	if got := climbPlatform(filepath.Join(dir, "app/android/app")); got != filepath.Join(dir, "app") {
		t.Errorf("climbPlatform = %s", got)
	}
	if got := climbPlatform(filepath.Join(dir, "lib")); got != filepath.Join(dir, "lib") {
		t.Errorf("climbPlatform no app = %s", got)
	}
}

// TestRealGit checks the git command lines against a real git binary, in a
// throwaway repository under t.TempDir().
func TestRealGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	f := newFixture(t)
	f.env.Runner = core.ExecRunner{}
	repo := f.path("src/app")
	f.file("src/app/.gitignore", "node_modules/\ndist/\n.qa-cache/\nios/\n")
	f.file("src/app/package.json", "{}")
	f.file("src/app/node_modules/a.js", "a")
	f.file("src/app/dist/index.js", "x")
	f.file("src/app/lib/package.json", "{}")
	f.file("src/app/lib/dist/index.js", "force-added")
	f.file("src/app/café/package.json", "{}")
	f.file("src/app/café/dist/index.js", "x")
	f.file("src/app/ios/Podfile", "x")
	f.file("src/app/ios/Pods/Manifest.lock", "x")
	f.big("src/app/.qa-cache/run/video.mp4", 64<<10)
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("add", ".gitignore", "package.json", "lib/package.json")
	git("add", "-f", "lib/dist/index.js")
	f.setup([]string{"src"}, nil)
	old := ignoredMin
	ignoredMin = 32 << 10
	defer func() { ignoredMin = old }()
	items, _ := f.scan()
	for _, rel := range []string{"src/app/node_modules", "src/app/dist", "src/app/café/dist", "src/app/ios/Pods", "src/app/.qa-cache"} {
		if it := items[f.path(rel)]; it == nil || it.Meta["git"] != "ignored" {
			t.Errorf("%s: %+v (got %v)", rel, it, f.rels(items))
		}
	}
	if it := items[f.path("src/app/lib/dist")]; it != nil {
		t.Errorf("force-added dist must not be proposed")
	}
	if it := items[f.path("src/app/ios")]; it != nil {
		t.Errorf("ignored ios/ only holds known artifacts: must not be reported (%s)", it.Kind)
	}
}
