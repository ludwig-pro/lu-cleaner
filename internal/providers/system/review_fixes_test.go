package system

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"golang.org/x/sys/unix"
)

// Regression tests for defects found by the adversarial review.

// trash-smart-selected: emptying the Trash is irreversible and not
// regenerable; smart select must never pick it, however old it is.
func TestTrashNeverSmartSelected(t *testing.T) {
	f := newFixture(t, "trash")
	f.file(".Trash/report.pdf", 50_000, 0)
	f.dir(".Trash/old-project/.git", 0)
	it := one(t, f.scan(), "trash")
	for _, later := range []time.Duration{0, 20 * day, 400 * day} {
		if core.Recommend(it, f.now.Add(later), 14*day) {
			t.Errorf("trash preselected %v after the last trashing", later)
		}
	}
	if !it.NoRecommend {
		t.Errorf("trash item must carry the NoRecommend veto")
	}
}

// docker-commands-unpinned-context: the prune commands run against the
// context that was measured, and are re-validated before running.
func TestDockerCommandsPinnedToContext(t *testing.T) {
	const endpoint = "unix:///Users/x/.colima/work/docker.sock"
	f := newFixture(t, "docker")
	f.runner.bins["docker"] = true
	f.runner.out["docker context show"] = "colima-work\n"
	f.runner.out["docker --context colima-work version --format {{.Server.Version}}"] = "27.3.1\n"
	f.runner.out["docker --context colima-work system df --format json"] = dockerDFOut
	f.runner.out["docker context inspect colima-work --format {{.Endpoints.docker.Host}}"] = endpoint + "\n"
	items := f.scan()

	var cmds []*core.Item
	for _, it := range items {
		if it.Method == core.MethodCommand {
			cmds = append(cmds, it)
		}
	}
	if len(cmds) != 2 {
		t.Fatalf("want 2 docker command items, got %d", len(cmds))
	}
	for _, it := range cmds {
		if len(it.Command) < 3 || it.Command[0] != "docker" || it.Command[1] != "--context" || it.Command[2] != "colima-work" {
			t.Errorf("%s: command not pinned to the scanned context: %v", it.Kind, it.Command)
		}
		if it.Meta["endpoint"] != endpoint {
			t.Errorf("%s: endpoint not recorded: %v", it.Kind, it.Meta)
		}
		if it.Recheck == nil {
			t.Fatalf("%s: no Recheck", it.Kind)
		}
	}
	recheck := cmds[0].Recheck
	ctx := context.Background()
	if err := recheck(ctx); err != nil {
		t.Errorf("unchanged context must pass: %v", err)
	}

	// The context now points at another engine (re-created with the same name).
	f.runner.mu.Lock()
	f.runner.out["docker context inspect colima-work --format {{.Endpoints.docker.Host}}"] = "ssh://deploy@prod\n"
	f.runner.mu.Unlock()
	if err := recheck(ctx); err == nil || !strings.Contains(err.Error(), "ssh://deploy@prod") {
		t.Errorf("changed endpoint must be refused, got %v", err)
	}

	// The context was removed.
	f.runner.mu.Lock()
	delete(f.runner.out, "docker context inspect colima-work --format {{.Endpoints.docker.Host}}")
	f.runner.mu.Unlock()
	if err := recheck(ctx); err == nil {
		t.Errorf("a vanished context must be refused")
	}

	// Same endpoint, engine stopped.
	f.runner.mu.Lock()
	f.runner.out["docker context inspect colima-work --format {{.Endpoints.docker.Host}}"] = endpoint + "\n"
	f.runner.fail["docker --context colima-work version --format {{.Server.Version}}"] = true
	f.runner.mu.Unlock()
	if err := recheck(ctx); err == nil {
		t.Errorf("an engine that does not answer must be refused")
	}
}

// With DOCKER_HOST set, contexts are ignored by this process and the
// commands it runs later: they stay bare.
func TestDockerHostEnvKeepsBareCommands(t *testing.T) {
	f := newFixture(t, "docker")
	f.p.getenv = func(k string) string {
		if k == "DOCKER_HOST" {
			return "tcp://127.0.0.1:2375"
		}
		return ""
	}
	f.runner.bins["docker"] = true
	f.runner.out["docker context show"] = "default\n"
	f.runner.out["docker version --format {{.Server.Version}}"] = "27.3.1\n"
	f.runner.out["docker system df --format json"] = dockerDFOut
	bc := one(t, f.scan(), "docker-build-cache")
	if strings.Join(bc.Command, " ") != "docker builder prune -a -f" || bc.Recheck != nil {
		t.Errorf("DOCKER_HOST: command = %v, recheck = %v", bc.Command, bc.Recheck != nil)
	}
}

// When the current context cannot be read (`docker context show` failed or
// timed out), bare prune commands would follow whatever context is current
// at clean time: none are offered.
func TestDockerUnknownContextOffersNoCommands(t *testing.T) {
	f := newFixture(t, "docker")
	f.runner.bins["docker"] = true
	f.runner.fail["docker context show"] = true
	f.runner.out["docker version --format {{.Server.Version}}"] = "27.3.1\n"
	f.runner.out["docker system df --format json"] = dockerDFOut
	for _, it := range f.scan() {
		if it.Method == core.MethodCommand {
			t.Errorf("%s offered with an unknown context: %v", it.Kind, it.Command)
		}
	}
}

// A context name docker itself would reject is never put on a command line.
func TestDockerOddContextName(t *testing.T) {
	f := newFixture(t, "docker")
	f.runner.bins["docker"] = true
	f.runner.out["docker context show"] = "-H evil\n"
	f.runner.out["docker version --format {{.Server.Version}}"] = "27.3.1\n"
	f.runner.out["docker system df --format json"] = dockerDFOut
	if items := f.scan(); len(items) != 0 {
		t.Errorf("unexpected items for an invalid context name: %d", len(items))
	}
}

// go-commands-cwd-toolchain: go runs from the home folder with the local
// toolchain, both during the scan and in the emitted commands.
func TestGoRunsLocalToolchainFromHome(t *testing.T) {
	f := newFixture(t, "go")
	f.runner.bins["go"] = true
	key := "env GOTOOLCHAIN=local go env GOCACHE GOMODCACHE"
	f.runner.out[key] = f.abs("Library/Caches/go-build") + "\n" + f.abs("go/pkg/mod") + "\n"
	f.file("Library/Caches/go-build/00/abc-d", 20_000, 0)
	f.file("go/pkg/mod/cache/download/x", 20_000, 0)
	items := f.scan()
	if d, ok := f.runner.dirs[key]; !ok || d != f.home {
		t.Errorf("go env must run in the home folder, ran in %q (called: %v)", d, ok)
	}
	for _, kind := range []string{"go-build-cache", "go-module-cache"} {
		it := one(t, items, kind)
		if len(it.Command) < 4 || strings.Join(it.Command[:3], " ") != "env GOTOOLCHAIN=local go" {
			t.Errorf("%s: command must pin the local toolchain: %v", kind, it.Command)
		}
	}
}

// When `go env` fails, the items fall back to the default locations
// instead of disappearing silently.
func TestGoEnvFailureFallsBackToDefaults(t *testing.T) {
	f := newFixture(t, "go")
	f.runner.bins["go"] = true
	f.runner.fail["env GOTOOLCHAIN=local go env GOCACHE GOMODCACHE"] = true
	f.file("Library/Caches/go-build/00/abc-d", 20_000, 0)
	f.file("go/pkg/mod/cache/download/x", 20_000, 0)
	items := f.scan()
	if it := one(t, items, "go-build-cache"); it.Location != f.abs("Library/Caches/go-build") || it.Method != core.MethodCommand {
		t.Errorf("build cache fallback = %+v", it)
	}
	if it := one(t, items, "go-module-cache"); it.Location != f.abs("go/pkg/mod") {
		t.Errorf("module cache fallback = %+v", it)
	}
}

// The fallback honours the locations configured with `go env -w` (go env
// file), the environment winning over the file.
func TestGoEnvFailureFallbackReadsGoEnvFile(t *testing.T) {
	f := newFixture(t, "go")
	f.runner.bins["go"] = true
	f.runner.fail["env GOTOOLCHAIN=local go env GOCACHE GOMODCACHE"] = true
	f.write("Library/Application Support/go/env", "GOMODCACHE="+f.abs("gomod")+"\nGOCACHE="+f.abs("from-file")+"\n")
	f.p.getenv = func(k string) string {
		if k == "GOCACHE" {
			return f.abs("from-env")
		}
		return ""
	}
	f.file("from-env/00/x", 20_000, 0)
	f.file("from-file/00/x", 20_000, 0)
	f.file("gomod/cache/download/x", 20_000, 0)
	f.file("go/pkg/mod/cache/download/x", 20_000, 0) // stale default location
	items := f.scan()
	if it := one(t, items, "go-build-cache"); it.Location != f.abs("from-env") {
		t.Errorf("build cache fallback = %s, want the GOCACHE environment variable", it.Location)
	}
	if it := one(t, items, "go-module-cache"); it.Location != f.abs("gomod") {
		t.Errorf("module cache fallback = %s, want the go env file value", it.Location)
	}
}

// End to end with the real go command: lu-cleaner started inside a module
// that requires a Go release that does not exist must neither try to
// download a toolchain nor lose the Go items.
func TestGoRealToolchainInsideNewerModule(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go binary")
	}
	f := newFixture(t, "go")
	f.env.Runner = core.ExecRunner{}
	mod := t.TempDir()
	if err := os.WriteFile(filepath.Join(mod, "go.mod"), []byte("module x\n\ngo 1.999.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(mod)
	t.Setenv("GOTOOLCHAIN", "auto")
	t.Setenv("GOPROXY", "off") // never download anything from a test
	t.Setenv("GOFLAGS", "")
	t.Setenv("GOCACHE", f.abs("gocache"))
	t.Setenv("GOMODCACHE", f.abs("gomod"))
	f.file("gocache/00/x", 10_000, 0)
	f.file("gomod/cache/download/x", 10_000, 0)
	items := f.scan()
	if it := one(t, items, "go-build-cache"); it.Location != f.abs("gocache") {
		t.Errorf("build cache = %s", it.Location)
	}
	if it := one(t, items, "go-module-cache"); it.Location != f.abs("gomod") {
		t.Errorf("module cache = %s", it.Location)
	}
}

// homebrew-cleanup-timeout: the dry run gets enough time, and when it does
// not complete the old-kegs report does not claim they "stay forever".
func TestHomebrewCleanupTimeoutOldKegs(t *testing.T) {
	if brewCleanupTimeout < time.Minute {
		t.Errorf("brew cleanup -n timeout %v: it takes more than a minute on a cold disk", brewCleanupTimeout)
	}
	old := brewCleanupTimeout
	brewCleanupTimeout = 50 * time.Millisecond
	defer func() { brewCleanupTimeout = old }()
	f := newFixture(t, "homebrew")
	prefix := f.abs("brew")
	f.runner.bins["brew"] = true
	f.runner.out["brew --cache"] = f.abs("Library/Caches/Homebrew") + "\n"
	f.runner.out["brew --prefix"] = prefix + "\n"
	f.runner.block["brew cleanup -n --prune=all"] = true
	for _, keg := range []string{"foo/1.0", "foo/2.0"} {
		f.file("brew/Cellar/"+keg+"/bin/x", 5_000, 0)
	}
	os.MkdirAll(f.abs("brew/opt"), 0o755)
	os.Symlink("../Cellar/foo/2.0", f.abs("brew/opt/foo"))
	items := f.scan()
	kegs := one(t, items, "homebrew-old-kegs")
	if strings.Contains(kegs.Note, "forever") || strings.Contains(kegs.Name, "outdated") || kegs.Meta["brew_cleanup"] == "" {
		t.Errorf("old kegs without a completed dry run: name %q, note %q, meta %v", kegs.Name, kegs.Note, kegs.Meta)
	}
}

// Homebrew's "<name>--git" checkouts are git repositories: the safety guard
// refuses them, so they must not make the whole cache item unremovable.
func TestHomebrewCacheSkipsGitCheckouts(t *testing.T) {
	f := newFixture(t, "homebrew")
	cache := f.abs("Library/Caches/Homebrew")
	f.runner.bins["brew"] = true
	f.runner.out["brew --cache"] = cache + "\n"
	f.runner.fail["brew cleanup -n --prune=all"] = true
	f.file("Library/Caches/Homebrew/downloads/x.tar.gz", 50_000, 0)
	f.file("Library/Caches/Homebrew/foo--git/.git/HEAD", 100, 0)
	f.file("Library/Caches/Homebrew/foo--git/main.c", 100, 0)
	c := one(t, f.scan(), "homebrew-cache") // scan() also runs the guard on every target
	if !sameStrings(bases(c), []string{"downloads"}) {
		t.Errorf("cache targets = %v", bases(c))
	}
}

// downloads-lastused-mtime: a file that arrived today with an old mtime
// (archive extraction, curl -R, AirDrop, cp -p) is not "> 30 days old".
func TestDownloadsArrivalTimeCounts(t *testing.T) {
	f := newFixture(t, "downloads")
	f.p.added = fileAdded // real ctime: the files below were created now
	f.file("Downloads/release/app.ipa", 40_000, 900*day)
	f.file("Downloads/Old App.dmg", 40_000, 90*day)
	if items := f.scan(); len(items) != 0 {
		for _, it := range items {
			t.Errorf("fresh file proposed as old: %s %v", it.Name, bases(it))
		}
	}
	// Arrived long ago too: proposed.
	f.p.added = func(string, *unix.Stat_t) time.Time { return f.now.Add(-60 * day) }
	if len(byKind(f.scan(), "downloads-installers")) != 1 {
		t.Errorf("an old download must still be proposed")
	}
}

// reclaim-zero-semantics: a tree entirely hardlinked elsewhere frees ~0
// bytes, not its full size (Reclaim 0 means "same as Size").
func TestPublishFullyHardlinkedReclaim(t *testing.T) {
	f := newFixture(t, "trash")
	orig := f.file("Documents/big.bin", 200_000, 0)
	os.MkdirAll(f.abs(".Trash"), 0o755)
	if err := os.Link(orig, f.abs(".Trash/big.bin")); err != nil {
		t.Fatal(err)
	}
	it := one(t, f.scan(), "trash")
	if it.Size < 200_000 {
		t.Fatalf("size = %d", it.Size)
	}
	if it.Freed() >= it.Size {
		t.Errorf("fully hardlinked trash item frees %d of %d bytes, want ~0", it.Freed(), it.Size)
	}
}

// keep-latest-ignored: config keep_latest keeps the N newest JetBrains IDE
// versions of each product.
func TestJetBrainsKeepLatest(t *testing.T) {
	setup := func(keep int) map[string]*core.Item {
		f := newFixture(t, "jetbrains")
		f.env.KeepLatest = keep
		for _, v := range []string{"2023.3", "2024.2", "2024.3"} {
			f.file("Library/Caches/JetBrains/IntelliJIdea"+v+"/index/x", 10_000, 0)
			f.file("Library/Application Support/JetBrains/IntelliJIdea"+v+"/options/a.xml", 1_000, 0)
		}
		return f.scan()
	}
	versions := func(items map[string]*core.Item, kind string) string {
		l := byKind(items, kind)
		if len(l) == 0 {
			return ""
		}
		return l[0].Meta["versions"]
	}
	k1 := setup(1)
	if got := versions(k1, "jetbrains-old-caches"); got != "IntelliJIdea 2023.3, IntelliJIdea 2024.2" {
		t.Errorf("keep 1: old caches = %q", got)
	}
	k2 := setup(2)
	if got := versions(k2, "jetbrains-old-caches"); got != "IntelliJIdea 2023.3" {
		t.Errorf("keep 2: old caches = %q", got)
	}
	if got := versions(k2, "jetbrains-old-settings"); got != "IntelliJIdea 2023.3" {
		t.Errorf("keep 2: old settings = %q", got)
	}
	if got := versions(k2, "jetbrains-caches"); got != "IntelliJIdea 2024.2, IntelliJIdea 2024.3" {
		t.Errorf("keep 2: current caches = %q", got)
	}
}
