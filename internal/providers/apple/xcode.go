package apple

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
)

// ---------------------------------------------------------------- alternate device sets

// deviceSets are the simulator device sets Xcode keeps outside the default
// one. `xcrun simctl --set <dir> delete all` empties them; Xcode recreates
// devices on demand.
var deviceSets = []struct {
	rel   []string
	title string
	note  string
}{
	{[]string{"Developer", "Xcode", "UserData", "Previews", "Simulator Devices"}, "SwiftUI Previews simulators",
		"Simulator clones used by SwiftUI / #Preview canvases; Xcode recreates them the next time a preview runs."},
	{[]string{"Developer", "Xcode", "UserData", "IB Support", "Simulator Devices"}, "Interface Builder simulators",
		"Simulators Interface Builder uses to render storyboards and XIBs; recreated on demand."},
	{[]string{"Developer", "XCTestDevices"}, "XCTest clone simulators",
		"Clones created for parallel testing (xcodebuild -parallel-testing); recreated by the next parallel test run."},
	{[]string{"Developer", "XCPGDevices"}, "Playground simulators",
		"Simulators used by Xcode Playgrounds; recreated on demand."},
}

func (s *scan) deviceSets() {
	if !s.env.Has("xcrun") {
		return
	}
	for _, set := range deviceSets {
		dir := s.lib(set.rel...)
		ents, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		n := 0
		for _, e := range ents {
			if e.IsDir() && uuidRe.MatchString(e.Name()) {
				n++
			}
		}
		if n == 0 || s.skipPath(dir) {
			continue
		}
		it := s.item("ios-simulator-device-set", core.CatSimulators, dir)
		it.Name = fmt.Sprintf("%s (%d)", set.title, n)
		it.Location = dir
		it.Method = core.MethodCommand
		it.Command = []string{"xcrun", "simctl", "--set", dir, "delete", "all"}
		it.Risk = core.RiskSafe
		it.ProcessGuard = []string{"Xcode"}
		it.Note = set.note
		setMeta(it, "devices", fmt.Sprint(n))
		if !s.applyPlace(it, dir) {
			continue
		}
		s.guardWarn(it)
		// Measured before emitting: never-booted clones weigh nothing and are not worth a line.
		m := s.measure(dir)
		if m.bytes < 1<<20 || s.ctx.Err() != nil {
			continue
		}
		m.apply(it)
		it.LastUsed = maxTime(childrenNewest(dir), m.newest)
		s.emit(it)
	}
}

// ---------------------------------------------------------------- CoreSimulator caches

// coreSimulatorCaches: ~/Library/Developer/CoreSimulator/{Caches,Temp}/*
// (older Xcode kept multi-GB dyld caches there).
func (s *scan) coreSimulatorCaches() {
	base := s.lib("Developer", "CoreSimulator")
	var paths []string
	for _, sub := range []string{"Caches", "Temp"} {
		dir := filepath.Join(base, sub)
		ents, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range ents {
			p := filepath.Join(dir, e.Name())
			if !s.skipPath(p) {
				paths = append(paths, p)
			}
		}
	}
	if len(paths) == 0 {
		return
	}
	m := s.measure(paths...)
	if m.bytes == 0 || s.ctx.Err() != nil {
		return // empty folders: nothing worth showing
	}
	it := s.item("coresimulator-caches", core.CatSimulators, "user-caches")
	it.Name = "CoreSimulator caches"
	setTargets(it, paths, base)
	it.Risk = core.RiskSafe
	it.Method = core.MethodDelete
	it.ProcessGuard = []string{"Simulator"}
	it.Note = "Per-user CoreSimulator caches and temporary files; recreated automatically when a simulator boots."
	m.apply(it)
	it.LastUsed = m.newest
	if !s.applyPlace(it, paths[0]) {
		return
	}
	s.guardWarn(it)
	s.emit(it)
}

// ---------------------------------------------------------------- Xcode versions

// xcodeApps reports installed Xcode versions other than the selected one
// (xcode-select -p). Report only: uninstalling Xcode is the user's call.
func (s *scan) xcodeApps() {
	if !s.env.Has("xcode-select") {
		return
	}
	out, err := s.output(5*time.Second, "xcode-select", "-p")
	if err != nil {
		return
	}
	active := appBundleOf(strings.TrimSpace(string(out)))
	if active == "" {
		return // Command Line Tools selected: cannot tell which Xcode is in use
	}
	activeReal := active
	if r, err := filepath.EvalSymlinks(active); err == nil {
		activeReal = r
	}
	var apps []string
	for _, pat := range []string{s.sys("/Applications/Xcode*.app"), filepath.Join(s.env.Home, "Applications", "Xcode*.app")} {
		m, _ := filepath.Glob(pat)
		apps = append(apps, m...)
	}
	for _, app := range apps {
		fi, err := os.Lstat(app)
		if err != nil || !fi.IsDir() { // symlinks (Xcode.app -> Xcode-16.2.app) are not copies
			continue
		}
		if !fsx.IsDir(filepath.Join(app, "Contents", "Developer")) {
			continue // Xcodes.app and other look-alikes
		}
		real := app
		if r, err := filepath.EvalSymlinks(app); err == nil {
			real = r
		}
		if real == activeReal || s.skipPath(app) {
			continue
		}
		info, _ := readPlistDict(filepath.Join(app, "Contents", "version.plist"))
		ver := pString(info, "CFBundleShortVersionString")
		build := pString(info, "ProductBuildVersion")
		it := s.item("xcode-app", core.CatXcode, app)
		it.Path = app
		it.Name = "Xcode"
		if ver != "" {
			it.Name += " " + ver
		} else {
			it.Name = strings.TrimSuffix(filepath.Base(app), ".app")
		}
		if build != "" {
			it.Name += " (" + build + ")"
		}
		it.Risk = core.RiskCaution
		it.Method = core.MethodReport
		it.Selectable = false
		it.Note = "Extra Xcode install (the selected one is " + filepath.Base(active) + "). Remove it with Xcodes (`xcodes uninstall " + ver +
			"`) or move it to the Trash if no project needs it; re-downloading costs 3-12 GB."
		setMeta(it, "version", ver)
		setMeta(it, "build", build)
		setMeta(it, "active", active)
		if !s.applyPlace(it, app) {
			continue
		}
		s.sizeLater(it, []string{app}, nil)
	}
}

// appBundleOf returns "/Applications/Xcode.app" for ".../Xcode.app/Contents/Developer".
func appBundleOf(devDir string) string {
	i := strings.Index(devDir, ".app/")
	if i < 0 {
		if strings.HasSuffix(devDir, ".app") {
			return devDir
		}
		return ""
	}
	return devDir[:i+4]
}

// ---------------------------------------------------------------- Metal toolchain

// metalToolchainAssets is where the MobileAsset daemon stores the Metal
// toolchain (one <hash>.asset folder per downloaded build).
const metalToolchainAssets = "/System/Library/AssetsV2/com_apple_MobileAsset_MetalToolchain"

// metalToolchain offers `xcodebuild -deleteComponent metalToolchain`: the
// Metal shader compiler Xcode 26+ downloads separately (~700 MB), only needed
// to compile .metal files. The asset is found on disk (<hash>.asset holding
// AssetData, build number in its Info.plist): `xcodebuild -showComponent`
// takes 10-15 s.
func (s *scan) metalToolchain() {
	if !s.env.Has("xcodebuild") {
		return
	}
	base := s.sys(metalToolchainAssets)
	ents, err := os.ReadDir(base)
	if err != nil {
		return
	}
	var dirs, builds []string
	for _, e := range ents {
		dir := filepath.Join(base, e.Name())
		if !e.IsDir() || !strings.HasSuffix(e.Name(), ".asset") || !fsx.IsDir(filepath.Join(dir, "AssetData")) {
			continue // not downloaded (or being downloaded)
		}
		dirs = append(dirs, dir)
		info, _ := readPlistDict(filepath.Join(dir, "Info.plist"))
		if b := pString(pDict(info, "MobileAssetProperties"), "Build"); b != "" {
			builds = append(builds, b)
		}
	}
	if len(dirs) == 0 {
		return
	}
	it := s.item("xcode-metal-toolchain", core.CatXcode, "metalToolchain")
	it.Name = "Metal toolchain"
	if len(builds) > 0 {
		it.Name += " (" + strings.Join(builds, ", ") + ")"
	}
	it.Location = dirs[0]
	if len(dirs) > 1 {
		it.Location = base
	}
	it.Method = core.MethodCommand
	it.Command = []string{"xcodebuild", "-deleteComponent", "metalToolchain"}
	it.Risk = core.RiskModerate
	it.ProcessGuard = []string{"Xcode", "xcodebuild"}
	it.Note = "Metal shader compiler downloaded by Xcode; only needed to build targets with .metal files. Re-download with `xcodebuild -downloadComponent MetalToolchain`."
	setMeta(it, "build", strings.Join(builds, ", "))
	if !s.applyPlace(it, dirs[0]) {
		return
	}
	s.guardWarn(it)
	s.sizeLater(it, dirs, nil)
}
