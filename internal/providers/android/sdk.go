package android

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
)

// sdkRoot is one Android SDK location found on the machine.
type sdkRoot struct {
	loc     location
	sources []string
}

// sdkMarkers are entries that make a directory look like an Android SDK.
var sdkMarkers = []string{"platform-tools", "platforms", "build-tools", "system-images", "ndk", "cmdline-tools", "emulator", "licenses"}

func looksLikeSDK(dir string) bool {
	for _, m := range sdkMarkers {
		if exists(filepath.Join(dir, m)) {
			return true
		}
	}
	return false
}

const srcLocalProps = "local.properties sdk.dir"

var reStudioSDK = regexp.MustCompile(`androidSdkAbsolutePath"\s+value="([^"]+)"`)

// studioSDKPaths reads options/android.sdk.path.xml of every Android Studio
// settings directory.
func (s *scan) studioSDKPaths() []string {
	var out []string
	ms, _ := fsx.Glob(s.ctx, filepath.Join(s.env.Home, "Library", "Application Support", "Google", "AndroidStudio*", "options", "android.sdk.path.xml"))
	for _, m := range ms {
		for _, sm := range reStudioSDK.FindAllStringSubmatch(readSmall(m), -1) {
			v := strings.ReplaceAll(sm[1], "$USER_HOME$", s.env.Home)
			if filepath.IsAbs(v) {
				out = append(out, filepath.Clean(v))
			}
		}
	}
	return out
}

// findSDKs resolves every SDK location, in the documented order: env
// ANDROID_HOME, ANDROID_SDK_ROOT, shell profile exports, Android Studio's
// android.sdk.path.xml, projects' local.properties sdk.dir, the default
// ~/Library/Android/sdk, then Homebrew's android-commandlinetools.
// Locations are deduplicated by real path.
func (s *scan) findSDKs(pi *projectInfo) []*sdkRoot {
	var cands []candidate
	cands = append(cands, s.varCandidates("ANDROID_HOME", "")...)
	cands = append(cands, s.varCandidates("ANDROID_SDK_ROOT", "")...)
	for _, p := range s.studioSDKPaths() {
		cands = append(cands, candidate{p, "Android Studio settings"})
	}
	if pi != nil {
		for _, p := range uniqSorted(pi.sdkDirs) {
			cands = append(cands, candidate{p, srcLocalProps})
		}
	}
	cands = append(cands, candidate{filepath.Join(s.env.Home, "Library", "Android", "sdk"), "default (~/Library/Android/sdk)"})
	for _, p := range s.p.extraSDKs {
		cands = append(cands, candidate{p, "Homebrew android-commandlinetools"})
	}

	var out []*sdkRoot
	byReal := map[string]*sdkRoot{}
	for _, c := range cands {
		loc := s.locate(c.Path)
		if loc.Place == placeAbsent || (c.Source == srcLocalProps && !loc.Exists()) {
			// A committed local.properties may name another machine's SDK.
			continue
		}
		if r := byReal[loc.Real]; r != nil {
			if !containsStr(r.sources, c.Source) {
				r.sources = append(r.sources, c.Source)
			}
			continue
		}
		if loc.Exists() && !looksLikeSDK(c.Path) {
			continue
		}
		r := &sdkRoot{loc: loc, sources: []string{c.Source}}
		byReal[loc.Real] = r
		out = append(out, r)
	}
	return out
}

func containsStr(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// sdk emits the items of one SDK root.
func (s *scan) sdk(r *sdkRoot, pi *projectInfo, avds *avdSet) {
	if r.loc.Place != placeInternal {
		it := s.base("android-sdk", "Android SDK", core.RiskModerate)
		it.Path = r.loc.Path
		it.Warn = s.placeWarn(r.loc)
		it.Note = "Android SDK stored outside the internal home volume; its packages are never deleted from here."
		it.Meta["source"] = strings.Join(r.sources, ", ")
		it.Meta["real_path"] = r.loc.Real
		if r.loc.Volume != "" {
			it.Name += " on " + strings.TrimPrefix(r.loc.Volume, "/Volumes/")
		}
		if r.loc.Place == placeOutside && !r.loc.Ghost {
			it.Method = core.MethodReport
			it.Selectable = false
			s.add(it, []string{r.loc.Real}, sizeOpt{})
			return
		}
		s.report(it)
		return
	}
	root := r.loc.Path
	s.systemImages(root, avds)
	s.ndks(root, pi)
	s.versioned(root, pi, "build-tools")
	s.versioned(root, pi, "platforms")
	s.versioned(root, pi, "cmake")
	s.versioned(root, pi, "sources")
	s.sdkLegacy(root, pi)
}

// emitPkg emits an SDK package item, or a report when the path is a symlink
// or lives elsewhere (another volume...).
func (s *scan) emitPkg(it *core.Item, opt sizeOpt) {
	loc := s.locate(it.Path)
	if !loc.Deletable() {
		if loc.Place == placeAbsent {
			return
		}
		it.Warn = s.placeWarn(loc)
		s.report(it)
		return
	}
	s.add(it, nil, opt)
}

var imageTags = map[string]string{
	"default": "AOSP", "google_apis": "Google APIs", "google_apis_playstore": "Google Play",
	"google_atd": "ATD", "aosp_atd": "AOSP ATD", "android-tv": "TV", "google-tv": "Google TV",
	"android-wear": "Wear OS", "android-automotive": "Automotive", "android-desktop": "Desktop",
	"google_apis_ps16k": "Google APIs 16k", "google_apis_playstore_ps16k": "Google Play 16k",
}

func (s *scan) systemImages(root string, avds *avdSet) {
	ms, _ := fsx.Glob(s.ctx, filepath.Join(root, "system-images", "*", "*", "*"))
	for _, dir := range ms {
		fi, err := fsx.Lstat(s.ctx, dir)
		if err != nil || (!fi.IsDir() && fi.Mode()&os.ModeSymlink == 0) {
			continue
		}
		abi := filepath.Base(dir)
		tag := filepath.Base(filepath.Dir(dir))
		apiDir := filepath.Base(filepath.Dir(filepath.Dir(dir)))
		api := strings.TrimPrefix(apiDir, "android-")
		rel := "system-images/" + apiDir + "/" + tag + "/" + abi
		tagName := imageTags[tag]
		if tagName == "" {
			tagName = tag
		}
		it := s.base("android-system-image", "System image · API "+api+" · "+tagName+" · "+abi, core.RiskModerate)
		it.Path = dir
		it.Meta = map[string]string{"api": api, "tag": tag, "abi": abi, "package": strings.ReplaceAll(rel, "/", ";")}
		it.Note = "Emulator system image; re-download it with the SDK Manager (1.5-7 GB) to create or boot an AVD with it."
		users := avds.images[rel]
		if len(users) > 0 {
			it.Meta["used_by"] = strings.Join(users, ", ")
		}
		switch {
		case s.arm64 && isX86(abi):
			it.Recommended = true
			it.Note = abi + " image: cannot boot on Apple Silicon. " + it.Note
		case len(users) > 0:
			it.Selectable = false
			it.Note = "Used by AVD " + strings.Join(users, ", ") + "; delete the AVD first if you no longer need it."
		case !avds.complete:
			it.Warn = "an AVD folder is not reachable — cannot tell whether an emulator uses this image"
		default:
			it.Recommended = true
		}
		s.emitPkg(it, sizeOpt{})
	}
}

func (s *scan) ndks(root string, pi *projectInfo) {
	dir := filepath.Join(root, "ndk")
	vers := dirNames(s.ctx, dir)
	keep := newest(vers, s.keepLatest())
	for _, v := range vers {
		it := s.base("android-ndk", "NDK "+v, core.RiskModerate)
		it.Path = filepath.Join(dir, v)
		it.Note = "Native Development Kit (C++ builds: RN new architecture, Hermes, native modules); AGP re-downloads it when a project needs it (~1 GB download)."
		if rev := parseProperties(readSmall(filepath.Join(it.Path, "source.properties")))["Pkg.Revision"]; rev != "" {
			it.Meta["revision"] = rev
		}
		s.keepOrRecommend(it, pi, pi.ndk[v], keep[v])
		s.emitPkg(it, sizeOpt{})
	}
	if bundle := filepath.Join(root, "ndk-bundle"); exists(bundle) {
		it := s.base("android-ndk-bundle", "Legacy NDK (ndk-bundle)", core.RiskModerate)
		it.Path = bundle
		it.Note = "Deprecated single NDK location replaced by side-by-side ndk/<version> since AGP 4; re-installable from the SDK Manager."
		if pi.ndkPathRef {
			it.Meta["note"] = "a project pins ndk.dir / ndkPath"
		} else {
			it.Recommended = pi.known()
		}
		s.emitPkg(it, sizeOpt{})
	}
}

// keepOrRecommend applies the "used by a project / newest kept / unused"
// policy to a versioned SDK package.
func (s *scan) keepOrRecommend(it *core.Item, pi *projectInfo, users map[string]bool, newestKept bool) {
	switch {
	case len(users) > 0:
		list := uniqSorted(users)
		it.Meta["used_by"] = s.projectList(list)
		it.Note += " Used by " + plural(len(list), "project", "projects") + "."
	case newestKept:
		it.Meta["kept"] = "newest installed version"
		if n := s.keepLatest(); n > 1 {
			it.Meta["kept"] = "one of the " + strconv.Itoa(n) + " newest installed versions (keep_latest)"
		}
	case pi.known():
		it.Recommended = true
	default:
		it.Meta["note"] = "no project roots scanned — usage unknown"
	}
}

var reLeftover = regexp.MustCompile(`^\d+(\.\d+)*(-rc\d+)?-\d+$`)

// versioned handles build-tools/<ver>, platforms/android-<N>, cmake/<ver> and
// sources/android-<N>: keep what projects use and the newest, propose the rest.
func (s *scan) versioned(root string, pi *projectInfo, kind string) {
	dir := filepath.Join(root, kind)
	names := dirNames(s.ctx, dir)
	if len(names) == 0 {
		return
	}
	var clean []string
	for _, n := range names {
		if !reLeftover.MatchString(n) {
			clean = append(clean, n)
		}
	}
	keep := newest(clean, s.keepLatest())
	for _, n := range names {
		p := filepath.Join(dir, n)
		ver := strings.TrimPrefix(n, "android-")
		var it *core.Item
		var users map[string]bool
		switch kind {
		case "build-tools":
			it = s.base("android-build-tools", "Build-tools "+n, core.RiskModerate)
			it.Note = "SDK build tools (aapt2, d8, apksigner); AGP downloads the version a project needs automatically (~60-150 MB)."
			users = pi.buildTools[n]
		case "platforms":
			it = s.base("android-platform", "SDK platform API "+ver, core.RiskModerate)
			it.Note = "android.jar to compile against (compileSdk); AGP downloads the missing platform automatically (~60-150 MB)."
			users = pi.compileSdk[ver]
			if users == nil {
				if parts := versionParts(ver); len(parts) > 0 {
					users = pi.compileSdk[strconv.Itoa(parts[0])]
				}
			}
		case "cmake":
			it = s.base("android-cmake", "CMake "+n+" (SDK)", core.RiskModerate)
			it.Note = "CMake used by externalNativeBuild; AGP re-downloads the version a project asks for (~50-100 MB)."
			users = pi.cmake[n]
			if n == "3.22.1" && users == nil {
				users = map[string]bool{"AGP 8 default (React Native)": true}
			}
		case "sources":
			it = s.base("android-sources", "SDK sources API "+ver, core.RiskSafe)
			it.Note = "Android framework sources, only used to browse code in the IDE; re-downloadable (~50-100 MB)."
			users = pi.compileSdk[ver]
			if users != nil || keep[n] {
				it.Risk = core.RiskModerate
			}
		}
		it.Path = p
		if reLeftover.MatchString(n) {
			it.Risk = core.RiskSafe
			it.Recommended = true
			it.Name += " (leftover of a failed install)"
		} else if kind == "cmake" && users != nil && users["AGP 8 default (React Native)"] {
			it.Meta["kept"] = "AGP 8 default version"
		} else {
			s.keepOrRecommend(it, pi, users, keep[n])
		}
		s.emitPkg(it, sizeOpt{})
	}
}

// sdkLegacy handles temp/partial downloads, the deprecated SDK Tools, HAXM on
// Apple Silicon and superseded cmdline-tools.
func (s *scan) sdkLegacy(root string, pi *projectInfo) {
	var tmp []string
	for _, n := range []string{".temp", ".downloadIntermediates", "temp"} {
		if p := filepath.Join(root, n); fsx.IsDir(p) && s.locate(p).Deletable() {
			tmp = append(tmp, p)
		}
	}
	if len(tmp) > 0 {
		it := s.base("android-sdk-temp", "SDK Manager temp & partial downloads", core.RiskSafe)
		it.Location = filepath.Join(root, ".temp")
		it.Paths = tmp
		it.ProcessGuard = []string{studioProcess}
		it.Note = "Leftovers of interrupted SDK updates; nothing uses them."
		it.LastUsed = maxTime(mtimes(tmp)...)
		if s.isStudioRunning() {
			it.Warn = s.studioWarn("Android Studio is running (may be updating the SDK) — quit it first")
		}
		s.add(it, nil, sizeOpt{})
	}
	latest := exists(filepath.Join(root, "cmdline-tools", "latest"))
	if tools := filepath.Join(root, "tools"); latest && exists(tools) {
		it := s.base("android-sdk-legacy-tools", "Legacy SDK Tools (tools/)", core.RiskModerate)
		it.Path = tools
		it.Recommended = true
		it.Note = "Deprecated SDK Tools 26.x, replaced by cmdline-tools/latest which is installed."
		s.emitPkg(it, sizeOpt{})
	}
	if latest {
		for _, n := range dirNames(s.ctx, filepath.Join(root, "cmdline-tools")) {
			if n == "latest" {
				continue
			}
			it := s.base("android-cmdline-tools-old", "Old cmdline-tools ("+n+")", core.RiskModerate)
			it.Path = filepath.Join(root, "cmdline-tools", n)
			it.Recommended = true
			it.Note = "Superseded copy of the SDK command-line tools; cmdline-tools/latest is the one in use."
			s.emitPkg(it, sizeOpt{})
		}
	}
	if haxm := filepath.Join(root, "extras", "intel"); s.arm64 && exists(haxm) {
		it := s.base("android-haxm", "Intel HAXM (extras/intel)", core.RiskModerate)
		it.Path = haxm
		it.Recommended = true
		it.Note = "Intel hardware accelerator for x86 emulators; useless on Apple Silicon (leftover of an Intel Mac migration)."
		s.emitPkg(it, sizeOpt{})
	}
}

func mtimes(ps []string) []time.Time {
	out := make([]time.Time, 0, len(ps))
	for _, p := range ps {
		out = append(out, mtime(p))
	}
	return out
}
