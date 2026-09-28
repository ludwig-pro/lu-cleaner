package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

	exclude := expandAll(env, cfg.Exclude, env.Home)
	s.protect = expandAll(env, cfg.Protect, env.Home)
	env.Exclude = exclude

	switch {
	case len(rootsOverride) > 0:
		env.Roots, err = explicitDirs(env, rootsOverride)
		s.rootsSource = "arguments"
	case len(c.f.roots) > 0:
		env.Roots, err = explicitDirs(env, c.f.roots)
		s.rootsSource = "--root"
	case len(cfg.Roots) > 0:
		env.Roots = existingDirs(env, cfg.Roots, false)
		s.rootsSource = "config"
	default:
		env.Roots = existingDirs(env, config.DefaultRootCandidates, true)
		s.rootsSource = "auto-detected"
	}
	if err != nil {
		return nil, err
	}
	wt := append(append([]string{}, config.DefaultWorktreeRoots...), cfg.WorktreeRoots...)
	env.WorktreeRoots = existingDirs(env, wt, false)
	for _, r := range env.Roots {
		c.logf("root: %s", r)
	}
	for _, r := range env.WorktreeRoots {
		c.logf("worktree root: %s", r)
	}

	roots := append(append([]string{}, env.Roots...), env.WorktreeRoots...)
	extra := append(append([]string{}, s.protect...), exclude...)
	s.guard = safety.New(env.Home, env.TmpDir, roots, extra)
	// Scanners must not propose protected paths, roots themselves or anything
	// containing a root; paths inside the roots stay proposable.
	env.Protected = s.guard.Protected

	s.clean = clean.Options{
		Guard:  s.guard,
		Runner: env.Runner,
		Home:   env.Home,
		Trash:  c.f.trash || cfg.UseTrash,
		Force:  c.f.force,
		DryRun: c.f.dryRun,
	}
	return s, nil
}

// expandAll expands "~" and makes relative paths absolute (relative to base).
func expandAll(env *core.Env, paths []string, base string) []string {
	var out []string
	for _, p := range paths {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		out = append(out, absPath(env, p, base))
	}
	return out
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
			return nil, usageErr("%s is not a directory", raw)
		}
		out = append(out, p)
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
		out = append(out, p)
	}
	return dedupeDirs(out)
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
	}

	f.Kinds = append(splitList(c.f.kinds), splitList(extraKinds)...)

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

// logf is Env.Logf: debug lines on stderr with --verbose.
func (c *cli) logf(format string, args ...any) {
	if !c.f.verbose {
		return
	}
	c.errw.printf("debug: "+format+"\n", args...)
}
