package android

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
)

// studioProcess is the executable name of Android Studio (Contents/MacOS/studio).
const studioProcess = "studio"

// studioApps returns the dataDirectoryName ("AndroidStudio2025.1.1") of every
// installed Android Studio bundle.
func (s *scan) studioApps() map[string]string {
	out := map[string]string{} // dataDirectoryName -> app path
	var apps []string
	for _, d := range s.p.appDirs {
		ms, _ := filepath.Glob(filepath.Join(d, "Android Studio*.app"))
		apps = append(apps, ms...)
	}
	if s.p.mdfind && s.env.Has("mdfind") {
		ctx, cancel := context.WithTimeout(s.ctx, 3*time.Second)
		o, err := s.env.Output(ctx, "", "mdfind", "kMDItemCFBundleIdentifier == 'com.google.android.studio*'")
		cancel()
		if err == nil {
			for _, l := range strings.Split(string(o), "\n") {
				if l = strings.TrimSpace(l); strings.HasSuffix(l, ".app") {
					apps = append(apps, l)
				}
			}
		}
	}
	for _, a := range apps {
		var pi struct {
			DataDirectoryName string `json:"dataDirectoryName"`
		}
		if json.Unmarshal([]byte(readSmall(filepath.Join(a, "Contents", "Resources", "product-info.json"))), &pi) == nil && pi.DataDirectoryName != "" {
			out[pi.DataDirectoryName] = a
		}
	}
	return out
}

// studio emits Android Studio caches, logs and settings of old versions.
func (s *scan) studio() {
	g := filepath.Join(s.env.Home, "Library")
	cfgs, _ := filepath.Glob(filepath.Join(g, "Application Support", "Google", "AndroidStudio*"))
	caches, _ := filepath.Glob(filepath.Join(g, "Caches", "Google", "AndroidStudio*"))
	logs, _ := filepath.Glob(filepath.Join(g, "Logs", "Google", "AndroidStudio*"))
	if len(cfgs)+len(caches)+len(logs) == 0 {
		return
	}
	installed := s.studioApps()
	current := map[string]bool{}
	for n := range installed {
		current[n] = true
	}
	// Versions are ranked by their most recently used settings directories
	// (the caches when there are no settings).
	pool := cfgs
	if len(pool) == 0 {
		pool = caches
	}
	if len(current) == 0 {
		// No bundle found (custom location, Toolbox...): be conservative and
		// treat the most recently used settings directories (keep_latest of
		// them) as the current ones.
		for _, n := range newestDirs(pool, s.keepLatest()) {
			current[n] = true
		}
	} else if keep := s.keepLatest(); keep > 1 {
		// config keep_latest > 1: besides the installed versions, keep the
		// most recently used other versions until keep versions are kept.
		kept := 0
		for _, p := range pool {
			if current[filepath.Base(p)] {
				kept++
			}
		}
		for _, n := range newestDirs(pool, len(pool)) {
			if kept >= keep {
				break
			}
			if !current[n] {
				current[n] = true
				kept++
			}
		}
	}
	running := s.isStudioRunning()

	for _, p := range caches {
		n := filepath.Base(p)
		it := s.base("android-studio-cache", "Android Studio caches · "+strings.TrimPrefix(n, "AndroidStudio"), core.RiskSafe)
		it.Path = p
		if current[n] {
			it.Risk = core.RiskModerate
			it.ProcessGuard = []string{studioProcess}
			it.Note = "Indexes and caches of the Android Studio you use; rebuilt on next launch (re-indexing takes minutes)."
			if running {
				it.Warn = "Android Studio is running — quit it first"
			}
		} else {
			it.Recommended = true
			it.Note = "Caches of an Android Studio version that is no longer installed; nothing reads them."
		}
		s.emitPkg(it, sizeOpt{lastUsedFromNewest: true})
	}
	for _, p := range logs {
		n := filepath.Base(p)
		it := s.base("android-studio-logs", "Android Studio logs · "+strings.TrimPrefix(n, "AndroidStudio"), core.RiskSafe)
		it.Path = p
		it.Note = "idea.log and crash reports; Android Studio writes new ones."
		if current[n] {
			it.ProcessGuard = []string{studioProcess}
			if running {
				it.Warn = "Android Studio is running — quit it first"
			}
		}
		s.emitPkg(it, sizeOpt{lastUsedFromNewest: true})
	}
	hasCurrentCfg := false
	for _, p := range cfgs {
		if current[filepath.Base(p)] {
			hasCurrentCfg = true
		}
	}
	for _, p := range cfgs {
		n := filepath.Base(p)
		if current[n] {
			continue // settings in use: never proposed
		}
		it := s.base("android-studio-config", "Android Studio settings · "+strings.TrimPrefix(n, "AndroidStudio")+" (old version)", core.RiskModerate)
		it.Path = p
		it.LastUsed = mtime(p)
		it.Note = "Settings and plugins of an older Android Studio, only read to migrate settings on upgrade; the current version has its own copy."
		it.Recommended = hasCurrentCfg
		s.emitPkg(it, sizeOpt{lastUsedFromNewest: true})
	}
}

// newestDirs returns the base names of the n most recently modified paths.
func newestDirs(ps []string, n int) []string {
	c := append([]string(nil), ps...)
	sort.Slice(c, func(i, j int) bool { return mtime(c[i]).After(mtime(c[j])) })
	var out []string
	for i := 0; i < n && i < len(c); i++ {
		out = append(out, filepath.Base(c[i]))
	}
	return out
}

// userCache emits the contents of ~/.android/{cache,build-cache,breakpad}:
// SDK repository XML, the legacy AGP (< 4.1) build cache and emulator crash
// dumps. The folders themselves and every other ~/.android file (adbkey,
// debug.keystore, emulator settings) are kept.
func (s *scan) userCache() {
	home := filepath.Join(s.env.Home, ".android")
	for _, c := range s.varCandidates("ANDROID_USER_HOME", "") {
		home = c.Path
		break
	}
	var ps []string
	for _, sub := range []string{"cache", "build-cache", "breakpad"} {
		dir := filepath.Join(home, sub)
		if s.locate(dir).Place != placeInternal {
			continue
		}
		ents, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range ents {
			p := filepath.Join(dir, e.Name())
			if e.Type()&os.ModeSymlink == 0 && !s.env.IsProtected(p) {
				ps = append(ps, p)
			}
		}
	}
	if len(ps) == 0 {
		return
	}
	it := s.base("android-user-cache", "Android SDK repo cache, legacy build cache & crash dumps", core.RiskSafe)
	it.Location = home + "/{cache,build-cache,breakpad}"
	it.ID = itemID("android-user-cache", home)
	it.Paths = ps
	it.ProcessGuard = []string{studioProcess}
	it.Note = "SDK Manager repository cache, the pre-AGP 4.1 build cache and emulator crash dumps; recreated when needed."
	if s.isStudioRunning() {
		it.Warn = "Android Studio is running — quit it first"
	}
	s.add(it, nil, sizeOpt{lastUsedFromNewest: true})
}
