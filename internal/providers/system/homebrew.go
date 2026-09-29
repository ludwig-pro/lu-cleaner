package system

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
)

// brewCacheKeep are cache entries never deleted directly: wiping the API
// cache and bootsnap makes the next brew command stall for a minute and a
// half (mole #1594 lesson).
var brewCacheKeep = map[string]bool{"api": true, "bootsnap": true, ".lock": true, "Locks": true}

// brewLayout is where Homebrew keeps things, found without running brew:
// any brew command costs from 0.3 s (`brew --prefix`) to more than a minute
// (`brew cleanup -n`, 47-74 s measured, on the critical path of the scan).
type brewLayout struct {
	prefix     string // HOMEBREW_PREFIX (/opt/homebrew, /usr/local)
	repository string // HOMEBREW_REPOSITORY (/usr/local/Homebrew on Intel Macs)
	cellar     string
	cache      string // HOMEBREW_CACHE
	logs       string // HOMEBREW_LOGS
	noCleanup  map[string]bool
}

// brewLayout derives the prefix and the repository from the brew executable
// found on PATH, like bin/brew does (the executable's folder with symlinks
// resolved, then its parent; a symlinked executable points into the
// repository), and the cache and logs folders from HOMEBREW_CACHE /
// HOMEBREW_LOGS (environment, then the brew.env files brew loads), with
// their macOS defaults. ok is false when brew is not installed.
func (s *scan) brewLayout() (l brewLayout, ok bool) {
	exe, err := s.env.Runner.LookPath("brew")
	if err != nil || exe == "" {
		return l, false
	}
	if !filepath.IsAbs(exe) {
		if exe, err = filepath.Abs(exe); err != nil {
			return l, false
		}
	}
	dir := filepath.Dir(filepath.Clean(exe))
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		dir = r
	}
	file := filepath.Join(dir, filepath.Base(exe))
	l.prefix = filepath.Dir(dir)
	l.repository = l.prefix
	if fi, err := os.Lstat(file); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		if r, err := filepath.EvalSymlinks(file); err == nil {
			l.repository = filepath.Dir(filepath.Dir(r))
		}
	}
	l.cellar = filepath.Join(l.prefix, "Cellar")
	if c := filepath.Join(l.repository, "Cellar"); isDir(c) {
		l.cellar = c
	}
	set := s.brewSettings(l.prefix, "HOMEBREW_CACHE", "HOMEBREW_LOGS", "HOMEBREW_NO_CLEANUP_FORMULAE")
	l.cache = absOr(set["HOMEBREW_CACHE"], s.home("Library/Caches/Homebrew"))
	l.logs = absOr(set["HOMEBREW_LOGS"], s.home("Library/Logs/Homebrew"))
	l.noCleanup = map[string]bool{}
	for _, f := range strings.Split(set["HOMEBREW_NO_CLEANUP_FORMULAE"], ",") {
		if f = strings.TrimSpace(f); f != "" {
			l.noCleanup[f] = true
		}
	}
	return l, true
}

func absOr(p, def string) string {
	if p == "" || !filepath.IsAbs(p) {
		return filepath.Clean(def)
	}
	return filepath.Clean(p)
}

// brewSettings returns the values brew would use for the given HOMEBREW_*
// variables: the environment, overridden by /etc/homebrew/brew.env, then
// <prefix>/etc/homebrew/brew.env, then the user's brew.env
// ($XDG_CONFIG_HOME/homebrew or ~/.homebrew), and /etc again last when
// HOMEBREW_SYSTEM_ENV_TAKES_PRIORITY is set (bin/brew's order).
func (s *scan) brewSettings(prefix string, names ...string) map[string]string {
	const priority = "HOMEBREW_SYSTEM_ENV_TAKES_PRIORITY"
	want := map[string]bool{priority: true}
	out := map[string]string{}
	for _, n := range append(names, priority) {
		want[n] = true
		if v := s.p.getenv(n); v != "" {
			out[n] = v
		}
	}
	load := func(file string) {
		if file == "" || file == "-" {
			return
		}
		f, err := os.Open(file)
		if err != nil {
			return
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			// `read -r line` trims the line; `export "$line"` keeps the value verbatim.
			name, val, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
			if ok && want[name] {
				out[name] = val
			}
		}
	}
	system := s.p.brewSystemEnv
	load(system)
	takesPriority := out[priority] != ""
	load(filepath.Join(prefix, "etc", "homebrew", "brew.env"))
	switch {
	case s.p.getenv("XDG_CONFIG_HOME") != "":
		load(filepath.Join(s.p.getenv("XDG_CONFIG_HOME"), "homebrew", "brew.env"))
	case s.p.getenv("HOMEBREW_XDG_CONFIG_HOME") != "":
		load(filepath.Join(s.p.getenv("HOMEBREW_XDG_CONFIG_HOME"), "homebrew", "brew.env"))
	default:
		load(s.home(".homebrew/brew.env"))
	}
	if takesPriority {
		load(system)
	}
	return out
}

// homebrew handles the download cache (deleted directly, except its API
// metadata), `brew cleanup` for the old versions it removes, and reports the
// old kegs `brew cleanup` keeps (pinned formulae...). Nothing runs brew: the
// cleanup item is estimated from the Cellar (see oldKegs).
func (s *scan) homebrew() {
	l, ok := s.brewLayout()
	if !ok {
		return
	}
	s.homebrewCache(l.cache)
	s.homebrewCleanup(l)
}

// homebrewCache proposes the download cache (bottles, casks, source
// tarballs), keeping its API metadata.
func (s *scan) homebrewCache(cacheDir string) {
	if !isDir(cacheDir) {
		return
	}
	pl := s.locate(cacheDir)
	var paths []string
	for _, e := range list(cacheDir, false) {
		if brewCacheKeep[e.name] {
			continue
		}
		if e.dir && holdsGitRepo(e.path) {
			// "<name>--git" checkouts of HEAD formulae: the safety guard
			// refuses git repositories, which would skip the whole item.
			continue
		}
		paths = append(paths, e.path)
	}
	if len(paths) == 0 {
		return
	}
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

// keg is one installed version of a formula that is not the one in use.
type keg struct {
	formula, version, path string
	// keep says why `brew cleanup` keeps it ("" = it removes it,
	// keepUnknown = cannot tell).
	keep string
}

const (
	keepOutdated = "outdated formula (newest version not installed)"
	keepUnknown  = "unknown"
)

// oldKegs lists the Cellar versions that are not the one in use (the
// opt/<formula> link, or the var/homebrew/linked record) and tells which ones
// `brew cleanup` removes, following its own rules (Formula#eligible_kegs_for_
// cleanup): nothing of a formula whose newest version is not installed
// (outdated), of a pinned formula or of one listed in
// HOMEBREW_NO_CLEANUP_FORMULAE; otherwise every version older than the newest
// one, except HEAD builds and kegs holding a .keepme reference. The newest
// version of a homebrew/core formula comes from Homebrew's API cache (see
// brewAPIVersions); for other taps (or without that cache) it is unknown.
// Formulae without an opt link are skipped: nobody can tell which version is
// in use.
func oldKegs(l brewLayout) []keg {
	if !isDir(l.cellar) {
		return nil
	}
	opt := filepath.Join(l.prefix, "opt")
	linkedDir := filepath.Join(l.prefix, "var", "homebrew", "linked")
	pinnedDir := filepath.Join(l.prefix, "var", "homebrew", "pinned")
	type rack struct {
		name, current string
		inUse         map[string]bool
		vers          []entry
		core          bool // installed from homebrew/core
	}
	var racks []rack
	var core []string
	for _, f := range list(l.cellar, false) {
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
		r := rack{name: f.name, current: filepath.Base(target), inUse: map[string]bool{filepath.Base(target): true}, vers: vers}
		if t, err := os.Readlink(filepath.Join(linkedDir, f.name)); err == nil {
			r.inUse[filepath.Base(t)] = true
		}
		r.core = kegTap(filepath.Join(f.path, r.current)) == "homebrew/core"
		if r.core {
			core = append(core, f.name)
		}
		racks = append(racks, r)
	}
	latest := brewAPIVersions(l.cache, core)
	var out []keg
	for _, r := range racks {
		formulaKeep := ""
		newest, known := latest[r.name]
		switch {
		case exists(filepath.Join(pinnedDir, r.name)):
			formulaKeep = "pinned"
		case l.noCleanup[r.name]:
			formulaKeep = "HOMEBREW_NO_CLEANUP_FORMULAE"
		case !r.core || !known:
			formulaKeep = keepUnknown
		case !isDir(filepath.Join(l.cellar, r.name, newest)):
			formulaKeep = keepOutdated
		}
		for _, v := range r.vers {
			if !v.dir || r.inUse[v.name] {
				continue
			}
			k := keg{formula: r.name, version: v.name, path: v.path, keep: formulaKeep}
			switch {
			case k.keep != "":
			case strings.HasPrefix(v.name, "HEAD"):
				k.keep = "HEAD build"
			case !brewVersionLess(v.name, newest):
				k.keep = "newest version (not linked)"
			case exists(filepath.Join(v.path, ".keepme")):
				k.keep = "in use (.keepme)"
			}
			out = append(out, k)
		}
	}
	return out
}

// kegTap returns the tap a keg was installed from (INSTALL_RECEIPT.json
// source.tap; "" when unknown).
func kegTap(keg string) string {
	b, err := os.ReadFile(filepath.Join(keg, "INSTALL_RECEIPT.json"))
	if err != nil {
		return ""
	}
	var r struct {
		Source struct {
			Tap string `json:"tap"`
		} `json:"source"`
	}
	if json.Unmarshal(b, &r) != nil {
		return ""
	}
	return r.Source.Tap
}

// brewAPIVersions returns the current version ("1.2.3", "1.2.3_1" with a
// revision) of homebrew/core formulae, read from the API cache brew itself
// downloads and reads, without running brew: the payload of
// <cache>/api/internal/packages.<tag>.jws.json (the .payload sidecar) and its
// byte-offset index (.payload.index, format 1), so only the entries needed
// are parsed. Formulae it does not list are absent from the result, as
// everything is when the cache is missing or in another format.
func brewAPIVersions(cache string, names []string) map[string]string {
	out := map[string]string{}
	if len(names) == 0 {
		return out
	}
	matches, _ := filepath.Glob(filepath.Join(cache, "api", "internal", "packages.*.jws.json.payload"))
	payload := ""
	var newest time.Time
	for _, m := range matches {
		if fi, err := os.Stat(m); err == nil && exists(m+".index") && fi.ModTime().After(newest) {
			payload, newest = m, fi.ModTime()
		}
	}
	if payload == "" {
		return out
	}
	var idx struct {
		Version         int                 `json:"version"`
		PayloadBytesize int64               `json:"payload_bytesize"`
		Formulae        map[string][2]int64 `json:"formulae"`
	}
	b, err := os.ReadFile(payload + ".index")
	if err != nil || json.Unmarshal(b, &idx) != nil || idx.Version != 1 {
		return out
	}
	f, err := os.Open(payload)
	if err != nil {
		return out
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return out
	}
	// The sidecar holds a header line, then the payload the offsets refer to.
	base := fi.Size() - idx.PayloadBytesize
	if idx.PayloadBytesize <= 0 || base < 0 {
		return out
	}
	for _, name := range names {
		loc, ok := idx.Formulae[name]
		key, _ := json.Marshal(name)
		key = append(key, ':')
		off, size := loc[0], loc[1]
		if !ok || size <= 0 || size > 1<<20 || off < int64(len(key)) || off+size > idx.PayloadBytesize {
			continue
		}
		buf := make([]byte, int64(len(key))+size)
		if _, err := f.ReadAt(buf, base+off-int64(len(key))); err != nil || string(buf[:len(key)]) != string(key) {
			continue // stale or foreign index: never trust a misplaced entry
		}
		var e struct {
			Version  string `json:"stable_version"`
			Revision int    `json:"revision"`
		}
		if json.Unmarshal(buf[len(key):], &e) != nil || e.Version == "" {
			continue
		}
		if e.Revision > 0 {
			e.Version += "_" + strconv.Itoa(e.Revision)
		}
		out[name] = e.Version
	}
	return out
}

// brewVersionLess compares two keg names ("1.6.44", "2.14.1_2",
// "2026-09-25"): numeric runs numerically, the rest as text, then the
// "_<n>" revision.
func brewVersionLess(a, b string) bool {
	av, ar := splitRevision(a)
	bv, br := splitRevision(b)
	if c := naturalCompare(av, bv); c != 0 {
		return c < 0
	}
	return ar < br
}

func splitRevision(v string) (string, int) {
	if i := strings.LastIndexByte(v, '_'); i > 0 {
		if n, err := strconv.Atoi(v[i+1:]); err == nil {
			return v[:i], n
		}
	}
	return v, 0
}

// naturalCompare compares version strings token by token: numbers
// numerically, words as text, separators ignored. A word sorts before a
// number and a trailing word marks a pre-release (1.0beta < 1.0 < 1.0.1).
func naturalCompare(a, b string) int {
	ta, tb := versionTokens(a), versionTokens(b)
	for i := 0; i < len(ta) && i < len(tb); i++ {
		x, y := ta[i], tb[i]
		dx, dy := isDigit(x[0]), isDigit(y[0])
		switch {
		case dx && dy:
			x, y = strings.TrimLeft(x, "0"), strings.TrimLeft(y, "0")
			if len(x) != len(y) {
				return cmpInt(len(x), len(y))
			}
			if x != y {
				return strings.Compare(x, y)
			}
		case dx != dy:
			if dx {
				return 1
			}
			return -1
		case x != y:
			return strings.Compare(x, y)
		}
	}
	switch {
	case len(ta) > len(tb):
		if !isDigit(ta[len(tb)][0]) {
			return -1 // a has a pre-release suffix
		}
		return 1
	case len(ta) < len(tb):
		if !isDigit(tb[len(ta)][0]) {
			return 1
		}
		return -1
	}
	return 0
}

// versionTokens splits "1.0beta2" into "1", "0", "beta", "2".
func versionTokens(s string) []string {
	var out []string
	for i := 0; i < len(s); {
		c := s[i]
		if !isDigit(c) && !isLetter(c) {
			i++
			continue
		}
		j := i + 1
		for j < len(s) && (isDigit(s[j]) == isDigit(c)) && (isDigit(s[j]) || isLetter(s[j])) {
			j++
		}
		out = append(out, s[i:j])
		i = j
	}
	return out
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// homebrewCleanup offers `brew cleanup --prune=all` with the size it frees
// estimated from the filesystem (the old kegs it removes, its old logs and
// old portable Rubies; the download cache is its own item), and reports the
// old kegs it keeps (or may keep).
func (s *scan) homebrewCleanup(l brewLayout) {
	var removed, kept, unknown []keg
	for _, k := range oldKegs(l) {
		switch k.keep {
		case "":
			removed = append(removed, k)
		case keepUnknown:
			unknown = append(unknown, k)
		default:
			kept = append(kept, k)
		}
	}
	var measure []string
	for _, k := range removed {
		measure = append(measure, k.path)
	}
	// `--prune=all` removes every log folder (HOMEBREW_LOGS/<formula>).
	for _, e := range list(l.logs, false) {
		if e.dir {
			measure = append(measure, e.path)
		}
	}
	measure = append(measure, oldPortableRubies(l.repository)...)
	if len(measure) > 0 || len(unknown) > 0 {
		it := s.newItem("homebrew-cleanup", core.CatLangs, "Homebrew old versions (brew cleanup)", core.RiskModerate)
		it.ID = itemID(it.Kind, "brew cleanup")
		it.Location = l.prefix
		it.Method = core.MethodCommand
		it.Command = []string{"brew", "cleanup", "--prune=all"}
		it.Meta = map[string]string{"kegs": strconv.Itoa(len(removed))}
		if len(removed) > 0 {
			it.Meta["formulae"] = kegList(removed)
		}
		it.Note = "Removes old versions of installed formulae and casks, the download cache, stale lock files and old logs, as `brew cleanup` decides " +
			"(size estimated from the Cellar and Homebrew's API cache, without the download cache item); installed versions in use are never touched."
		if len(unknown) > 0 {
			it.Meta["unknown_kegs"] = kegList(unknown)
			it.Note += " It may also remove some of the old versions listed as \"not linked\" (their newest version is unknown here)."
		}
		if len(measure) == 0 {
			// Nothing known: offered, never preselected (size shown as unknown).
			it.Meta["size"] = "unknown (no Homebrew API data for the old versions)"
			s.emitNow(it)
		} else {
			it.Recommended = true
			s.publish(it, pubOpts{measure: measure, placeholder: len(removed) > 0})
		}
	}
	s.homebrewOldKegs(kept, unknown)
}

// homebrewOldKegs reports the old kegs `brew cleanup` keeps, and those for
// which it cannot be told.
func (s *scan) homebrewOldKegs(kept, unknown []keg) {
	all := append(append([]keg(nil), kept...), unknown...)
	if len(all) == 0 {
		return
	}
	it := s.newItem("homebrew-old-kegs", core.CatLangs, "", core.RiskModerate)
	why := map[string]bool{}
	for _, k := range kept {
		why[k.keep] = true
	}
	var reasons []string
	for r := range why {
		reasons = append(reasons, r)
	}
	sort.Strings(reasons)
	for _, k := range all {
		it.Paths = append(it.Paths, k.path)
	}
	sort.Strings(it.Paths)
	root := filepath.Dir(filepath.Dir(all[0].path))
	it.ID = itemID(it.Kind, root)
	it.Location = root + "/…"
	it.Meta = map[string]string{"formulae": kegList(all)}
	n := strconv.Itoa(len(all))
	switch {
	case len(unknown) > 0:
		it.Name = "Homebrew old versions not linked (" + n + " kegs)"
		it.Meta["brew_cleanup"] = "unknown for " + kegList(unknown) + " (no Homebrew API data: other tap, or API cache missing)"
		it.Note = "Installed versions that are not the linked one. For some of them it is unknown whether `brew cleanup` removes them (the \"Homebrew old versions (brew cleanup)\" item) " +
			"or keeps them until `brew upgrade` (outdated formulae)."
	case len(reasons) == 1 && reasons[0] == keepOutdated:
		it.Name = "Homebrew old versions kept by outdated formulae (" + n + " kegs)"
		it.Note = "`brew cleanup` skips a formula until its newest version is installed, so these old versions stay forever: run `brew upgrade` then `brew cleanup` (or uninstall the formula) to remove them."
	default:
		it.Name = "Homebrew old versions kept by brew cleanup (" + n + " kegs)"
		it.Note = "Installed versions that are not the linked one and that `brew cleanup` keeps: outdated formulae (`brew upgrade` them first), pinned ones (`brew unpin`), " +
			"formulae listed in HOMEBREW_NO_CLEANUP_FORMULAE, the newest version when an older one is linked, HEAD builds and kegs still referenced (.keepme)."
	}
	if len(reasons) > 0 {
		it.Meta["kept_because"] = strings.Join(reasons, ", ")
	}
	s.report(it, pubOpts{newest: true, placeholder: true})
}

// kegList returns "boost ×3, cjson" for kegs.
func kegList(kegs []keg) string {
	counts := map[string]int{}
	for _, k := range kegs {
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
	return strings.Join(shown, ", ")
}

// oldPortableRubies returns the portable Ruby versions `brew cleanup`
// removes: every <repository>/Library/Homebrew/vendor/portable-ruby/<x.y.z>
// folder but the one named in vendor/portable-ruby-version (none when that
// file is unreadable).
func oldPortableRubies(repository string) []string {
	vendor := filepath.Join(repository, "Library", "Homebrew", "vendor")
	b, err := os.ReadFile(filepath.Join(vendor, "portable-ruby-version"))
	if err != nil {
		return nil
	}
	latest := strings.TrimSpace(string(b))
	var out []string
	for _, e := range list(filepath.Join(vendor, "portable-ruby"), false) {
		if e.dir && strings.Contains(e.name, ".") && e.name != latest {
			out = append(out, e.path)
		}
	}
	return out
}
