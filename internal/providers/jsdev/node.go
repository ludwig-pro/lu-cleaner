package jsdev

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/ludwig-pro/lu-cleaner/internal/core"
)

// nodeInstall is one Node.js version installed by a version manager.
type nodeInstall struct {
	manager string // nvm, fnm, mise, asdf, volta
	root    string // manager data dir (~/.nvm, ~/.local/share/fnm...)
	dir     string // version directory: the item path
	ver     version
	lib     string   // global lib/node_modules
	bin     string   // bin dir holding `node`
	extra   []string // alias symlinks removed with the version (mise "20" -> 20.20.1)

	// references (filled by classify)
	defaults []string
	aliases  []string
	pins     []string
	onPath   bool
	running  int
	globals  []string
}

func (n *nodeInstall) referenced() bool {
	return n.running > 0 || n.onPath || len(n.defaults) > 0 || len(n.aliases) > 0 || len(n.pins) > 0
}

// managerRoots lists the data directories of each Node version manager.
func (s *scanner) managerRoots() map[string][]string {
	return map[string][]string{
		"nvm": uniqDirs(s.envDir("NVM_DIR"), s.home(".nvm")),
		"fnm": uniqDirs(s.envDir("FNM_DIR"), s.envDir("XDG_DATA_HOME", "fnm"), s.home(".local", "share", "fnm"),
			s.home("Library", "Application Support", "fnm"), s.home(".fnm")),
		"mise":  uniqDirs(s.envDir("MISE_DATA_DIR"), s.envDir("XDG_DATA_HOME", "mise"), s.home(".local", "share", "mise")),
		"asdf":  uniqDirs(s.envDir("ASDF_DATA_DIR"), s.home(".asdf")),
		"volta": uniqDirs(s.envDir("VOLTA_HOME"), s.home(".volta")),
	}
}

// loadInstalls lists installed Node versions for one manager root.
func loadInstalls(manager, root string) []*nodeInstall {
	var out []*nodeInstall
	add := func(dir, name string, inst string) {
		v, ok := parseVersion(name)
		if !ok || !isRealDir(dir) {
			return
		}
		out = append(out, &nodeInstall{
			manager: manager, root: root, dir: dir, ver: v,
			lib: filepath.Join(inst, "lib", "node_modules"), bin: filepath.Join(inst, "bin"),
		})
	}
	scanDir := func(parent string, installation string) {
		for _, e := range listDir(parent) {
			dir := filepath.Join(parent, e.Name())
			inst := dir
			if installation != "" {
				inst = filepath.Join(dir, installation)
			}
			add(dir, e.Name(), inst)
		}
	}
	switch manager {
	case "nvm":
		scanDir(filepath.Join(root, "versions", "node"), "")
	case "fnm":
		scanDir(filepath.Join(root, "node-versions"), "installation")
	case "asdf":
		scanDir(filepath.Join(root, "installs", "nodejs"), "")
	case "volta":
		scanDir(filepath.Join(root, "tools", "image", "node"), "")
	case "mise":
		for _, sub := range []string{"node", "nodejs"} {
			parent := filepath.Join(root, "installs", sub)
			before := len(out)
			scanDir(parent, "")
			// mise keeps alias symlinks ("20", "lts", "latest") next to versions.
			for _, e := range listDir(parent) {
				if e.Type()&os.ModeSymlink == 0 {
					continue
				}
				link := filepath.Join(parent, e.Name())
				real, err := filepath.EvalSymlinks(link)
				if err != nil {
					continue
				}
				for _, n := range out[before:] {
					if r, err := filepath.EvalSymlinks(n.dir); err == nil && r == real {
						n.extra = append(n.extra, link)
					}
				}
			}
		}
	}
	return out
}

// resolveNvmAlias follows an nvm alias chain (default -> lts/* -> lts/krypton -> v24.x).
func resolveNvmAlias(root, name string) spec {
	val := name
	for i := 0; i < 8; i++ {
		var file string
		if strings.HasPrefix(val, "lts/") {
			file = filepath.Join(root, "alias", "lts", strings.TrimPrefix(val, "lts/"))
		} else {
			if strings.ContainsAny(val, "/\\") || val == "" || val == "." || val == ".." {
				break
			}
			file = filepath.Join(root, "alias", val)
		}
		data, err := readSmall(file)
		if err != nil {
			break
		}
		next := firstLine(data)
		if next == "" || next == val {
			break
		}
		val = next
	}
	return parseSpec(val)
}

// miseDefaults reads the global mise config ([tools] node = "...").
func (s *scanner) miseDefaults() []spec {
	var out []spec
	for _, p := range []string{
		s.envDir("MISE_GLOBAL_CONFIG_FILE"),
		s.envDir("MISE_CONFIG_DIR", "config.toml"),
		s.envDir("XDG_CONFIG_HOME", "mise", "config.toml"),
		s.home(".config", "mise", "config.toml"),
		s.home(".config", "mise.toml"),
	} {
		if p == "" {
			continue
		}
		data, err := readSmall(p)
		if err != nil {
			continue
		}
		var cfg struct {
			Tools map[string]any `toml:"tools"`
		}
		if _, err := toml.Decode(data, &cfg); err != nil {
			continue
		}
		for _, key := range []string{"node", "nodejs"} {
			out = append(out, toolSpecs(cfg.Tools[key])...)
		}
	}
	return out
}

func toolSpecs(v any) []spec {
	var out []spec
	switch t := v.(type) {
	case string:
		for _, f := range strings.Fields(t) {
			out = append(out, parseSpec(f))
		}
	case []any:
		for _, x := range t {
			out = append(out, toolSpecs(x)...)
		}
	case map[string]any:
		out = append(out, toolSpecs(t["version"])...)
	}
	return out
}

// voltaDefault reads tools/user/platform.json ({"node":{"runtime":"20.11.1"}}).
func voltaDefault(root string) []spec {
	data, err := readSmall(filepath.Join(root, "tools", "user", "platform.json"))
	if err != nil {
		return nil
	}
	var pf struct {
		Node struct {
			Runtime string `json:"runtime"`
		} `json:"node"`
	}
	if json.Unmarshal([]byte(data), &pf) != nil || pf.Node.Runtime == "" {
		return nil
	}
	return []spec{parseSpec(pf.Node.Runtime)}
}

// pathNode returns the real path of the `node` found on PATH ("" if none).
func (s *scanner) pathNode() string {
	p, err := s.env.Runner.LookPath("node")
	if err != nil || p == "" {
		return ""
	}
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	return p
}

func within(p, dir string) bool {
	return p == dir || strings.HasPrefix(p, strings.TrimSuffix(dir, "/")+"/")
}

// classify fills the references of every install.
func (s *scanner) classify(byRoot map[string][]*nodeInstall) {
	toolVersions := []spec(nil)
	if data, err := readSmall(s.home(".tool-versions")); err == nil {
		toolVersions = toolVersionsNode(data)
	}
	miseDefs := s.miseDefaults()
	nodeOnPath := s.pathNode()

	for root, list := range byRoot {
		if len(list) == 0 {
			continue
		}
		manager := list[0].manager
		vers := make([]version, len(list))
		for i, n := range list {
			vers[i] = n.ver
		}
		mark := func(sp spec, f func(n *nodeInstall)) {
			if i := sp.bestOrMajor(vers); i >= 0 {
				f(list[i])
			}
		}
		addDefault := func(sp spec, label string) {
			mark(sp, func(n *nodeInstall) { n.defaults = append(n.defaults, label) })
		}
		switch manager {
		case "nvm":
			if sp := resolveNvmAlias(root, "default"); sp.kind != specNone {
				addDefault(sp, "nvm default ("+sp.raw+")")
			}
			for _, e := range listDir(filepath.Join(root, "alias")) {
				name := e.Name()
				if e.IsDir() || name == "default" || strings.HasPrefix(name, ".") {
					continue
				}
				sp := resolveNvmAlias(root, name)
				mark(sp, func(n *nodeInstall) { n.aliases = append(n.aliases, "nvm alias "+name) })
			}
		case "fnm":
			for _, e := range listDir(filepath.Join(root, "aliases")) {
				real, err := filepath.EvalSymlinks(filepath.Join(root, "aliases", e.Name()))
				if err != nil {
					continue
				}
				vdir := fnmVersionDir(real)
				for _, n := range list {
					nd, err := filepath.EvalSymlinks(n.dir)
					if err != nil || nd != vdir {
						continue
					}
					if e.Name() == "default" {
						n.defaults = append(n.defaults, "fnm default")
					} else {
						n.aliases = append(n.aliases, "fnm alias "+e.Name())
					}
				}
			}
		case "mise":
			for _, sp := range miseDefs {
				addDefault(sp, "mise global ("+sp.raw+")")
			}
			for _, sp := range toolVersions {
				addDefault(sp, "~/.tool-versions ("+sp.raw+")")
			}
		case "asdf":
			for _, sp := range toolVersions {
				addDefault(sp, "~/.tool-versions ("+sp.raw+")")
			}
		case "volta":
			for _, sp := range voltaDefault(root) {
				addDefault(sp, "volta default ("+sp.raw+")")
			}
		}
		for _, pin := range s.projects.pins {
			src := s.env.Pretty(pin.source)
			mark(pin.spec, func(n *nodeInstall) { n.pins = append(n.pins, src) })
		}
		for _, n := range list {
			real, err := filepath.EvalSymlinks(n.dir)
			if err != nil {
				real = n.dir
			}
			if nodeOnPath != "" && (within(nodeOnPath, n.dir) || within(nodeOnPath, real)) {
				n.onPath = true
			}
			n.running = s.procs.usesDir(n.dir)
			if real != n.dir {
				n.running += s.procs.usesDir(real)
			}
			n.globals = globalPackages(n.lib)
		}
	}
}

// nodeVersions emits one item per installed Node version (nvm, fnm, mise,
// asdf, volta) and the npm -g leftovers found in them.
func (s *scanner) nodeVersions() {
	byRoot := map[string][]*nodeInstall{}
	var all []*nodeInstall
	roots := s.managerRoots()
	for _, manager := range []string{"nvm", "fnm", "mise", "asdf", "volta"} {
		for _, root := range roots[manager] {
			list := loadInstalls(manager, root)
			var keep []*nodeInstall
			for _, n := range list {
				if s.allowed(n.dir) {
					keep = append(keep, n)
				}
			}
			byRoot[root] = keep
			all = append(all, keep...)
		}
	}
	s.classify(byRoot)

	// Global packages available elsewhere: kept versions and npm prefixes.
	elsewhere := map[string]bool{}
	for _, n := range all {
		if n.referenced() {
			for _, g := range n.globals {
				elsewhere[g] = true
			}
		}
	}
	for _, lib := range s.prefixLibs(true) {
		for _, g := range globalPackages(lib) {
			elsewhere[g] = true
		}
	}
	managersOf := map[version][]string{}
	for _, n := range all {
		managersOf[n.ver] = append(managersOf[n.ver], n.manager)
	}
	lastUse := s.fnmLastUse()

	sort.Slice(all, func(i, j int) bool { return all[i].dir < all[j].dir })
	for _, n := range all {
		if s.ctx.Err() != nil {
			return
		}
		s.emitNodeVersion(n, elsewhere, managersOf[n.ver], lastUse)
	}
	s.npmLeftovers(all)
}

func (s *scanner) emitNodeVersion(n *nodeInstall, elsewhere map[string]bool, managers []string, lastUse map[string]time.Time) {
	name := fmt.Sprintf("node %s (%s)", n.ver, n.manager)
	it := s.base("node-version", n.dir, name, core.RiskModerate)
	paths := append([]string{n.dir}, n.extra...)
	if len(n.extra) > 0 {
		it.Paths = paths
		it.Location = s.env.Pretty(n.dir)
	} else {
		it.Path = n.dir
	}
	it.Meta["manager"] = n.manager
	it.Meta["version"] = n.ver.String()
	if len(n.globals) > 0 {
		it.Meta["globals"] = joinLimit(n.globals, 12)
	}
	var unique []string
	for _, g := range n.globals {
		if !elsewhere[g] {
			unique = append(unique, g)
		}
	}
	if len(unique) > 0 {
		it.Meta["globals_only_here"] = joinLimit(unique, 12)
	}
	var others []string
	for _, m := range managers {
		if m != n.manager {
			others = append(others, m)
		}
	}
	if len(others) > 0 {
		it.Meta["also_in"] = strings.Join(others, ", ")
	}
	var reasons []string
	reasons = append(reasons, n.defaults...)
	reasons = append(reasons, n.aliases...)
	if len(n.pins) > 0 {
		reasons = append(reasons, "pinned by "+joinLimit(n.pins, 3))
	}
	if n.onPath {
		reasons = append(reasons, "`node` on PATH")
	}
	if n.running > 0 {
		reasons = append(reasons, itoa(n.running)+" running process(es)")
	}
	if len(reasons) > 0 {
		it.Meta["kept_because"] = strings.Join(reasons, "; ")
	}

	it.LastUsed = newestMtime(n.dir, n.bin, n.lib)
	if real, err := filepath.EvalSymlinks(n.dir); err == nil {
		if t := lastUse[real]; t.After(it.LastUsed) {
			it.LastUsed = t
			it.Meta["last_used_source"] = "fnm shell link"
		}
	}

	v := strings.TrimPrefix(n.ver.String(), "v")
	reinstall := map[string]string{
		"nvm": "nvm install " + v, "fnm": "fnm install " + v, "mise": "mise install node@" + v,
		"asdf": "asdf install nodejs " + v, "volta": "volta install node@" + v,
	}[n.manager]
	it.Note = fmt.Sprintf("Node.js %s installed by %s; `%s` re-downloads it (~50 MB) but global npm packages installed under it are lost.",
		n.ver, n.manager, reinstall)

	switch {
	case n.running > 0:
		it.Risk = core.RiskCaution
		it.Warn = "in use by " + itoa(n.running) + " running process(es) — stop them first"
		it.Selectable = false
	case n.onPath:
		it.Risk = core.RiskCaution
		it.Warn = "this is the `node` on your PATH"
	case len(n.defaults) > 0:
		it.Risk = core.RiskCaution
		it.Warn = "default version: " + strings.Join(n.defaults, ", ")
	case len(n.pins) > 0:
		it.Warn = "pinned by " + itoa(len(n.pins)) + " project file(s): " + joinLimit(n.pins, 2)
	case len(n.aliases) > 0:
		it.Warn = "referenced by " + strings.Join(n.aliases, ", ")
	case len(unique) > 0:
		// Unreferenced, but deleting it also deletes CLIs found nowhere else.
		it.Warn = "global packages installed only here: " + joinLimit(unique, 4)
	default:
		// Unreferenced: recommended, unless the process list is unknown (it could be running).
		it.Recommended = s.procs.ok
	}
	s.sized(it, paths, false)
}

// prefixLibs returns global lib/node_modules dirs of npm prefixes (not
// managed by a version manager). withSystem adds Homebrew / /usr/local.
func (s *scanner) prefixLibs(withSystem bool) []string {
	cands := []string{
		s.envDir("npm_config_prefix", "lib", "node_modules"),
		s.envDir("NPM_CONFIG_PREFIX", "lib", "node_modules"),
		s.home(".local", "lib", "node_modules"),
		s.home(".npm-global", "lib", "node_modules"),
		s.home(".npm-packages", "lib", "node_modules"),
	}
	if withSystem {
		cands = append(cands, s.p.sysPrefixes...)
	}
	return uniqDirs(cands...)
}

// npmLeftoverName matches the temp dirs npm leaves behind when an `npm i -g`
// (typically an AI CLI self-update) is interrupted: .<name>-XXXXXXXX
var npmLeftoverName = regexp.MustCompile(`^\.[A-Za-z0-9@._-]+-[A-Za-z0-9]{8}$`)

const npmLeftoverMinAge = 24 * time.Hour

func (s *scanner) npmLeftovers(installs []*nodeInstall) {
	type lib struct{ dir, owner string }
	var libs []lib
	for _, n := range installs {
		libs = append(libs, lib{n.lib, fmt.Sprintf("%s %s", n.manager, n.ver)})
	}
	for _, l := range s.prefixLibs(false) {
		libs = append(libs, lib{l, "npm prefix " + s.env.Pretty(filepath.Dir(filepath.Dir(l)))})
	}
	for _, l := range libs {
		dirs := []string{l.dir}
		for _, e := range listDir(l.dir) {
			if e.IsDir() && strings.HasPrefix(e.Name(), "@") {
				dirs = append(dirs, filepath.Join(l.dir, e.Name()))
			}
		}
		for _, d := range dirs {
			for _, e := range listDir(d) {
				if s.ctx.Err() != nil {
					return
				}
				if !e.IsDir() || !npmLeftoverName.MatchString(e.Name()) {
					continue
				}
				p := filepath.Join(d, e.Name())
				mt := newestMtime(p)
				if s.env.Now.Sub(mt) < npmLeftoverMinAge || !s.allowed(p) || s.procs.usesDir(p) > 0 {
					continue
				}
				rel := strings.TrimPrefix(p, l.dir+"/")
				it := s.base("npm-global-leftover", p, rel+" (npm -g leftover, "+l.owner+")", core.RiskSafe)
				it.Path = p
				it.LastUsed = mt
				it.Recommended = true
				it.Note = "Temp directory left by an interrupted `npm install -g` (often an AI CLI self-update); npm never uses it again."
				it.Meta["owner"] = l.owner
				s.sized(it, []string{p}, false)
			}
		}
	}
}
