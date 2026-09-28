package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/clean"
	"github.com/ludwig-pro/lu-cleaner/internal/config"
	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
	"github.com/ludwig-pro/lu-cleaner/internal/safety"
)

// setup is the resolved configuration of a run: environment, safety guard
// and cleaning options, all derived from the config file and the flags.
type setup struct {
	cfg         *config.Config
	env         *core.Env
	guard       *safety.Guard
	clean       clean.Options
	staleAfter  time.Duration
	minSize     int64 // config min_size
	disabled    []core.Category
	rootsSource string // where env.Roots come from
	protect     []string
	// missing are the exclude / protect entries that do not exist (yet):
	// harmless, but often a typo (shown by `config show`).
	missing []string
	// scope filters what the providers emit (see scopeProviders).
	scope *scope
}

// newSetup loads the config and builds the Env and the Guard. rootsOverride
// (positional arguments of `artifacts`) takes precedence over --root.
func (c *cli) newSetup(rootsOverride []string) (*setup, error) {
	cfg, err := c.LoadConfig()
	if err != nil {
		return nil, err
	}
	env := c.NewEnv()
	env.Logf = c.logf
	if env.Runner == nil {
		env.Runner = core.ExecRunner{}
	}
	if cfg.MaxDepth > 0 {
		env.MaxDepth = cfg.MaxDepth
	}
	s := &setup{cfg: cfg, env: env}

	if s.staleAfter, err = fsx.ParseAge(cfg.StaleAfter); err != nil {
		return nil, fmt.Errorf("config stale_after: %w", err)
	}
	env.StaleAfter = s.staleAfter
	env.KeepLatest = max(1, cfg.KeepLatest)
	env.ExtraArtifacts = cfg.ExtraArtifacts
	if s.minSize, err = fsx.ParseBytes(cfg.MinSize); err != nil {
		return nil, fmt.Errorf("config min_size: %w", err)
	}
	for _, name := range cfg.DisabledCategories {
		cat, err := core.ParseCategory(name)
		if err != nil {
			return nil, fmt.Errorf("config disabled_categories: %w", err)
		}
		s.disabled = append(s.disabled, cat)
	}

	// Exclude / protect entries: $VARS expanded, and each entry written
	// through a symlink also gets its resolved form (scanners emit real
	// paths: an exclusion of "~/code/keep" with ~/code -> ~/real must also
	// cover ~/real/keep).
	exclude, err := c.expandPaths(env, "exclude", cfg.Exclude, env.Home)
	if err != nil {
		return nil, err
	}
	if s.protect, err = c.expandPaths(env, "protect", cfg.Protect, env.Home); err != nil {
		return nil, err
	}
	for _, p := range append(append([]string{}, exclude...), s.protect...) {
		if _, err := os.Lstat(p); err != nil && !slices.Contains(s.missing, p) {
			s.missing = append(s.missing, p)
			c.logf("exclude/protect entry %s does not exist", p)
		}
	}
	exclude = withResolved(exclude)
	env.Exclude = exclude

	// The configured (or auto-detected) project roots. Roots given on the
	// command line replace them for the artifacts scan only: the other
	// providers keep them (see scope.usageRoots).
	var configured []string
	configSource := "config"
	if len(cfg.Roots) > 0 {
		configured = existingDirs(env, cfg.Roots, false)
	} else {
		configured = existingDirs(env, config.DefaultRootCandidates, true)
		configSource = "auto-detected"
	}
	switch {
	case len(rootsOverride) > 0:
		env.Roots, err = explicitDirs(env, rootsOverride)
		env.ExplicitRoots = true
		s.rootsSource = "arguments"
	case len(c.f.roots) > 0:
		env.Roots, err = explicitDirs(env, c.f.roots)
		env.ExplicitRoots = true
		s.rootsSource = "--root"
	default:
		env.Roots = configured
		s.rootsSource = configSource
	}
	if err != nil {
		return nil, err
	}
	wt := append(append([]string{}, config.DefaultWorktreeRoots...), cfg.WorktreeRoots...)
	env.WorktreeRoots = existingDirs(env, wt, false)
	for _, r := range env.Roots {
		c.logf("root: %s", r)
	}
	if env.ExplicitRoots {
		for _, r := range configured {
			c.logf("%s root (usage detection of the other scanners): %s", configSource, r)
		}
	}
	for _, r := range env.WorktreeRoots {
		c.logf("worktree root: %s", r)
	}

	// Every root (explicit or configured) is protected as a whole.
	roots := append(append([]string{}, env.Roots...), env.WorktreeRoots...)
	if env.ExplicitRoots {
		roots = append(roots, configured...)
	}
	extra := append(withResolved(s.protect), exclude...)
	s.guard = safety.New(env.Home, env.TmpDir, roots, extra)
	s.scope = newScope(env, exclude, configured)
	// Scanners must not propose protected paths, roots themselves or anything
	// containing a root; paths inside the roots stay proposable.
	env.Protected = s.guard.Protected

	trash := c.f.trash || cfg.UseTrash
	if c.trashSet && !c.f.trash {
		trash = false // --trash=false overrides use_trash
	}
	s.clean = clean.Options{
		Guard:  s.guard,
		Runner: env.Runner,
		Home:   env.Home,
		Trash:  trash,
		Force:  c.f.force,
		DryRun: c.f.dryRun,
	}
	return s, nil
}

// expandPaths expands "~" and $VARS ($HOME, ${TMPDIR}, any environment
// variable; "$$" is a literal "$") and makes relative paths absolute
// (relative to base). An undefined variable is an error: a protect or
// exclude entry that silently expands to another path protects nothing.
func (c *cli) expandPaths(env *core.Env, key string, paths []string, base string) ([]string, error) {
	var out []string
	for _, p := range paths {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		x, err := c.expandVars(env, p)
		if err != nil {
			return nil, fmt.Errorf("config %s: %w", key, err)
		}
		out = append(out, absPath(env, x, base))
	}
	return out, nil
}

// expandVars expands the $VARS of p. HOME and TMPDIR come from env (the
// resolved values the rest of the run uses).
func (c *cli) expandVars(env *core.Env, p string) (string, error) {
	if !strings.Contains(p, "$") {
		return p, nil
	}
	var undefined []string
	x := os.Expand(p, func(name string) string {
		switch name {
		case "$":
			return "$"
		case "HOME":
			return env.Home
		case "TMPDIR":
			return env.TmpDir
		}
		v := c.Getenv(name)
		if v == "" {
			undefined = append(undefined, name)
		}
		return v
	})
	if len(undefined) > 0 {
		return "", fmt.Errorf("%q: undefined variable $%s", p, undefined[0])
	}
	return x, nil
}

// withResolved returns paths followed by the resolved form of the entries
// that go through a symlink (see resolvePath), without duplicates.
func withResolved(paths []string) []string {
	out := append([]string(nil), paths...)
	for _, p := range paths {
		if r := resolvePath(p); r != p && !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	return out
}

// resolvePath resolves the symlinks of p. When p does not exist, its
// deepest existing ancestor is resolved and the rest appended, so an entry
// for a folder that does not exist yet still gets its real location.
func resolvePath(p string) string {
	rest := ""
	for cur := p; ; {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

func absPath(env *core.Env, p, base string) string {
	p = env.Expand(p)
	if !filepath.IsAbs(p) {
		if base == "" {
			if abs, err := filepath.Abs(p); err == nil {
				return abs
			}
		}
		p = filepath.Join(base, p)
	}
	return filepath.Clean(p)
}

// explicitDirs resolves user-given directories (relative to the cwd); they
// must exist.
func explicitDirs(env *core.Env, paths []string) ([]string, error) {
	var out []string
	for _, raw := range paths {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		p := absPath(env, strings.TrimSpace(raw), "")
		fi, err := os.Stat(p)
		if err != nil || !fi.IsDir() {
			return nil, usageErr("%s is not a directory", sanitize(raw))
		}
		if p == "/" || resolvePath(p) == "/" {
			return nil, usageErr("%s: the whole disk cannot be a project root, name a project folder (e.g. ~/dev)", sanitize(raw))
		}
		out = append(out, onDiskCase(p))
	}
	return dedupeDirs(out), nil
}

// existingDirs keeps the paths that are existing directories. With
// exactCase, the last element must exist with that exact case (APFS is case
// insensitive: "~/Dev" would otherwise duplicate "~/dev").
func existingDirs(env *core.Env, paths []string, exactCase bool) []string {
	var out []string
	for _, raw := range paths {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		p := absPath(env, strings.TrimSpace(raw), env.Home)
		fi, err := os.Stat(p)
		if err != nil || !fi.IsDir() {
			continue
		}
		if exactCase && !exactName(p) {
			continue
		}
		if env.Excluded(p) {
			continue
		}
		out = append(out, onDiskCase(p))
	}
	return dedupeDirs(out)
}

// onDiskCase returns p spelled as stored on disk: each component takes the
// name its parent directory lists (APFS is case- and
// normalization-insensitive: the root "~/code" may be the folder "~/Code").
// Paths derived from the roots then match the paths the kernel reports for
// processes (cwd, executables), which the in-use checks compare with.
// Symlinks are kept; unreadable components are kept as given.
func onDiskCase(p string) string {
	if !filepath.IsAbs(p) {
		return p
	}
	cur := "/"
	for _, name := range strings.Split(filepath.Clean(p), "/") {
		if name != "" {
			cur = filepath.Join(cur, diskName(cur, name))
		}
	}
	return cur
}

// diskName returns the entry of dir that is name: name itself when it is
// listed as is, else the entry equal to it up to case and normalization.
func diskName(dir, name string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return name
	}
	key, fold := safety.Key(name), ""
	for _, e := range entries {
		if e.Name() == name {
			return name
		}
		if fold == "" && safety.Key(e.Name()) == key {
			fold = e.Name()
		}
	}
	if fold != "" {
		return fold
	}
	return name
}

func exactName(p string) bool {
	entries, err := os.ReadDir(filepath.Dir(p))
	if err != nil {
		return true
	}
	base := filepath.Base(p)
	for _, e := range entries {
		if e.Name() == base {
			return true
		}
	}
	return false
}

// dedupeDirs drops directories that are the same file as, or nested inside,
// another one of the list (keeping the outermost, in first-seen order).
func dedupeDirs(paths []string) []string {
	type dir struct {
		path string
		key  string
		fi   os.FileInfo
	}
	var kept []dir
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		d := dir{path: p, key: strings.ToLower(p), fi: fi}
		skip := false
		for i := 0; i < len(kept); i++ {
			k := kept[i]
			if os.SameFile(k.fi, fi) || fsx.Within(d.key, k.key) {
				skip = true
				break
			}
			if fsx.Within(k.key, d.key) { // new dir contains a kept one
				kept = append(kept[:i], kept[i+1:]...)
				i--
			}
		}
		if !skip {
			kept = append(kept, d)
		}
	}
	out := make([]string, len(kept))
	for i, d := range kept {
		out[i] = d.path
	}
	return out
}

// ------------------------------------------------------------------ filters

// filterMode selects the defaults applied to unset flags.
type filterMode int

const (
	modeDisplay  filterMode = iota // lists, pickers: min-size from config, every risk
	modeCleanYes                   // non-interactive clean: min-size 0, max risk moderate
)

// splitList flattens repeatable, comma-separated flag values.
func splitList(vals []string) []string {
	var out []string
	for _, v := range vals {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

// buildFilter turns the flags into a core.Filter. forced restricts the
// categories (single-category commands); extraKinds adds kinds (--target).
func (c *cli) buildFilter(s *setup, mode filterMode, forced []core.Category, extraKinds []string) (core.Filter, error) {
	var f core.Filter

	var requested []core.Category
	for _, name := range splitList(c.f.categories) {
		cat, err := core.ParseCategory(name)
		if err != nil {
			return f, usageErr("--category: %v", err)
		}
		if !hasCat(requested, cat) {
			requested = append(requested, cat)
		}
	}
	if len(forced) > 0 {
		if len(requested) == 0 {
			requested = forced
		} else {
			var both []core.Category
			for _, cat := range requested {
				if hasCat(forced, cat) {
					both = append(both, cat)
				}
			}
			if len(both) == 0 {
				return f, usageErr("--category must be one of %s for this command", joinCats(forced))
			}
			requested = both
		}
	}
	if len(requested) > 0 {
		var kept []core.Category
		for _, cat := range requested {
			if hasCat(s.disabled, cat) {
				c.logf("category %s is disabled in the config", cat)
				continue
			}
			kept = append(kept, cat)
		}
		if len(kept) == 0 {
			return f, usageErr("%s disabled in the config (disabled_categories in %s)", joinCats(requested), config.Path())
		}
		f.Categories = kept
	} else if len(s.disabled) > 0 {
		for _, ci := range core.Categories {
			if !hasCat(s.disabled, ci.ID) {
				f.Categories = append(f.Categories, ci.ID)
			}
		}
		if len(f.Categories) == 0 {
			// An empty list means "every category": never let it stand for
			// "none".
			return f, usageErr("all categories are disabled in the config (disabled_categories in %s)", config.Path())
		}
	}

	f.Kinds = expandKinds(append(splitList(c.f.kinds), splitList(extraKinds)...))

	var err error
	switch {
	case c.f.minSize != "":
		if f.MinSize, err = fsx.ParseBytes(c.f.minSize); err != nil {
			return f, usageErr("--min-size: %v", err)
		}
	case mode == modeDisplay:
		f.MinSize = s.minSize
	}
	if c.f.olderThan != "" {
		if f.OlderThan, err = fsx.ParseAge(c.f.olderThan); err != nil {
			return f, usageErr("--older-than: %v", err)
		}
	}
	switch {
	case c.f.risk != "":
		if f.MaxRisk, err = core.ParseRisk(c.f.risk); err != nil {
			return f, usageErr("--risk: %v", err)
		}
	case mode == modeCleanYes:
		f.MaxRisk = core.RiskModerate
	default:
		// Lists show everything, report-only entries included (never cleanable).
		f.MaxRisk = core.RiskNever
	}
	return f, nil
}

// kindAliases maps a kind name to the kinds it also stands for: the short
// names people type (pods) and the two names of the artifacts that change
// kind outside an Android / iOS project (android-build is gradle-build in a
// plain Gradle project, ios-build is xcode-build...).
var kindAliases = map[string][]string{
	"pods":           {"ios-pods"},
	"pod":            {"ios-pods"},
	"cocoapods":      {"ios-pods"},
	"node-modules":   {"node_modules"},
	"ios-build":      {"xcode-build"},
	"xcode-build":    {"ios-build"},
	"android-build":  {"gradle-build"},
	"gradle-build":   {"android-build"},
	"android-gradle": {"gradle-cache"},
	"gradle-cache":   {"android-gradle"},
	"android-kotlin": {"gradle-kotlin"},
	"gradle-kotlin":  {"android-kotlin"},
}

// expandKinds adds the aliases of every requested kind (case-insensitive,
// without duplicates, requested names first).
func expandKinds(kinds []string) []string {
	var out []string
	add := func(k string) {
		if !slices.ContainsFunc(out, func(x string) bool { return strings.EqualFold(x, k) }) {
			out = append(out, k)
		}
	}
	for _, k := range kinds {
		add(k)
	}
	for _, k := range kinds {
		for _, a := range kindAliases[strings.ToLower(k)] {
			add(a)
		}
	}
	return out
}

// unmatchedKinds returns the requested kinds (before alias expansion) that
// match no item of the scan, whatever the other filters: usually a typo.
func unmatchedKinds(requested []string, items []*core.Item) []string {
	var out []string
	for _, k := range requested {
		names := expandKinds([]string{k})
		hit := slices.ContainsFunc(items, func(it *core.Item) bool {
			return slices.ContainsFunc(names, func(n string) bool {
				return strings.EqualFold(n, it.Kind) || strings.EqualFold(n, it.Provider)
			})
		})
		if !hit {
			out = append(out, k)
		}
	}
	return out
}

// warnUnmatchedKinds prints a warning for the --kind / --target values that
// matched nothing: a scripted cleanup must not silently skip what it asked for.
func (c *cli) warnUnmatchedKinds(extraKinds []string, items []*core.Item) {
	requested := append(splitList(c.f.kinds), splitList(extraKinds)...)
	for _, k := range unmatchedKinds(requested, items) {
		c.errw.printf("warning: kind %q matched nothing in this scan (see the kind of the items in 'lu-cleaner scan --json')\n", k)
	}
}

// providersFor returns the providers that may emit one of cats (all when empty).
func providersFor(all []core.Provider, cats []core.Category) []core.Provider {
	if len(cats) == 0 {
		return all
	}
	var out []core.Provider
	for _, p := range all {
		for _, pc := range p.Categories() {
			if hasCat(cats, pc) {
				out = append(out, p)
				break
			}
		}
	}
	return out
}

func hasCat(cats []core.Category, c core.Category) bool {
	for _, x := range cats {
		if x == c {
			return true
		}
	}
	return false
}

func joinCats(cats []core.Category) string {
	s := make([]string, len(cats))
	for i, c := range cats {
		s[i] = string(c)
	}
	return strings.Join(s, ", ")
}

// logf is Env.Logf: debug lines on stderr with --verbose (sanitized: they
// carry names and paths).
func (c *cli) logf(format string, args ...any) {
	if !c.f.verbose {
		return
	}
	c.errw.printf("debug: %s\n", sanitizeLines(fmt.Sprintf(format, args...), "  "))
}

// ------------------------------------------------------------------ scope

// scope is the CLI's own filter on what the providers emit, applied
// whatever each provider does: config exclusions (compared case- and
// Unicode-normalization-insensitively, symlinked forms included) and, when
// roots are given on the command line (`artifacts <root>`, --root), the
// project artifacts outside those roots.
//
// Explicit roots narrow the artifacts scan only. The other providers read
// Env.Roots to learn what the projects use (Android SDK / NDK / Gradle
// versions, Ruby and node versions, main repositories of worktrees): they
// get the configured roots (usageRoots), or `--root ~/one-project` would
// make every version used by the other projects look unused.
type scope struct {
	exclude []string // pathKey of every exclusion
	roots   []string // pathKey of the explicit roots (and their resolved form); nil = not restricted
	// usageRoots replaces Env.Roots (and clears Env.ExplicitRoots) for the
	// providers that do not emit project artifacts; nil = no substitution.
	usageRoots []string

	mu       sync.Mutex
	resolved map[string]string // parent dir -> its symlink-resolved form
}

func newScope(env *core.Env, exclude, configured []string) *scope {
	sc := &scope{resolved: map[string]string{}}
	for _, x := range exclude {
		sc.exclude = append(sc.exclude, pathKey(x))
	}
	if env.ExplicitRoots {
		sc.roots = []string{}
		for _, r := range withResolved(env.Roots) {
			sc.roots = append(sc.roots, pathKey(r))
		}
		sc.usageRoots = append([]string{}, configured...)
	}
	return sc
}

// pathKey is the comparison form of a path on APFS (case-insensitive,
// normalization-insensitive), the same as the safety guard's.
func pathKey(p string) string { return safety.Key(p) }

// withinKey reports whether key k is equal to or inside key dir.
func withinKey(k, dir string) bool {
	return k == dir || dir == "/" || strings.HasPrefix(k, dir+"/")
}

// active reports whether the scope filters anything.
func (sc *scope) active() bool { return sc != nil && (len(sc.exclude) > 0 || sc.roots != nil) }

// check returns why it is out of scope ("" when it is kept).
func (sc *scope) check(it *core.Item) string {
	paths := append([]string(nil), it.Targets()...)
	if it.Covers != "" {
		paths = append(paths, it.Covers)
	}
	if len(paths) == 0 && filepath.IsAbs(it.Location) {
		paths = append(paths, it.Location)
	}
	for _, p := range paths {
		if sc.excluded(p, it.CanClean()) {
			return "excluded by the config"
		}
	}
	if sc.roots != nil && it.Category == core.CatArtifacts {
		if len(paths) == 0 {
			return "outside the roots given on the command line"
		}
		for _, p := range paths {
			k := pathKey(p)
			if !slices.ContainsFunc(sc.roots, func(r string) bool { return withinKey(k, r) }) {
				return "outside the roots given on the command line"
			}
		}
	}
	return ""
}

// excluded reports whether p is or is inside an exclusion, or, with
// orContains (items that remove something), contains one: cleaning it would
// remove the excluded path too. A report-only item that merely contains an
// exclusion (the Downloads folder...) stays visible. A path emitted through
// a symlinked folder (~/code/app with ~/code -> ~/real) is also compared in
// its resolved form, like the safety guard does.
func (sc *scope) excluded(p string, orContains bool) bool {
	match := func(p string) bool {
		k := pathKey(p)
		return slices.ContainsFunc(sc.exclude, func(x string) bool {
			return withinKey(k, x) || (orContains && withinKey(x, k))
		})
	}
	if len(sc.exclude) == 0 {
		return false
	}
	if match(p) {
		return true
	}
	if !filepath.IsAbs(p) {
		return false
	}
	r := filepath.Join(sc.resolveDir(filepath.Dir(p)), filepath.Base(p))
	return r != p && match(r)
}

// resolveDir returns dir with its symlinks resolved (cached: group items
// have many paths in the same folder).
func (sc *scope) resolveDir(dir string) string {
	sc.mu.Lock()
	r, ok := sc.resolved[dir]
	sc.mu.Unlock()
	if !ok {
		r = resolvePath(dir)
		sc.mu.Lock()
		sc.resolved[dir] = r
		sc.mu.Unlock()
	}
	return r
}

// scopedProvider applies a scope to the items of a provider.
type scopedProvider struct {
	core.Provider
	sc *scope
}

func (p *scopedProvider) Scan(ctx context.Context, env *core.Env, emit core.Emit) error {
	if p.sc.usageRoots != nil && !slices.Contains(p.Categories(), core.CatArtifacts) {
		e := *env
		e.Roots, e.ExplicitRoots = p.sc.usageRoots, false
		env = &e
	}
	var mu sync.Mutex
	kept := map[string]bool{}
	return p.Provider.Scan(ctx, env, func(it *core.Item) {
		if it == nil {
			return
		}
		why := p.sc.check(it)
		mu.Lock()
		was := kept[it.ID]
		kept[it.ID] = why == ""
		mu.Unlock()
		switch {
		case why == "":
			emit(it)
		case was:
			// An earlier version of the item was in scope (upsert by ID):
			// replace it with a hidden, non-cleanable placeholder.
			h := it.Clone()
			h.Selectable, h.Method, h.Size, h.Reclaim = false, core.MethodReport, 0, 0
			h.Recommended, h.NoRecommend = false, true
			emit(h)
		default:
			env.Logf("left out %s: %s", it.Where(), why)
		}
	})
}

// scopeProviders wraps the providers with the setup's scope (no-op when the
// scope filters nothing).
func scopeProviders(s *setup, provs []core.Provider) []core.Provider {
	if !s.scope.active() {
		return provs
	}
	out := make([]core.Provider, len(provs))
	for i, p := range provs {
		out[i] = &scopedProvider{Provider: p, sc: s.scope}
	}
	return out
}
