package system

import (
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
)

// ------------------------------------------------------------------ Zed

var zedProc = []string{"zed"}

// zed: downloaded language servers / runtimes and caches when Zed is
// installed; the whole data folder when it is not (settings live in
// ~/.config/zed and are never touched).
func (s *scan) zed() {
	data := s.appSupport("Zed")
	caches := s.home("Library/Caches/Zed")
	logs := s.home("Library/Logs/Zed")
	if !isDir(data) && !isDir(caches) && !isDir(logs) {
		return
	}
	if pl := s.locate(data); pl.External || pl.Link {
		return
	}
	if !s.appInstalled("dev.zed.Zed", "Zed", "Zed Preview") {
		it := s.newItem("zed-leftover", core.CatIDE, "Zed data (app not installed)", core.RiskCaution)
		it.ID = itemID(it.Kind, data)
		for _, p := range []string{data, caches, logs} {
			if isDir(p) {
				it.Paths = append(it.Paths, p)
			}
		}
		it.Location = s.home("Library") + "/…/Zed"
		it.Note = "Data of the Zed editor, which is not installed anymore: downloaded language servers, AI agent binaries, but also its local database (AI threads, workspace state). Settings in ~/.config/zed are kept."
		s.publish(it, pubOpts{placeholder: true, newest: true})
		return
	}
	var runtimes []string
	for _, sub := range []string{"languages", "node", "prettier", "copilot", "external_agents", "extensions/work"} {
		if p := filepath.Join(data, filepath.FromSlash(sub)); isDir(p) {
			runtimes = append(runtimes, p)
		}
	}
	if len(runtimes) > 0 {
		it := s.newItem("zed-language-servers", core.CatIDE, "Zed downloaded language servers & runtimes", core.RiskModerate)
		it.ID = itemID(it.Kind, data)
		it.Location = data + "/…"
		it.Paths = runtimes
		it.ProcessGuard = zedProc
		it.Note = "Language servers, Node runtime, Prettier and agent binaries Zed downloads on demand; downloaded again the next time a file needs them."
		s.publish(it, pubOpts{placeholder: true, newest: true})
	}
	var cache []string
	for _, p := range []string{caches, logs, filepath.Join(data, "hang_traces")} {
		if isDir(p) {
			cache = append(cache, p)
		}
	}
	if len(cache) > 0 {
		it := s.newItem("zed-caches", core.CatIDE, "Zed caches & logs", core.RiskSafe)
		it.ID = itemID(it.Kind, data)
		it.Location = s.home("Library") + "/…/Zed"
		it.Paths = cache
		it.ProcessGuard = zedProc
		it.Note = "Zed caches, logs and hang traces; rebuilt automatically."
		s.publish(it, pubOpts{newest: true})
	}
}

// ------------------------------------------------------------------ JetBrains

// reJetBrains splits "WebStorm2024.3", "IntelliJIdea2025.1", "PyCharmCE2023.2".
var reJetBrains = regexp.MustCompile(`^([A-Za-z][A-Za-z-]*?)(\d{4}\.\d+(?:\.\d+)?)$`)

// jetbrainsProc maps a product folder prefix to its launcher name.
var jetbrainsProc = map[string]string{
	"IntelliJIdea": "idea", "IdeaIC": "idea", "WebStorm": "webstorm", "PyCharm": "pycharm", "PyCharmCE": "pycharm",
	"GoLand": "goland", "CLion": "clion", "Rider": "rider", "PhpStorm": "phpstorm", "RubyMine": "rubymine",
	"DataGrip": "datagrip", "RustRover": "rustrover", "DataSpell": "dataspell", "AppCode": "appcode",
	"Aqua": "aqua", "Writerside": "writerside",
}

type jbDir struct {
	product, version, path string
	mtime                  time.Time
}

// jetbrains: per product, keep the newest IDE version; caches of older
// versions (safe), their settings folders (moderate: only used to import
// settings) and all IDE logs (safe). Current caches are proposed as
// moderate (re-indexing takes time). Android Studio lives under Google/ and
// belongs to the android provider.
func (s *scan) jetbrains() {
	roots := map[string]string{
		"caches":   s.home("Library/Caches/JetBrains"),
		"settings": s.appSupport("JetBrains"),
		"logs":     s.home("Library/Logs/JetBrains"),
	}
	dirs := map[string][]jbDir{} // root kind -> dirs
	newest := map[string]string{}
	for kind, root := range roots {
		for _, e := range list(root, false) {
			if !e.dir {
				continue
			}
			m := reJetBrains.FindStringSubmatch(e.name)
			if m == nil {
				continue
			}
			d := jbDir{product: m[1], version: m[2], path: e.path, mtime: e.mtime}
			dirs[kind] = append(dirs[kind], d)
			if kind == "logs" {
				continue // a log folder does not prove the version is installed
			}
			if cur, ok := newest[d.product]; !ok || compareVersions(d.version, cur) > 0 {
				newest[d.product] = d.version
			}
		}
	}
	if len(dirs) == 0 {
		return
	}
	procs := func(ds []jbDir) []string {
		seen := map[string]bool{}
		var out []string
		for _, d := range ds {
			p := jetbrainsProc[d.product]
			if p == "" {
				p = strings.ToLower(d.product)
			}
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
		sort.Strings(out)
		return out
	}
	group := func(kind, name string, risk core.Risk, ds []jbDir, rec bool, note string) {
		if len(ds) == 0 {
			return
		}
		it := s.newItem(kind, core.CatIDE, "", risk)
		it.ID = itemID(kind, filepath.Dir(ds[0].path))
		it.Recommended = rec
		it.ProcessGuard = procs(ds)
		var labels []string
		for _, d := range ds {
			it.Paths = append(it.Paths, d.path)
			it.LastUsed = maxTime(it.LastUsed, d.mtime)
			labels = append(labels, d.product+" "+d.version)
		}
		sort.Strings(labels)
		it.Name = name + " (" + strconv.Itoa(len(ds)) + ")"
		it.Location = filepath.Dir(ds[0].path) + "/…"
		it.Meta = map[string]string{"versions": strings.Join(labels, ", ")}
		it.Note = note
		s.publish(it, pubOpts{placeholder: true, newest: true})
	}
	var oldCaches, curCaches, oldSettings []jbDir
	for _, d := range dirs["caches"] {
		if d.version == newest[d.product] {
			curCaches = append(curCaches, d)
		} else {
			oldCaches = append(oldCaches, d)
		}
	}
	for _, d := range dirs["settings"] {
		if d.version != newest[d.product] {
			oldSettings = append(oldSettings, d)
		}
	}
	group("jetbrains-old-caches", "JetBrains caches of old IDE versions", core.RiskSafe, oldCaches, true,
		"Indexes and caches of IDE versions you upgraded from; the newest version of each IDE is kept.")
	group("jetbrains-old-settings", "JetBrains settings of old IDE versions", core.RiskModerate, oldSettings, false,
		"Settings and plugins folders of IDE versions you upgraded from (only used to import settings into a newer version).")
	group("jetbrains-logs", "JetBrains IDE logs", core.RiskSafe, dirs["logs"], false,
		"IDE logs of every JetBrains version; written again by the running IDE.")
	group("jetbrains-caches", "JetBrains caches of current IDE versions", core.RiskModerate, curCaches, false,
		"Indexes and caches of the IDE versions you use; rebuilt at the next start (re-indexing big projects takes minutes).")
}
