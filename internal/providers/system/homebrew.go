package system

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
)

// brewCacheKeep are cache entries never deleted directly: wiping the API
// cache and bootsnap makes the next brew command stall for a minute and a
// half (mole #1594 lesson).
var brewCacheKeep = map[string]bool{"api": true, "bootsnap": true, ".lock": true, "Locks": true}

// brewCleanupTimeout bounds `brew cleanup -n` (it takes seconds; much longer
// on a slow disk). Variable for tests.
var brewCleanupTimeout = 20 * time.Second

// brewCleanup is the parsed output of `brew cleanup -n`.
type brewCleanup struct {
	ok      bool
	other   int64           // bytes outside the cache (old kegs, casks, logs)
	removed map[string]bool // paths brew would remove
	kegs    int             // number of Cellar/Caskroom entries
	skipped []string        // formulae skipped because their newest version is not installed
}

// parseBrewCleanup parses `brew cleanup -n` output. Sizes are binary units.
func parseBrewCleanup(out, cacheDir string) brewCleanup {
	res := brewCleanup{ok: true, removed: map[string]bool{}}
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if rest, ok := strings.CutPrefix(l, "Would remove: "); ok {
			path, abv := rest, ""
			if i := strings.LastIndex(rest, " ("); i > 0 && strings.HasSuffix(rest, ")") {
				path, abv = rest[:i], rest[i+2:len(rest)-1]
			}
			res.removed[path] = true
			size := int64(0)
			if abv != "" {
				f := strings.Split(abv, ",")
				size = parseHumanSize(strings.TrimSpace(f[len(f)-1]), true)
			}
			if cacheDir != "" && fsx.Within(path, cacheDir) {
				continue
			}
			if size > 0 {
				res.other += size
			}
			if strings.Contains(path, "/Cellar/") || strings.Contains(path, "/Caskroom/") {
				res.kegs++
			}
			continue
		}
		l = strings.TrimPrefix(l, "Warning: ")
		if rest, ok := strings.CutPrefix(l, "Skipping "); ok {
			if i := strings.Index(rest, ":"); i > 0 && strings.Contains(rest, "most recent version") {
				res.skipped = append(res.skipped, rest[:i])
			}
		}
	}
	return res
}

// homebrew handles the download cache (deleted directly, except its API
// metadata), `brew cleanup` for old versions it knows how to remove, and
// reports the old kegs `brew cleanup` refuses to touch because the formula
// is outdated (newest version not installed): they only go away after
// `brew upgrade`.
func (s *scan) homebrew() {
	if !s.has("brew") {
		return
	}
	cacheDir := ""
	if out, err := s.run(10*time.Second, "brew", "--cache"); err == nil {
		cacheDir = strings.TrimSpace(string(out))
	}
	if cacheDir == "" || !filepath.IsAbs(cacheDir) {
		cacheDir = s.home("Library/Caches/Homebrew")
	}
	cacheDir = filepath.Clean(cacheDir)
	prefix := ""
	if out, err := s.run(10*time.Second, "brew", "--prefix"); err == nil {
		prefix = strings.TrimSpace(string(out))
	}

	// 1. download cache (bottles, casks, source tarballs)
	if isDir(cacheDir) {
		pl := s.locate(cacheDir)
		var paths []string
		for _, e := range list(cacheDir, false) {
			if !brewCacheKeep[e.name] {
				paths = append(paths, e.path)
			}
		}
		if len(paths) > 0 {
			it := s.newItem("homebrew-cache", core.CatLangs, "Homebrew download cache", core.RiskSafe)
			it.ID = itemID(it.Kind, cacheDir)
			it.Location = cacheDir + "/…"
			it.Paths = paths
			it.Note = "Bottles, casks and source archives downloaded by Homebrew; downloaded again when (re)installing (its API metadata is kept so brew stays fast)."
			if pl.External || pl.Dangling || pl.Link {
				it.Warn = warnExternal
				s.report(it, pubOpts{})
			} else {
				s.publish(it, pubOpts{newest: true})
			}
		}
	}

	// 2. brew cleanup (dry run first: its own logic knows what is safe)
	cl := brewCleanup{}
	out, err := s.run(brewCleanupTimeout, "brew", "cleanup", "-n", "--prune=all")
	timedOut := errors.Is(err, context.DeadlineExceeded)
	if err == nil {
		cl = parseBrewCleanup(string(out), cacheDir)
	}
	if (cl.ok && cl.other > 0) || (timedOut && s.ctx.Err() == nil) {
		it := s.newItem("homebrew-cleanup", core.CatLangs, "Homebrew old versions (brew cleanup)", core.RiskModerate)
		it.ID = itemID(it.Kind, "brew cleanup")
		it.Location = nonEmpty(prefix, "brew")
		it.Method = core.MethodCommand
		it.Command = []string{"brew", "cleanup", "--prune=all"}
		it.Note = "Removes old versions of installed formulae and casks, the download cache, stale lock files and old logs, as computed by `brew cleanup -n` (size shown excludes the download cache item); installed versions are never touched."
		if timedOut {
			it.Meta = map[string]string{"size": "unknown (brew cleanup -n took too long)"}
		} else {
			it.Size = cl.other
			it.Recommended = true
			it.Meta = map[string]string{"entries": strconv.Itoa(len(cl.removed)), "kegs": strconv.Itoa(cl.kegs)}
		}
		s.emitNow(it)
	}

	// 3. old kegs that brew cleanup skips (outdated formulae)
	s.homebrewOldKegs(prefix, cl)
}

// homebrewOldKegs reports Cellar versions that are not the linked (opt)
// version and that brew cleanup would not remove.
func (s *scan) homebrewOldKegs(prefix string, cl brewCleanup) {
	var cellars []string
	if prefix != "" {
		cellars = append(cellars, filepath.Join(prefix, "Cellar"))
	} else {
		cellars = []string{"/opt/homebrew/Cellar", "/usr/local/Cellar"}
	}
	type keg struct {
		formula, version, path string
	}
	var kegs []keg
	for _, cellar := range cellars {
		if !isDir(cellar) {
			continue
		}
		opt := filepath.Join(filepath.Dir(cellar), "opt")
		for _, f := range list(cellar, false) {
			if !f.dir {
				continue
			}
			vers := list(f.path, false)
			if len(vers) < 2 {
				continue
			}
			target, err := os.Readlink(filepath.Join(opt, f.name))
			if err != nil {
				continue // not linked: cannot tell which one is in use
			}
			linked := filepath.Base(target)
			for _, v := range vers {
				if !v.dir || v.name == linked || cl.removed[v.path] {
					continue
				}
				kegs = append(kegs, keg{formula: f.name, version: v.name, path: v.path})
			}
		}
	}
	if len(kegs) == 0 {
		return
	}
	it := s.newItem("homebrew-old-kegs", core.CatLangs, "", core.RiskModerate)
	counts := map[string]int{}
	for _, k := range kegs {
		it.Paths = append(it.Paths, k.path)
		counts[k.formula]++
	}
	var names []string
	for f := range counts {
		names = append(names, f)
	}
	sort.Strings(names)
	var shown []string
	for _, f := range names {
		if counts[f] > 1 {
			shown = append(shown, f+" ×"+strconv.Itoa(counts[f]))
		} else {
			shown = append(shown, f)
		}
	}
	it.ID = itemID(it.Kind, filepath.Dir(filepath.Dir(kegs[0].path)))
	it.Location = filepath.Dir(filepath.Dir(kegs[0].path)) + "/…"
	it.Name = "Homebrew old versions kept by outdated formulae (" + strconv.Itoa(len(kegs)) + " kegs)"
	it.Meta = map[string]string{"formulae": strings.Join(shown, ", ")}
	it.Note = "`brew cleanup` skips a formula until its newest version is installed, so these old versions stay forever: run `brew upgrade` then `brew cleanup` (or uninstall the formula) to remove them."
	s.report(it, pubOpts{newest: true, placeholder: true})
}
