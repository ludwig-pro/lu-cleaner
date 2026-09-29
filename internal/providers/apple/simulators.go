package apple

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
	"golang.org/x/sys/unix"
)

// ---------------------------------------------------------------- simctl JSON

// simList is `xcrun simctl list -j` (devicetypes, runtimes, devices, pairs).
type simList struct {
	DeviceTypes []simDeviceType        `json:"devicetypes"`
	Runtimes    []simRuntime           `json:"runtimes"`
	Devices     map[string][]simDevice `json:"devices"` // keyed by runtime identifier
	Pairs       map[string]simPair     `json:"pairs"`
}

type simDeviceType struct {
	Identifier string `json:"identifier"`
	Name       string `json:"name"`
}

type simRuntime struct {
	Identifier  string `json:"identifier"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Build       string `json:"buildversion"`
	Platform    string `json:"platform"`
	IsAvailable bool   `json:"isAvailable"`
}

type simDevice struct {
	UDID              string `json:"udid"`
	Name              string `json:"name"`
	State             string `json:"state"` // Shutdown | Booted | Creating | Shutting Down
	IsAvailable       bool   `json:"isAvailable"`
	AvailabilityError string `json:"availabilityError"`
	DataPath          string `json:"dataPath"`
	DataPathSize      int64  `json:"dataPathSize"`
	LogPath           string `json:"logPath"`
	LogPathSize       int64  `json:"logPathSize"`
	LastUsedAt        string `json:"lastUsedAt"` // absent for never-booted devices
	DeviceType        string `json:"deviceTypeIdentifier"`
}

type simPair struct {
	Watch struct {
		UDID string `json:"udid"`
	} `json:"watch"`
	Phone struct {
		UDID string `json:"udid"`
	} `json:"phone"`
}

// runtimeImage is one entry of `xcrun simctl runtime list -j` (keyed by image UUID).
type runtimeImage struct {
	Identifier         string `json:"identifier"`
	RuntimeIdentifier  string `json:"runtimeIdentifier"`
	Version            string `json:"version"`
	Build              string `json:"build"`
	Kind               string `json:"kind"`
	State              string `json:"state"`
	Deletable          bool   `json:"deletable"`
	SizeBytes          int64  `json:"sizeBytes"`
	LastUsedAt         string `json:"lastUsedAt"`
	Path               string `json:"path"`
	MountPath          string `json:"mountPath"`
	RuntimeBundlePath  string `json:"runtimeBundlePath"`
	PlatformIdentifier string `json:"platformIdentifier"`
}

const (
	simctlTimeout     = 45 * time.Second // a cold CoreSimulatorService can take a while
	simStaleRecommend = 60 * day
	simCacheMinAge    = 14 * day
	orphanMinAge      = 24 * time.Hour
	orphanRecommend   = 7 * day
)

var uuidRe = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)

// jsonPayload drops anything printed before the JSON document.
func jsonPayload(b []byte) []byte {
	if i := bytes.IndexByte(b, '{'); i > 0 {
		return b[i:]
	}
	return b
}

func (s *scan) simctlList() (*simList, error) {
	out, err := s.output(simctlTimeout, "xcrun", "simctl", "list", "-j")
	if err != nil {
		return nil, err
	}
	var l simList
	if err := json.Unmarshal(jsonPayload(out), &l); err != nil {
		return nil, err
	}
	if l.Devices == nil {
		return nil, fmt.Errorf("simctl list: no devices section")
	}
	return &l, nil
}

func (s *scan) runtimeImages() (map[string]runtimeImage, error) {
	out, err := s.output(simctlTimeout, "xcrun", "simctl", "runtime", "list", "-j")
	if err != nil {
		return nil, err
	}
	imgs := map[string]runtimeImage{}
	if err := json.Unmarshal(jsonPayload(out), &imgs); err != nil {
		return nil, err
	}
	return imgs, nil
}

// ---------------------------------------------------------------- orchestration

type simDev struct {
	simDevice
	runtime string // runtime identifier (devices map key)
	dir     string // ~/Library/Developer/CoreSimulator/Devices/<UDID>
}

type simScan struct {
	*scan
	list      *simList
	typeNames map[string]string
	rtNames   map[string]string
	paired    map[string]bool
	root      string
	// imgs are the runtime disk images (`simctl runtime list`); imgsKnown is
	// false when that listing failed, so a missing image proves nothing.
	imgs      map[string]runtimeImage
	imgsKnown bool

	bundlesOnce sync.Once
	bundles     map[string]string // runtime identifier -> .simruntime bundle on disk
}

// simulators covers everything that needs simctl: devices (one item each),
// unavailable devices, XCTest recordings, unified logs and system caches
// inside simulators, orphan device folders, runtimes and dyld caches.
func (s *scan) simulators() {
	if !s.env.Has("xcrun") {
		return
	}
	var (
		imgs   map[string]runtimeImage
		imgErr error
		wg     sync.WaitGroup
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		imgs, imgErr = s.runtimeImages()
	}()
	list, err := s.simctlList()
	wg.Wait()
	if err != nil {
		s.logf("apple: simctl list: %v", err)
		return // no Xcode selected (CLT only), CoreSimulator broken...
	}
	ss := &simScan{scan: s, list: list, typeNames: map[string]string{}, rtNames: map[string]string{}, paired: map[string]bool{},
		root: s.lib("Developer", "CoreSimulator", "Devices"), imgs: imgs, imgsKnown: imgErr == nil}
	for _, t := range list.DeviceTypes {
		ss.typeNames[t.Identifier] = t.Name
	}
	for _, r := range list.Runtimes {
		ss.rtNames[r.Identifier] = r.Name
	}
	for _, p := range list.Pairs {
		ss.paired[strings.ToUpper(p.Watch.UDID)] = true
		ss.paired[strings.ToUpper(p.Phone.UDID)] = true
	}
	ss.devices()
	if imgErr == nil {
		ss.runtimes(imgs)
	} else {
		s.logf("apple: simctl runtime list: %v", imgErr)
	}
	ss.dyldCaches(imgs, imgErr == nil)
}

func (ss *simScan) devices() {
	known := map[string]bool{}
	inRoot := 0
	var shutdown []simDev
	for _, rt := range sortedKeys(ss.list.Devices) {
		for _, d := range ss.list.Devices[rt] {
			if d.UDID == "" {
				continue
			}
			known[strings.ToUpper(d.UDID)] = true
			dv := simDev{simDevice: d, runtime: rt, dir: ss.deviceDir(d)}
			if filepath.Dir(dv.dir) == ss.root {
				inRoot++
			}
			if ss.skipPath(dv.dir) {
				continue
			}
			// Recordings are listed for unavailable devices too: the device
			// item Covers its folder, so TopLevel never counts both.
			recordings := ss.attachments(dv)
			if !d.IsAvailable {
				ss.unavailableItem(dv, recordings)
				continue
			}
			ss.deviceItem(dv, recordings)
			if d.State == "Shutdown" {
				shutdown = append(shutdown, dv)
			}
		}
	}
	ss.deviceLogs(shutdown)
	ss.deviceCaches(shutdown)
	// Orphan detection needs proof that simctl describes this folder.
	if inRoot > 0 {
		ss.orphans(known)
	}
}

// deviceDir returns the device folder (parent of dataPath when consistent).
func (ss *simScan) deviceDir(d simDevice) string {
	if d.DataPath != "" {
		dir := filepath.Dir(filepath.Clean(d.DataPath))
		if strings.EqualFold(filepath.Base(dir), d.UDID) && filepath.IsAbs(dir) {
			return dir
		}
	}
	return filepath.Join(ss.root, d.UDID)
}

// runtimeName returns "iOS 26.5" for a runtime identifier.
func (ss *simScan) runtimeName(id string) string {
	if n := ss.rtNames[id]; n != "" {
		return n
	}
	return prettyRuntime(id)
}

// prettyRuntime turns "com.apple.CoreSimulator.SimRuntime.iOS-17-5" into "iOS 17.5".
func prettyRuntime(id string) string {
	s := strings.TrimPrefix(id, "com.apple.CoreSimulator.SimRuntime.")
	plat, ver, ok := strings.Cut(s, "-")
	if !ok {
		return s
	}
	if plat == "xrOS" {
		plat = "visionOS"
	}
	return plat + " " + strings.ReplaceAll(ver, "-", ".")
}

// runtimePlatform returns iOS, watchOS, tvOS or visionOS for a runtime.
func runtimePlatform(rid, platformID string) string {
	if s := strings.TrimPrefix(rid, "com.apple.CoreSimulator.SimRuntime."); s != rid {
		p, _, _ := strings.Cut(s, "-")
		if p == "xrOS" {
			return "visionOS"
		}
		if p != "" {
			return p
		}
	}
	switch {
	case strings.Contains(platformID, "iphone"):
		return "iOS"
	case strings.Contains(platformID, "watch"):
		return "watchOS"
	case strings.Contains(platformID, "appletv"):
		return "tvOS"
	case strings.Contains(platformID, "xr"):
		return "visionOS"
	}
	return "Simulator"
}

// lastUsed returns the device's last boot: simctl lastUsedAt, else the
// device.plist lastUsedAt. never is true when the device was never booted.
func (ss *simScan) lastUsed(dv simDev) (t time.Time, never bool) {
	if dv.LastUsedAt != "" {
		if t, err := time.Parse(time.RFC3339, dv.LastUsedAt); err == nil {
			return t, false
		}
	}
	if info, err := readPlistDict(filepath.Join(dv.dir, "device.plist")); err == nil {
		if t := pTime(info, "lastUsedAt"); !t.IsZero() {
			return t, false
		}
	}
	return time.Time{}, true
}

// simApps lists the bundle ids installed in a simulator.
func simApps(dir string) []string {
	base := filepath.Join(dir, "data", "Containers", "Bundle", "Application")
	ents, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		c := filepath.Join(base, e.Name())
		id := ""
		if meta, err := readPlistDict(filepath.Join(c, ".com.apple.mobile_container_manager.metadata.plist")); err == nil {
			id = pString(meta, "MCMMetadataIdentifier")
		}
		if id == "" { // fall back to the .app name
			if apps, _ := filepath.Glob(filepath.Join(c, "*.app")); len(apps) > 0 {
				id = strings.TrimSuffix(filepath.Base(apps[0]), ".app")
			}
		}
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// isTestRunner reports XCUITest runner apps installed by automation tools.
func isTestRunner(bundle string) bool {
	return strings.HasSuffix(bundle, ".xctrunner")
}

// splitApps separates user apps from UI-test runners.
func splitApps(apps []string) (userApps, runners []string) {
	for _, a := range apps {
		if isTestRunner(a) {
			runners = append(runners, a)
		} else {
			userApps = append(userApps, a)
		}
	}
	return userApps, runners
}

// ---------------------------------------------------------------- devices

func (ss *simScan) deviceItem(dv simDev, recordings bool) {
	lu, never := ss.lastUsed(dv)
	apps := simApps(dv.dir)
	userApps, runners := splitApps(apps)
	rtName := ss.runtimeName(dv.runtime)
	typeName := ss.typeNames[dv.DeviceType]
	custom := typeName != "" && dv.Name != typeName

	it := ss.item("ios-simulator", core.CatSimulators, dv.UDID)
	it.Name = dv.Name + " · " + rtName
	it.Location = dv.dir
	it.Method = core.MethodCommand
	it.Command = []string{"xcrun", "simctl", "delete", dv.UDID}
	it.Covers = dv.dir // recordings, logs and caches items inside become redundant
	it.Recheck = shutdownRecheck(ss.env.Runner, []string{dv.UDID})
	it.LastUsed = lu
	it.Size = dv.DataPathSize + dv.LogPathSize // estimate until measured
	setMeta(it, "udid", dv.UDID)
	setMeta(it, "runtime", rtName)
	setMeta(it, "state", dv.State)
	setMeta(it, "device_type", typeName)
	if len(apps) > 0 {
		setMeta(it, "apps", strings.Join(apps, ", "))
	}
	if custom {
		setMeta(it, "custom_name", "true")
	}
	if never {
		setMeta(it, "never_booted", "true")
	}
	if ss.paired[strings.ToUpper(dv.UDID)] {
		setMeta(it, "paired", "true")
	}

	var note []string
	if len(userApps) > 0 {
		it.Risk = core.RiskCaution
		note = append(note, fmt.Sprintf("Simulator with %d installed app(s) and their data (%s): deleting it loses app data, logins and settings — reinstall the dev build afterwards. `xcrun simctl erase %s` wipes it but keeps the device.",
			len(userApps), shortList(userApps, 3), dv.UDID))
	} else {
		it.Risk = core.RiskModerate
		msg := "Simulator without installed apps"
		if len(runners) > 0 {
			msg += fmt.Sprintf(" (only %d UI-test runner(s))", len(runners))
		}
		note = append(note, msg+"; recreate one from Xcode (Devices & Simulators) or `xcrun simctl create`.")
	}
	if never {
		note = append(note, "Never booted (Xcode default device): deleting it frees little and removes it from Xcode's run destinations.")
	}
	if custom {
		note = append(note, "Custom name — probably created by a script or an AI agent for QA.")
	}
	if recordings {
		note = append(note, "Its size includes the XCTest recordings listed separately: clean those first.")
		setMeta(it, "has_recordings", "true")
	}
	it.Note = strings.Join(note, " ")

	switch dv.State {
	case "Shutdown":
		if it.Risk == core.RiskModerate && !never && it.Age(ss.env.Now) >= simStaleRecommend {
			it.Recommended = true
		}
	case "Booted":
		it.Selectable = false
		addWarn(it, "booted — shut it down before deleting it")
	default:
		it.Selectable = false
		addWarn(it, "simulator state: "+dv.State)
	}
	if !ss.applyPlace(it, dv.dir) {
		return
	}
	ss.sizeLater(it, []string{dv.dir}, nil)
}

func shortList(xs []string, n int) string {
	if len(xs) <= n {
		return strings.Join(xs, ", ")
	}
	return strings.Join(xs[:n], ", ") + fmt.Sprintf(" +%d", len(xs)-n)
}

// unavailableItem proposes one simulator simctl reports as unavailable.
//
// It deletes that device only (`simctl delete <UDID>`), never `simctl delete
// unavailable`: the latter removes whatever is unavailable when it runs,
// including devices that are merely unusable right now (another Xcode
// selected, runtime image not mounted after a macOS update) and come back
// once the setup is fixed. Such devices, and devices holding user apps, are
// caution and never preselected. The Recheck refuses when the device became
// available again (or changed) since the scan.
func (ss *simScan) unavailableItem(dv simDev, recordings bool) {
	lu, _ := ss.lastUsed(dv)
	apps := simApps(dv.dir)
	userApps, _ := splitApps(apps)
	rtName := ss.runtimeName(dv.runtime)

	// Kind kept from the former grouped item so `-k ios-simulators-unavailable` still works.
	it := ss.item("ios-simulators-unavailable", core.CatSimulators, dv.UDID)
	it.Name = dv.Name + " · " + rtName + " (unavailable)"
	it.Location = dv.dir
	it.Method = core.MethodCommand
	it.Command = []string{"xcrun", "simctl", "delete", dv.UDID}
	it.Covers = dv.dir // recordings inside become redundant
	it.Recheck = unavailableRecheck(ss.env.Runner, dv.UDID, dv.runtime)
	it.LastUsed = lu
	it.Size = dv.DataPathSize + dv.LogPathSize // estimate until measured
	setMeta(it, "udid", dv.UDID)
	setMeta(it, "runtime", rtName)
	setMeta(it, "reason", dv.AvailabilityError)
	if len(apps) > 0 {
		setMeta(it, "apps", strings.Join(apps, ", "))
	}

	note := []string{"Simulator simctl reports as unavailable: it cannot boot with the current Xcode and runtimes."}
	if why := ss.mayComeBack(dv); why != "" {
		it.Risk = core.RiskCaution
		it.NoRecommend = true
		setMeta(it, "may_come_back", "true")
		addWarn(it, why)
		note = append(note, "It may only be unusable right now (another Xcode selected with xcode-select, runtime image not mounted after a macOS update): fixing that brings it back with its apps and data.")
	} else {
		it.Risk = core.RiskSafe
		it.Recommended = len(userApps) == 0
		note = append(note, "Its runtime "+rtName+" is no longer installed.")
	}
	if len(userApps) > 0 {
		it.Risk = core.RiskCaution
		it.Recommended = false
		note = append(note, fmt.Sprintf("It holds %d installed app(s) and their data (%s): re-installing its runtime would bring them back, deleting it loses them.",
			len(userApps), shortList(userApps, 3)))
	}
	if recordings {
		note = append(note, "Its size includes the XCTest recordings listed separately.")
		setMeta(it, "has_recordings", "true")
	}
	note = append(note, "`xcrun simctl delete "+dv.UDID+"` removes this device only.")
	it.Note = strings.Join(note, " ")
	if dv.State != "" && dv.State != "Shutdown" {
		it.Selectable = false
		addWarn(it, "simulator state: "+dv.State)
	}
	if !ss.applyPlace(it, dv.dir) {
		return
	}
	ss.sizeLater(it, []string{dv.dir}, nil)
}

// mayComeBack explains why an unavailable simulator may become usable again
// without re-creating it, or returns "" when its runtime is provably gone:
// not listed by `simctl list runtimes`, no bundle, registered image or
// downloaded asset of it on disk (runtimeOnDisk), no disk image of it in
// `simctl runtime list` (which must have succeeded), and an availability
// error that does not hint at an unsupported or unmounted runtime.
func (ss *simScan) mayComeBack(dv simDev) string {
	name := ss.runtimeName(dv.runtime)
	for _, r := range ss.list.Runtimes {
		if r.Identifier != dv.runtime {
			continue
		}
		if r.IsAvailable {
			return "its runtime " + name + " is installed — the device type may just be unsupported by the selected Xcode"
		}
		return "its runtime " + name + " is still installed but unusable with the selected Xcode or macOS — switching Xcode may bring it back"
	}
	if b := ss.runtimeOnDisk(dv.runtime); b != "" {
		if app := appBundleOf(b); app != "" {
			return "its runtime " + name + " is bundled with " + filepath.Base(app) + " — selecting that Xcode (xcode-select) brings it back"
		}
		return "its runtime " + name + " is still on disk (" + b + ") — it may just be unusable with the selected Xcode"
	}
	if !ss.imgsKnown {
		return "cannot verify that its runtime " + name + " was removed (`simctl runtime list` failed)"
	}
	for _, k := range sortedKeys(ss.imgs) {
		if img := ss.imgs[k]; img.RuntimeIdentifier == dv.runtime {
			state := img.State
			if state == "" {
				state = "unknown state"
			}
			return "the " + name + " runtime image is still on disk (" + state + ") — it may just be unmounted"
		}
	}
	e := strings.ToLower(dv.AvailabilityError)
	for _, hint := range []string{"not supported", "unsupported", "mount"} {
		if strings.Contains(e, hint) {
			return "simctl: " + dv.AvailabilityError
		}
	}
	return ""
}

// runtimeOnDisk returns where runtime rid still exists on disk, wherever
// simctl may not list it:
//   - .simruntime bundles: inside an Xcode that is not the selected one
//     (older Xcodes shipped their iOS runtime), legacy runtimes in
//     /Library/Developer/CoreSimulator/Profiles/Runtimes, mounted images;
//   - disk images registered in CoreSimulator's image database
//     (/Library/Developer/CoreSimulator/Images/images.plist), mounted or not,
//     read directly in case `simctl runtime list` came back incomplete;
//   - downloaded runtime assets (/System/Library/AssetsV2/
//     com_apple_MobileAsset_*SimulatorRuntime/*.asset), in case the image
//     database lost them (e.g. after a macOS update).
func (ss *simScan) runtimeOnDisk(rid string) string {
	ss.bundlesOnce.Do(func() {
		ss.bundles = map[string]string{}
		const rel = "Library/Developer/CoreSimulator/Profiles/Runtimes/*.simruntime"
		pats := []string{
			ss.sys(filepath.Join("/", rel)),
			ss.sys(filepath.Join("/Library/Developer/CoreSimulator/Volumes/*", rel)),
		}
		for _, apps := range []string{ss.sys("/Applications/Xcode*.app"), filepath.Join(ss.env.Home, "Applications", "Xcode*.app")} {
			pats = append(pats, filepath.Join(apps, "Contents/Developer/Platforms/*.platform", rel))
		}
		for _, pat := range pats {
			matches, _ := filepath.Glob(pat)
			for _, b := range matches {
				info, err := readPlistDict(filepath.Join(b, "Contents", "Info.plist"))
				id := pString(info, "CFBundleIdentifier")
				if err != nil || id == "" {
					// Unreadable: key it by its display name ("iOS 16.4"),
					// matched below through prettyRuntime.
					id = strings.TrimSuffix(filepath.Base(b), ".simruntime")
				}
				if _, ok := ss.bundles[id]; !ok {
					ss.bundles[id] = b
				}
			}
		}
		// After the bundles, whose "bundled with Xcode-14.3.app" says more.
		ss.registeredImages()
		ss.runtimeAssets()
	})
	if b := ss.bundles[rid]; b != "" {
		return b
	}
	return ss.bundles[prettyRuntime(rid)]
}

// addRuntime records evidence that runtime key (identifier, or display name
// such as "iOS 16.4") is on disk at where. The first evidence found wins.
func (ss *simScan) addRuntime(key, where string) {
	if _, ok := ss.bundles[key]; !ok && key != "" {
		ss.bundles[key] = where
	}
}

// registeredImages adds the runtime disk images of CoreSimulator's image
// database whose file is still on disk (or cannot be proven gone).
func (ss *simScan) registeredImages() {
	db := ss.sys("/Library/Developer/CoreSimulator/Images/images.plist")
	info, err := readPlistDict(db)
	if err != nil {
		return
	}
	imgs, _ := info["images"].([]any)
	for _, x := range imgs {
		img, _ := x.(map[string]any)
		id := pString(pDict(img, "runtimeInfo"), "bundleIdentifier")
		if id == "" {
			continue
		}
		where := db
		if u, err := url.Parse(pString(pDict(img, "path"), "relative")); err == nil && u.Scheme == "file" && filepath.IsAbs(u.Path) {
			if _, err := os.Stat(u.Path); errors.Is(err, fs.ErrNotExist) {
				continue // stale entry: the image file is gone
			}
			where = u.Path
		}
		ss.addRuntime(id, where)
	}
}

// runtimeAssets adds the downloaded simulator runtime assets, keyed by
// display name ("iOS 26.5": the asset only knows platform and version).
func (ss *simScan) runtimeAssets() {
	colls, _ := filepath.Glob(ss.sys("/System/Library/AssetsV2/com_apple_MobileAsset_*SimulatorRuntime"))
	for _, coll := range colls {
		plat := assetPlatform(filepath.Base(coll))
		if plat == "" {
			continue
		}
		assets, _ := filepath.Glob(filepath.Join(coll, "*.asset"))
		for _, a := range assets {
			info, err := readPlistDict(filepath.Join(a, "Info.plist"))
			if err != nil {
				continue
			}
			if v := majorMinor(pString(pDict(info, "MobileAssetProperties"), "SimulatorVersion")); v != "" {
				ss.addRuntime(plat+" "+v, a)
			}
		}
	}
}

// assetPlatform maps a MobileAsset collection
// ("com_apple_MobileAsset_iOSSimulatorRuntime") to the platform name
// prettyRuntime uses ("iOS"), or "" when unknown.
func assetPlatform(coll string) string {
	p := strings.TrimSuffix(strings.TrimPrefix(coll, "com_apple_MobileAsset_"), "SimulatorRuntime")
	switch strings.ToLower(p) {
	case "ios":
		return "iOS"
	case "watchos":
		return "watchOS"
	case "tvos", "appletvos":
		return "tvOS"
	case "xros", "visionos":
		return "visionOS"
	}
	return ""
}

// majorMinor returns "17.0" for "17.0.1" or "17" (runtime identifiers only
// carry major and minor), or "" when v is not a version.
func majorMinor(v string) string {
	parts := strings.Split(strings.TrimSpace(v), ".")
	for _, p := range parts {
		if _, err := strconv.Atoi(p); err != nil {
			return ""
		}
	}
	if len(parts) == 1 {
		parts = append(parts, "0")
	}
	return parts[0] + "." + parts[1]
}

// ---------------------------------------------------------------- inside simulators

// attachments finds the screen recordings testmanagerd leaves in
// InternalDaemon/<uuid>/tmp/Attachments during XCUITest automation
// (agent-device, Maestro, argent, xcodebuild test). Nothing ever cleans them.
func (ss *simScan) attachments(dv simDev) (found bool) {
	base := filepath.Join(dv.dir, "data", "Containers", "Data", "InternalDaemon")
	ents, err := os.ReadDir(base)
	if err != nil {
		return false
	}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		container := filepath.Join(base, e.Name())
		att := filepath.Join(container, "tmp", "Attachments")
		files, err := os.ReadDir(att)
		if err != nil || len(files) == 0 {
			continue
		}
		meta, err := readPlistDict(filepath.Join(container, ".com.apple.mobile_container_manager.metadata.plist"))
		if err != nil || pString(meta, "MCMMetadataIdentifier") != "com.apple.testmanagerd" {
			continue
		}
		var paths []string
		partial := 0
		for _, f := range files {
			p := filepath.Join(att, f.Name())
			if ss.skipPath(p) {
				continue
			}
			paths = append(paths, p)
			if strings.Contains(f.Name(), ".sb-") {
				partial++
			}
		}
		if len(paths) == 0 {
			continue
		}
		it := ss.item("ios-simulator-attachments", core.CatSimulators, att)
		it.Name = "XCTest recordings · " + dv.Name
		setTargets(it, paths, att) // the Attachments folder itself stays
		it.Risk = core.RiskSafe
		it.Method = core.MethodDelete
		it.ProcessGuard = []string{"xcodebuild"}
		it.Note = fmt.Sprintf("%d screen recording(s) written by testmanagerd during UI-test automation (agent-device, Maestro, argent, xcodebuild test); nothing reads them after the run", len(paths))
		if partial > 0 {
			it.Note += fmt.Sprintf(", %d are partial *.sb-* files from aborted writes", partial)
		}
		it.Note += "."
		setMeta(it, "udid", dv.UDID)
		setMeta(it, "device", dv.Name)
		setMeta(it, "partial_files", fmt.Sprint(partial))
		it.Recheck = shutdownRecheck(ss.env.Runner, []string{dv.UDID})
		if dv.State == "Shutdown" || !dv.IsAvailable {
			it.Recommended = true
		} else {
			addWarn(it, "simulator is "+strings.ToLower(dv.State)+" — a UI test may still be recording")
		}
		if !ss.applyPlace(it, att) {
			continue
		}
		found = true
		ss.guardWarn(it)
		it.LastUsed = childrenNewest(att)
		ss.sizeLater(it, paths, func(it *core.Item, m measured) { it.LastUsed = maxTime(it.LastUsed, m.newest) })
	}
	return found
}

// unifiedLogDirs are the tracev3 stores of the simulated logd.
var unifiedLogDirs = []string{"Persist", "Special", "Signpost", "HighVolume"}

// deviceLogs groups the unified-log archives (tracev3) of shut-down simulators.
// Only the files are removed: the store layout and version.plist stay.
func (ss *simScan) deviceLogs(devs []simDev) {
	var paths, udids []string
	var size, files int64
	var newest time.Time
	n := 0
	var st unix.Stat_t
	for _, dv := range devs {
		diag := filepath.Join(dv.dir, "data", "var", "db", "diagnostics")
		got := false
		for _, sub := range unifiedLogDirs {
			dir := filepath.Join(diag, sub)
			ents, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, e := range ents {
				if !e.Type().IsRegular() {
					continue
				}
				p := filepath.Join(dir, e.Name())
				if unix.Lstat(p, &st) != nil || ss.skipPath(p) {
					continue
				}
				paths = append(paths, p)
				size += st.Blocks * 512
				files++
				if mt := time.Unix(st.Mtim.Unix()); mt.After(newest) {
					newest = mt
				}
				got = true
			}
		}
		if got {
			n++
			udids = append(udids, dv.UDID)
		}
	}
	if len(paths) == 0 {
		return
	}
	it := ss.item("ios-simulator-logs", core.CatSimulators, "unified-logs")
	it.Recheck = shutdownRecheck(ss.env.Runner, udids)
	it.Name = fmt.Sprintf("Simulator unified logs (%d simulators)", n)
	setTargets(it, paths, filepath.Join(ss.root, "*", "data", "var", "db", "diagnostics"))
	it.Risk = core.RiskSafe
	it.Method = core.MethodDelete
	it.Size, it.Files = size, files
	it.LastUsed = newest
	it.Note = "os_log archives (tracev3) of shut-down simulators; logging restarts at the next boot, only past in-simulator logs are lost."
	if !ss.applyPlace(it, paths[0]) {
		return
	}
	ss.emit(it)
}

// simCacheDirs are the big, purely regenerable caches of the simulated OS.
var simCacheDirs = []string{
	"com.apple.mediaanalysisd",
	"com.apple.coresymbolicationd",
	"GeoServices",
	"com.apple.geod",
	"com.apple.parsecd",
}

// deviceCaches groups system caches of simulators unused for 2+ weeks.
func (ss *simScan) deviceCaches(devs []simDev) {
	var paths, udids []string
	var last time.Time
	n := 0
	for _, dv := range devs {
		lu, never := ss.lastUsed(dv)
		if never || ss.env.Now.Sub(lu) < simCacheMinAge {
			continue
		}
		got := false
		for _, name := range simCacheDirs {
			p := filepath.Join(dv.dir, "data", "Library", "Caches", name)
			if fsx.IsDir(p) && !ss.skipPath(p) {
				paths = append(paths, p)
				got = true
			}
		}
		if got {
			n++
			last = maxTime(last, lu)
			udids = append(udids, dv.UDID)
		}
	}
	if len(paths) == 0 {
		return
	}
	it := ss.item("ios-simulator-caches", core.CatSimulators, "system-caches")
	it.Recheck = shutdownRecheck(ss.env.Runner, udids)
	it.Name = fmt.Sprintf("Simulator system caches (%d simulators)", n)
	setTargets(it, paths, filepath.Join(ss.root, "*", "data", "Library", "Caches"))
	it.Risk = core.RiskModerate
	it.Method = core.MethodDelete
	it.LastUsed = last
	it.Note = "Media-analysis, symbolication, maps and suggestions caches of simulators unused for 2+ weeks; the simulated daemons rebuild them after the next boot (slower first boot)."
	if !ss.applyPlace(it, paths[0]) {
		return
	}
	ss.sizeLater(it, paths, nil)
}

// ---------------------------------------------------------------- orphans

// orphans reports device folders simctl does not know about (no or broken
// device.plist): unreachable, and `simctl delete unavailable` misses them.
func (ss *simScan) orphans(known map[string]bool) {
	ents, err := os.ReadDir(ss.root)
	if err != nil {
		return
	}
	for _, e := range ents {
		name := e.Name()
		if !e.IsDir() || !uuidRe.MatchString(name) || known[strings.ToUpper(name)] {
			continue
		}
		p := filepath.Join(ss.root, name)
		if ss.skipPath(p) {
			continue
		}
		top := newestMTime(p, filepath.Join(p, "data"), filepath.Join(p, "device.plist"))
		if ss.env.Now.Sub(top) < orphanMinAge {
			continue // possibly being created right now
		}
		it := ss.item("ios-simulator-orphan", core.CatSimulators, p)
		it.Path = p
		it.Name = "Orphan simulator " + name[:8]
		why := "missing or invalid device.plist"
		if info, err := readPlistDict(filepath.Join(p, "device.plist")); err == nil {
			rt := pString(info, "runtime")
			switch {
			case pBool(info, "isDeleted"):
				why = "marked deleted"
			case rt != "" && ss.rtNames[rt] != "":
				// A well-formed device of an installed runtime that simctl did
				// not list: its answer may be partial. Never call it an orphan.
				continue
			case rt != "":
				why = "its runtime " + prettyRuntime(rt) + " is not installed"
			}
			if n := pString(info, "name"); n != "" {
				it.Name = "Orphan simulator · " + n
				if rt != "" {
					it.Name += " · " + prettyRuntime(rt)
				}
			}
		} else {
			setMeta(it, "device_plist", "missing")
		}
		it.Risk = core.RiskModerate
		it.Method = core.MethodDelete
		it.LastUsed = top
		setMeta(it, "udid", name)
		setMeta(it, "reason", why)
		it.Note = "Simulator folder unknown to simctl (" + why + "): it cannot be booted or listed, and `simctl delete unavailable` does not remove it."
		if apps := simApps(p); len(apps) > 0 {
			setMeta(it, "apps", strings.Join(apps, ", "))
			it.Note += " Leftover app data: " + shortList(apps, 3) + "."
		}
		if !ss.applyPlace(it, p) {
			continue
		}
		ss.sizeLater(it, []string{p}, func(it *core.Item, m measured) {
			it.LastUsed = maxTime(it.LastUsed, m.newest)
			it.Recommended = it.Age(ss.env.Now) >= orphanRecommend
		})
	}
}

// ---------------------------------------------------------------- runtimes

func (ss *simScan) runtimes(imgs map[string]runtimeImage) {
	devCount, booted := map[string]int{}, map[string]int{}
	for rt, devs := range ss.list.Devices {
		devCount[rt] += len(devs)
		for _, d := range devs {
			if d.State != "" && d.State != "Shutdown" {
				booted[rt]++
			}
		}
	}
	served := map[string]int{}
	byPlat := map[string][]string{} // platform -> image keys
	for _, k := range sortedKeys(imgs) {
		img := imgs[k]
		served[img.RuntimeIdentifier]++
		plat := runtimePlatform(img.RuntimeIdentifier, img.PlatformIdentifier)
		byPlat[plat] = append(byPlat[plat], k)
	}
	// Per platform, newest first (ties: key order). The newest image is
	// "needed to run <platform> simulators"; the images of the keep_latest
	// newest runtimes (config keep_latest, at least 1; several images of the
	// same runtime count once) are never preselected.
	keep := max(1, ss.env.KeepLatest)
	newest := map[string]string{} // platform -> image key
	kept := map[string]bool{}     // image key -> among the keep newest runtimes
	for plat, keys := range byPlat {
		sort.SliceStable(keys, func(i, j int) bool { return newerImage(imgs[keys[i]], imgs[keys[j]]) })
		newest[plat] = keys[0]
		rank := map[string]int{} // runtime -> rank among the platform's runtimes
		for _, k := range keys {
			id := runtimeRankKey(k, imgs[k])
			if _, ok := rank[id]; !ok {
				rank[id] = len(rank)
			}
			kept[k] = rank[id] < keep
		}
	}
	for _, k := range sortedKeys(imgs) {
		img := imgs[k]
		plat := runtimePlatform(img.RuntimeIdentifier, img.PlatformIdentifier)
		ss.runtimeItem(k, img, plat, devCount[img.RuntimeIdentifier], booted[img.RuntimeIdentifier],
			served[img.RuntimeIdentifier], newest[plat] == k, kept[k], keep)
	}
}

// newerImage reports whether runtime image a is newer than b (version, then build).
func newerImage(a, b runtimeImage) bool {
	c := compareVersions(a.Version, b.Version)
	return c > 0 || (c == 0 && a.Build > b.Build)
}

// runtimeRankKey identifies the runtime an image provides, so that several
// images of one runtime count once for keep_latest.
func runtimeRankKey(key string, img runtimeImage) string {
	switch {
	case img.RuntimeIdentifier != "":
		return img.RuntimeIdentifier
	case img.Version != "":
		return "version:" + img.Version
	}
	return "image:" + key
}

// runtimeItem emits one runtime image. newest: the platform's newest image;
// kept: an image of one of the platform's keep newest runtimes (keep_latest).
func (ss *simScan) runtimeItem(key string, img runtimeImage, plat string, devices, booted, served int, newest, kept bool, keep int) {
	id := img.Identifier
	if id == "" {
		id = key
	}
	it := ss.item("ios-simulator-runtime", core.CatSimulators, id)
	it.Name = plat + " " + img.Version + " runtime"
	if img.Build != "" {
		it.Name += " (" + img.Build + ")"
	}
	it.Location = firstNonEmpty(img.Path, img.RuntimeBundlePath, img.MountPath)
	if i := strings.Index(it.Location, ".asset/"); i > 0 { // show the asset, not the dmg deep inside
		it.Location = it.Location[:i+len(".asset")]
	}
	it.Method = core.MethodCommand
	it.Command = []string{"xcrun", "simctl", "runtime", "delete", id}
	it.Risk = core.RiskModerate
	it.Size = img.SizeBytes // images live in system storage: trust simctl, never walk the mounts
	it.ProcessGuard = []string{"Xcode"}
	if t, err := time.Parse(time.RFC3339, img.LastUsedAt); err == nil {
		it.LastUsed = t
	}
	setMeta(it, "identifier", id)
	setMeta(it, "runtime", img.RuntimeIdentifier)
	setMeta(it, "build", img.Build)
	setMeta(it, "kind", img.Kind)
	setMeta(it, "state", img.State)
	setMeta(it, "devices", fmt.Sprint(devices))
	it.Note = fmt.Sprintf("%s simulator runtime disk image; re-download it (~8 GB) from Xcode › Settings › Components or `xcodebuild -downloadPlatform %s`.", plat, plat)
	switch {
	case !img.Deletable:
		it.Method = core.MethodReport
		it.Command = nil
		it.Selectable = false
		it.Note = plat + " simulator runtime bundled with Xcode (" + img.Kind + "): it goes away with that Xcode."
	case devices > 0:
		w := fmt.Sprintf("used by %d simulator(s) — deleting it makes them unavailable", devices)
		if booted > 0 {
			w += fmt.Sprintf(" (%d running: simctl shuts them down)", booted)
		}
		addWarn(it, w)
	case newest:
		addWarn(it, "newest "+plat+" runtime — needed to run "+plat+" simulators")
	case kept:
		setMeta(it, "kept", fmt.Sprintf("one of the %d newest installed %s runtimes (keep_latest)", keep, plat))
		it.Note = fmt.Sprintf("One of the %d newest %s runtimes, kept out of smart selection (config keep_latest). ", keep, plat) + it.Note
	case img.State == "Ready" && served == 1:
		it.Recommended = true // unused, and a newer runtime of the same platform exists
		it.Note = "No simulator uses this runtime and a newer " + plat + " runtime is installed. " + it.Note
	}
	if kept {
		it.NoRecommend = true // never preselected, whatever its age (keep_latest)
		it.Recommended = false
	}
	if img.State != "" && img.State != "Ready" {
		it.Note += " State: " + img.State + "."
	}
	if img.Path != "" && ss.place(img.Path) == placeExternal {
		toReport(it, "on external volume — no internal gain")
	}
	ss.guardWarn(it)
	ss.emit(it)
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

// ---------------------------------------------------------------- dyld caches

// dyldCaches reports simulator dyld shared caches built for another macOS
// build or for a runtime that is no longer installed. They are root-owned
// (/Library/Developer/CoreSimulator/Caches/dyld/<macOS build>/<runtime>.<build>):
// report only.
func (ss *simScan) dyldCaches(imgs map[string]runtimeImage, haveImgs bool) {
	root := ss.sys("/Library/Developer/CoreSimulator/Caches/dyld")
	ents, err := os.ReadDir(root)
	if err != nil {
		return
	}
	info, _ := readPlistDict(ss.sys("/System/Library/CoreServices/SystemVersion.plist"))
	cur := pString(info, "ProductBuildVersion")
	if cur == "" {
		return
	}
	installed := map[string]bool{}
	for _, r := range ss.list.Runtimes {
		installed[r.Identifier+"."+r.Build] = true
	}
	for _, img := range imgs {
		installed[img.RuntimeIdentifier+"."+img.Build] = true
	}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		p := filepath.Join(root, e.Name())
		if e.Name() != cur {
			ss.dyldItem(p, fmt.Sprintf("Simulator dyld cache · macOS %s (stale)", e.Name()),
				fmt.Sprintf("Simulator dyld shared caches built for macOS %s (this Mac runs %s): never used again, CoreSimulator rebuilds them for the current build.", e.Name(), cur))
			continue
		}
		if !haveImgs {
			continue
		}
		subs, err := os.ReadDir(p)
		if err != nil {
			continue
		}
		for _, sub := range subs {
			if !sub.IsDir() || sub.Name() == "inc" || installed[sub.Name()] {
				continue
			}
			ss.dyldItem(filepath.Join(p, sub.Name()), "Simulator dyld cache · "+prettyDyld(sub.Name())+" (runtime removed)",
				"Simulator dyld shared cache of a runtime that is no longer installed: never used again.")
		}
	}
}

// prettyDyld turns "com.apple.CoreSimulator.SimRuntime.iOS-26-4.23E254a" into "iOS 26.4 23E254a".
func prettyDyld(name string) string {
	i := strings.LastIndexByte(name, '.')
	if i < 0 {
		return name
	}
	return prettyRuntime(name[:i]) + " " + name[i+1:]
}

func (ss *simScan) dyldItem(p, name, note string) {
	it := ss.item("ios-simulator-dyld-cache", core.CatSimulators, p)
	it.Path = p
	it.Name = name
	it.Risk = core.RiskSafe
	it.Method = core.MethodReport
	it.Selectable = false
	it.LastUsed = fsx.ModTime(p)
	it.Note = note + " Root-owned: remove it with `sudo rm -rf '" + p + "'`."
	addWarn(it, "root-owned — lu-cleaner cannot remove it")
	if !ss.applyPlace(it, p) {
		return
	}
	ss.sizeLater(it, []string{p}, nil)
}
