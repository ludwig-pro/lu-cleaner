package cli

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/safety"
)

// Regression tests added while re-verifying the fixes of the cli group.

// envRecorder is a fakeProvider that records the Env each scan received.
type envRecorder struct {
	fakeProvider
	mu   sync.Mutex
	envs []core.Env
}

func (p *envRecorder) Scan(ctx context.Context, env *core.Env, emit core.Emit) error {
	p.mu.Lock()
	p.envs = append(p.envs, *env)
	p.mu.Unlock()
	return p.fakeProvider.Scan(ctx, env, emit)
}

func (p *envRecorder) last(t *testing.T) core.Env {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.envs) == 0 {
		t.Fatalf("provider %s did not scan", p.id)
	}
	return p.envs[len(p.envs)-1]
}

// roots-override-not-scoped, follow-up: `--root ~/one-project` narrows the
// artifacts scan only. The providers that read Env.Roots to learn what the
// projects use (Android NDK / build-tools, Ruby, node versions...) keep the
// configured roots: with only the explicit root, every version used by the
// other projects looked unused (and was recommended by smart select).
func TestExplicitRootsKeepUsageRootsForOtherProviders(t *testing.T) {
	h := newHarness(t)
	dev := filepath.Join(h.home, "dev")
	work := filepath.Join(h.home, "work")
	mkdirs(t, dev, work)
	h.cfg.Roots = []string{"~/dev"}
	arts := &envRecorder{fakeProvider: fakeProvider{id: "artifacts", cats: []core.Category{core.CatArtifacts}}}
	android := &envRecorder{fakeProvider: fakeProvider{id: "android", cats: []core.Category{core.CatAndroid}}}
	h.provs = []core.Provider{arts, android}

	if code := h.run("scan", "--root", work); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut.String())
	}
	if e := arts.last(t); !e.ExplicitRoots || !reflect.DeepEqual(e.Roots, []string{work}) {
		t.Errorf("artifacts env: explicit=%v roots=%v", e.ExplicitRoots, e.Roots)
	}
	if e := android.last(t); e.ExplicitRoots || !reflect.DeepEqual(e.Roots, []string{dev}) {
		t.Errorf("android env: explicit=%v roots=%v, want the configured roots", e.ExplicitRoots, e.Roots)
	}

	// The picker gets the same providers.
	h.tty()
	if code := h.run("clean", "--root", work); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut.String())
	}
	opt := h.pickers[len(h.pickers)-1]
	for _, p := range opt.Providers {
		_ = p.Scan(context.Background(), opt.Env, func(*core.Item) {})
	}
	if e := android.last(t); e.ExplicitRoots || !reflect.DeepEqual(e.Roots, []string{dev}) {
		t.Errorf("picker android env: explicit=%v roots=%v", e.ExplicitRoots, e.Roots)
	}
	if e := arts.last(t); !e.ExplicitRoots || !reflect.DeepEqual(e.Roots, []string{work}) {
		t.Errorf("picker artifacts env: explicit=%v roots=%v", e.ExplicitRoots, e.Roots)
	}

	// Configured roots stay protected as a whole with explicit roots.
	c := newCLI(h.app)
	c.f.roots = []string{work}
	s, err := c.newSetup(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.guard.Check(dev, safety.Options{}); err == nil {
		t.Error("a configured root must stay unremovable when --root is given")
	}

	// Without explicit roots nothing is substituted.
	h.app.StdinTTY, h.app.StdoutTTY = false, false
	if code := h.run("scan"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if e := arts.last(t); e.ExplicitRoots || !reflect.DeepEqual(e.Roots, []string{dev}) {
		t.Errorf("artifacts env without --root: explicit=%v roots=%v", e.ExplicitRoots, e.Roots)
	}
}

// exclude-protect-symlink, follow-up: an exclusion written with the real
// path also hides the items a provider emits through a symlinked folder
// (the guard refused them, but they were listed and planned).
func TestExcludeMatchesItemsEmittedThroughSymlinks(t *testing.T) {
	h := newHarness(t)
	real := filepath.Join(h.home, "real")
	mkdirs(t, filepath.Join(real, "keep", "node_modules"), filepath.Join(real, "other", "node_modules"))
	link := filepath.Join(h.home, "code")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	keep := h.mkItem("keep", core.CatArtifacts, "node_modules", gb, core.RiskModerate, 60*day)
	keep.Path = filepath.Join(link, "keep", "node_modules") // symlinked form
	group := h.mkItem("group", core.CatArtifacts, "js-build", gb, core.RiskModerate, 60*day)
	group.Path, group.Paths = "", []string{filepath.Join(link, "other", "dist"), filepath.Join(link, "KEEP", "dist")}
	other := h.mkItem("other", core.CatArtifacts, "node_modules", gb, core.RiskModerate, 60*day)
	other.Path = filepath.Join(link, "other", "node_modules")
	h.provs = []core.Provider{&fakeProvider{id: "artifacts", cats: []core.Category{core.CatArtifacts},
		items: []*core.Item{keep, group, other}}}
	h.cfg.Exclude = []string{"~/real/keep"}

	if code := h.run("clean", "-y", "-c", "artifacts", "-n"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut.String())
	}
	expectNames(t, h.lastClean(), "other")
	h.run("artifacts", "--list", "--min-size", "0")
	if out := h.out.String(); strings.Contains(out, "keep") || strings.Contains(out, "group") {
		t.Fatalf("excluded items listed:\n%s", out)
	}
}

// roots-override-not-scoped, follow-up: the "→ run:" hint of a report made
// with explicit roots keeps them (it proposed the artifacts of every root).
func TestReportHintKeepsExplicitRoots(t *testing.T) {
	h, root := scopeFixture(t)
	spaced := filepath.Join(h.home, "my work")
	mkdirs(t, spaced)
	h.run("artifacts", "--list", root)
	if out := h.out.String(); !strings.Contains(out, "→ run: lu-cleaner clean --smart -c artifacts --root ~/work") {
		t.Fatalf("hint:\n%s", out)
	}
	h.run("artifacts", "--list", root, spaced)
	if out := h.out.String(); !strings.Contains(out, "--root ~/work --root '"+spaced+"'") {
		t.Fatalf("hint with a space:\n%s", out)
	}
	h.run("artifacts", "--list")
	if out := h.out.String(); strings.Contains(out, "--root") {
		t.Fatalf("hint without explicit roots:\n%s", out)
	}
}

// trash-summary-misleading, follow-up: with use_trash in the config, the
// plan cannot tell the user to "rerun without --trash", and --trash=false
// must be able to override the config.
func TestUseTrashConfigCanBeOverridden(t *testing.T) {
	h := fixture(t)
	h.cfg.UseTrash = true
	if code := h.run("clean", "-y", "-c", "worktrees", "--risk", "caution", "-n"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut.String())
	}
	if out := h.out.String(); !strings.Contains(out, "use_trash is set in the config: rerun with --trash=false") {
		t.Fatalf("plan:\n%s", out)
	}
	if !h.cleanOpt[len(h.cleanOpt)-1].Trash {
		t.Fatal("use_trash ignored")
	}
	if code := h.run("clean", "-y", "-c", "worktrees", "--risk", "caution", "--trash=false", "-n"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errOut.String())
	}
	if h.cleanOpt[len(h.cleanOpt)-1].Trash || strings.Contains(h.out.String(), "Trash mode") {
		t.Fatalf("--trash=false must override use_trash:\n%s", h.out.String())
	}
	h.cfg.UseTrash = false
	h.run("clean", "-y", "-c", "worktrees", "--risk", "caution", "--trash", "-n")
	if out := h.out.String(); !strings.Contains(out, "rerun without --trash to remove them permanently") {
		t.Fatalf("plan with --trash:\n%s", out)
	}
}

// exclude-protect-symlink, follow-up: an item that contains an exclusion is
// hidden when cleaning it would remove the excluded folder, but a
// report-only item (removes nothing) that merely contains one stays visible.
func TestExcludeHidesContainersButKeepsReports(t *testing.T) {
	h := newHarness(t)
	dl := h.mkItem("Downloads", core.CatSystem, "downloads", 40*gb, core.RiskNever, 5*day)
	dl.Method, dl.Path = core.MethodReport, filepath.Join(h.home, "Downloads")
	cache := h.mkItem("big-cache", core.CatSystem, "cache", gb, core.RiskSafe, 60*day)
	cache.Path = filepath.Join(h.home, "Library", "Caches", "big")
	h.provs = []core.Provider{&fakeProvider{id: "system", cats: []core.Category{core.CatSystem}, items: []*core.Item{dl, cache}}}
	h.cfg.Exclude = []string{"~/Downloads/keep", "~/Library/Caches/big/keep"}
	h.run("scan", "-c", "system", "--min-size", "0")
	out := h.out.String()
	if !strings.Contains(out, "Downloads") {
		t.Errorf("a report-only item containing an exclusion must stay visible:\n%s", out)
	}
	if strings.Contains(out, "big-cache") {
		t.Errorf("a cleanable item containing an exclusion must be hidden:\n%s", out)
	}
}
