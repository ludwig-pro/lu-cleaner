package apple

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
)

const (
	rtIOS182 = "com.apple.CoreSimulator.SimRuntime.iOS-18-2"
	rtIOS164 = "com.apple.CoreSimulator.SimRuntime.iOS-16-4"
	udidA    = "AAAAAAAA-0000-4000-8000-000000000001" // iPhone 16, unused for 90 days, no app
	udidB    = "BBBBBBBB-0000-4000-8000-000000000002" // custom-named QA sim with an app + UI-test recordings
	udidC    = "CCCCCCCC-0000-4000-8000-000000000003" // booted
	udidD    = "DDDDDDDD-0000-4000-8000-000000000004" // never booted
	udidE    = "EEEEEEEE-0000-4000-8000-000000000005" // unavailable (runtime removed)
	udidG    = "99999999-0000-4000-8000-000000000006" // last boot only in device.plist (70 days)
	udidO1   = "0F0F0F0F-0000-4000-8000-000000000007" // orphan folder, old
	udidO2   = "0E0E0E0E-0000-4000-8000-000000000008" // orphan-looking folder created right now
	udidO3   = "0D0D0D0D-0000-4000-8000-000000000009" // valid device.plist, installed runtime, not listed: not an orphan
	udidO4   = "0C0C0C0C-0000-4000-8000-00000000000A" // device.plist of a runtime that is gone: orphan
)

const devicesRel = "Library/Developer/CoreSimulator/Devices"

type simFixture struct {
	*fixture
	root string
}

func newSimFixture(t *testing.T) *simFixture {
	f := &simFixture{fixture: newFixture(t, "xcrun"), root: ""}
	f.root = f.p(devicesRel)
	return f
}

func (f *simFixture) dev(udid string) string { return filepath.Join(f.root, udid) }

// device returns the simctl JSON entry of a device living in the fixture.
func (f *simFixture) device(udid, name, typ, state string, available bool, lastUsed time.Time) simDevice {
	d := simDevice{
		UDID: udid, Name: name, State: state, IsAvailable: available,
		DataPath:     filepath.Join(f.dev(udid), "data"),
		DataPathSize: 1 << 20,
		LogPath:      f.p("Library/Logs/CoreSimulator/" + udid),
		DeviceType:   "com.apple.CoreSimulator.SimDeviceType." + typ,
	}
	if !lastUsed.IsZero() {
		d.LastUsedAt = lastUsed.UTC().Format(time.RFC3339)
	}
	if !available {
		d.AvailabilityError = "runtime profile not found using \"System\" match policy"
	}
	f.dir(filepath.Join(d.DataPath, "Library"), ago(day))
	return d
}

func (f *simFixture) container(udid, kind, uuid, bundleID string) string {
	c := filepath.Join(f.dev(udid), "data", "Containers", kind, uuid)
	f.plist(filepath.Join(c, ".com.apple.mobile_container_manager.metadata.plist"),
		`<key>MCMMetadataIdentifier</key><string>`+bundleID+`</string>`)
	return c
}

func (f *simFixture) build() {
	t := f.t
	list := simList{
		DeviceTypes: []simDeviceType{
			{Identifier: "com.apple.CoreSimulator.SimDeviceType.iPhone-16", Name: "iPhone 16"},
			{Identifier: "com.apple.CoreSimulator.SimDeviceType.iPhone-16-Pro", Name: "iPhone 16 Pro"},
			{Identifier: "com.apple.CoreSimulator.SimDeviceType.iPad-A16", Name: "iPad (A16)"},
			{Identifier: "com.apple.CoreSimulator.SimDeviceType.iPhone-SE-3rd-generation", Name: "iPhone SE (3rd generation)"},
		},
		Runtimes: []simRuntime{{Identifier: rtIOS182, Name: "iOS 18.2", Version: "18.2", Build: "22C150", Platform: "iOS", IsAvailable: true}},
		Devices: map[string][]simDevice{
			rtIOS182: {
				f.device(udidA, "iPhone 16", "iPhone-16", "Shutdown", true, ago(90*day)),
				f.device(udidB, "Checkout QA", "iPhone-16", "Shutdown", true, ago(5*day)),
				f.device(udidC, "iPhone 16 Pro", "iPhone-16-Pro", "Booted", true, ago(time.Hour)),
				f.device(udidD, "iPad (A16)", "iPad-A16", "Shutdown", true, time.Time{}),
				f.device(udidG, "iPhone SE (3rd generation)", "iPhone-SE-3rd-generation", "Shutdown", true, time.Time{}),
			},
			rtIOS164: {f.device(udidE, "iPhone 14", "iPhone-14", "Shutdown", false, ago(300*day))},
		},
	}
	// Device plists: D never booted, G only knows its last boot here.
	f.plist(filepath.Join(f.dev(udidD), "device.plist"), `<key>UDID</key><string>`+udidD+`</string><key>state</key><integer>1</integer>`)
	f.plist(filepath.Join(f.dev(udidG), "device.plist"), `<key>lastUsedAt</key><date>`+ago(70*day).UTC().Format(time.RFC3339)+`</date>`)

	// B: an app, a UI-test runner and 2 recordings (one partial); another daemon's Attachments is ignored.
	f.container(udidB, "Bundle/Application", "11111111-0000-4000-8000-000000000001", "com.example.shop")
	f.container(udidB, "Bundle/Application", "11111111-0000-4000-8000-000000000002", "com.example.shopUITests.xctrunner")
	tm := f.container(udidB, "Data/InternalDaemon", "22222222-0000-4000-8000-000000000001", "com.apple.testmanagerd")
	f.file(filepath.Join(tm, "tmp/Attachments/6A0C7F31-9A3E-4E43-9D59-2E0A1F3A0B11"), 2<<20, ago(20*day))
	f.file(filepath.Join(tm, "tmp/Attachments/6A0C7F31-9A3E-4E43-9D59-2E0A1F3A0B12.sb-1c2d3e4f-AbCdEf"), 1<<20, ago(20*day))
	other := f.container(udidB, "Data/InternalDaemon", "22222222-0000-4000-8000-000000000002", "com.apple.gamed")
	f.file(filepath.Join(other, "tmp/Attachments/keep-me"), 1000, ago(20*day))
	// C (booted) also has a recording.
	tmC := f.container(udidC, "Data/InternalDaemon", "22222222-0000-4000-8000-000000000003", "com.apple.testmanagerd")
	f.file(filepath.Join(tmC, "tmp/Attachments/7A0C7F31-9A3E-4E43-9D59-2E0A1F3A0B11"), 1<<20, ago(time.Hour))

	// Unified logs of A (shut down) and C (booted: kept).
	f.file(filepath.Join(f.dev(udidA), "data/var/db/diagnostics/Persist/0000000000000001.tracev3"), 1<<20, ago(90*day))
	f.file(filepath.Join(f.dev(udidA), "data/var/db/diagnostics/Special/0000000000000002.tracev3"), 1<<19, ago(90*day))
	f.plist(filepath.Join(f.dev(udidA), "data/var/db/diagnostics/version.plist"), "")
	f.file(filepath.Join(f.dev(udidC), "data/var/db/diagnostics/Persist/0000000000000001.tracev3"), 1<<20, ago(time.Hour))
	// System caches of A (90 days) and B (5 days: kept).
	f.file(filepath.Join(f.dev(udidA), "data/Library/Caches/com.apple.mediaanalysisd/db"), 1<<20, ago(90*day))
	f.file(filepath.Join(f.dev(udidA), "data/Library/Caches/com.apple.containermanagerd/x"), 1<<20, ago(90*day))
	f.file(filepath.Join(f.dev(udidB), "data/Library/Caches/com.apple.mediaanalysisd/db"), 1<<20, ago(5*day))

	f.file(filepath.Join(f.dev(udidE), "data/Library/x"), 1<<20, ago(300*day))

	// Orphans and noise.
	f.file(filepath.Join(f.dev(udidO1), "data/Containers/Bundle/Application/33333333-0000-4000-8000-000000000001/Legacy.app/Legacy"), 1<<20, ago(400*day))
	ageTree(t, f.dev(udidO1), ago(400*day))
	f.file(filepath.Join(f.dev(udidO2), "data/x"), 1000, time.Now())
	f.plist(filepath.Join(f.dev(udidO3), "device.plist"), `<key>name</key><string>iPhone 16</string><key>runtime</key><string>`+rtIOS182+`</string>`)
	f.file(filepath.Join(f.dev(udidO3), "data/x"), 1<<20, ago(400*day))
	ageTree(t, f.dev(udidO3), ago(400*day))
	f.plist(filepath.Join(f.dev(udidO4), "device.plist"), `<key>name</key><string>iPhone 8</string><key>runtime</key><string>com.apple.CoreSimulator.SimRuntime.iOS-15-0</string>`)
	f.file(filepath.Join(f.dev(udidO4), "data/x"), 1<<20, ago(400*day))
	ageTree(t, f.dev(udidO4), ago(400*day))
	f.file(filepath.Join(f.root, "notadevice/x"), 1000, ago(400*day))
	f.plist(filepath.Join(f.root, "device_set.plist"), "")

	b, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	f.runner.out["xcrun simctl list -j"] = string(b)

	img := func(uuid, rid, version, build, kind, state, lastUsed string, deletable bool, size int64) string {
		m := map[string]any{
			"identifier": uuid, "runtimeIdentifier": rid, "version": version, "build": build, "kind": kind,
			"state": state, "deletable": deletable, "sizeBytes": size, "lastUsedAt": lastUsed,
			"path":               filepath.Join(f.sys, "System/Library/AssetsV2/com_apple_MobileAsset_iOSSimulatorRuntime", uuid+".asset/AssetData/Restore/x.dmg"),
			"platformIdentifier": "com.apple.platform.iphonesimulator",
		}
		b, _ := json.Marshal(m)
		return `"` + uuid + `":` + string(b)
	}
	f.runner.out["xcrun simctl runtime list -j"] = "{" + strings.Join([]string{
		img("5EB08DF5-AB86-44F4-9DC7-D09DAF3F9351", rtIOS182, "18.2", "22C150", "Disk Image", "Ready", now.Format(time.RFC3339), true, 8_000_000_000),
		img("5EB08DF5-AB86-44F4-9DC7-D09DAF3F9352", "com.apple.CoreSimulator.SimRuntime.iOS-17-5", "17.5", "21F79", "Disk Image", "Ready", ago(100*day).Format(time.RFC3339), true, 7_000_000_000),
		img("5EB08DF5-AB86-44F4-9DC7-D09DAF3F9353", "com.apple.CoreSimulator.SimRuntime.iOS-18-4", "18.4", "22E238", "Disk Image", "Ready", ago(100*day).Format(time.RFC3339), true, 8_500_000_000),
		img("5EB08DF5-AB86-44F4-9DC7-D09DAF3F9354", "com.apple.CoreSimulator.SimRuntime.watchOS-11-0", "11.0", "22R349", "Bundled with Xcode", "Ready", "", false, 4_000_000_000),
	}, ",") + "}"

	// dyld caches: one for an older macOS build, one for a removed runtime.
	f.plist(filepath.Join(f.sys, "System/Library/CoreServices/SystemVersion.plist"), `<key>ProductBuildVersion</key><string>25F80</string>`)
	dyld := filepath.Join(f.sys, "Library/Developer/CoreSimulator/Caches/dyld")
	f.file(filepath.Join(dyld, "24G90", rtIOS182+".22C150/dyld_sim_shared_cache_arm64e"), 1<<20, ago(200*day))
	f.file(filepath.Join(dyld, "25F80", rtIOS182+".22C150/dyld_sim_shared_cache_arm64e"), 1<<20, ago(day))
	f.file(filepath.Join(dyld, "25F80", "com.apple.CoreSimulator.SimRuntime.iOS-17-0.21A328/dyld_sim_shared_cache_arm64e"), 1<<20, ago(200*day))
	f.file(filepath.Join(dyld, "25F80", "inc/com.apple.CoreSimulator.SimRuntime.iOS-17-0.21A328/log"), 1000, ago(200*day))
}

func TestSimulatorDevices(t *testing.T) {
	f := newSimFixture(t)
	f.build()
	r := f.scan()

	sims := r.byKind("ios-simulator")
	if len(sims) != 5 {
		t.Fatalf("want 5 simulators (unavailable one grouped), got %v", names(sims))
	}
	get := func(udid string) *core.Item {
		t.Helper()
		it := r.final["apple:ios-simulator:"+udid]
		if it == nil {
			t.Fatalf("missing simulator %s", udid)
		}
		return it
	}
	a := get(udidA)
	if a.Name != "iPhone 16 · iOS 18.2" || a.Risk != core.RiskModerate || !a.Recommended || !a.CanClean() ||
		!reflect.DeepEqual(a.Command, []string{"xcrun", "simctl", "delete", udidA}) || a.Location != f.dev(udidA) {
		t.Errorf("A: %+v", a)
	}
	if !a.LastUsed.Equal(ago(90*day)) || a.Size < 3<<20 {
		t.Errorf("A: last used %v size %d", a.LastUsed, a.Size)
	}

	b := get(udidB)
	if b.Risk != core.RiskCaution || b.Recommended || b.Meta["custom_name"] != "true" || b.Meta["has_recordings"] != "true" ||
		b.Meta["apps"] != "com.example.shop, com.example.shopUITests.xctrunner" {
		t.Errorf("B: %+v", b)
	}
	if !strings.Contains(b.Note, "com.example.shop") || strings.Contains(b.Note, "(com.example.shopUITests") {
		t.Errorf("B note should list user apps only: %q", b.Note)
	}

	c := get(udidC)
	if c.CanClean() || !strings.Contains(c.Warn, "booted") {
		t.Errorf("booted C must not be selectable: %+v", c)
	}

	d := get(udidD)
	if !d.LastUsed.IsZero() || d.Meta["never_booted"] != "true" || d.Recommended || core.Recommend(d, now, 14*day) {
		t.Errorf("never-booted D: %+v", d)
	}

	g := get(udidG)
	if !g.LastUsed.Equal(ago(70*day)) || !g.Recommended {
		t.Errorf("G: last boot from device.plist: %+v", g)
	}

	u := r.one(t, "ios-simulators-unavailable")
	if u.Name != "Unavailable simulators (1)" || u.Risk != core.RiskSafe || !u.Recommended ||
		!reflect.DeepEqual(u.Command, []string{"xcrun", "simctl", "delete", "unavailable"}) ||
		!strings.Contains(u.Meta["devices"], "iPhone 14 (iOS 16.4)") || u.Size == 0 {
		t.Errorf("unavailable group: %+v", u)
	}
	if _, ok := r.final["apple:ios-simulator:"+udidE]; ok {
		t.Error("unavailable devices must not also get their own item")
	}
}

func TestSimulatorRecordingsLogsCaches(t *testing.T) {
	f := newSimFixture(t)
	f.build()
	f.alive = []string{"Simulator"}
	r := f.scan()

	recs := r.byKind("ios-simulator-attachments")
	if len(recs) != 2 {
		t.Fatalf("want recordings of B and C, got %v", names(recs))
	}
	var b, c *core.Item
	for _, it := range recs {
		switch it.Meta["udid"] {
		case udidB:
			b = it
		case udidC:
			c = it
		}
	}
	if b == nil || c == nil {
		t.Fatal("recording items not attributed to devices")
	}
	if b.Risk != core.RiskSafe || !b.Recommended || len(b.Paths) != 2 || b.Meta["partial_files"] != "1" ||
		!reflect.DeepEqual(b.ProcessGuard, []string{"xcodebuild"}) || b.Size < 3<<20 || !core.Recommend(b, now, 14*day) {
		t.Errorf("B recordings: %+v", b)
	}
	for _, p := range b.Targets() {
		if strings.Contains(p, "keep-me") || filepath.Base(p) == "Attachments" {
			t.Errorf("only testmanagerd recordings may be targeted, not %s", p)
		}
	}
	if c.Recommended || !strings.Contains(c.Warn, "booted") || core.Recommend(c, now, 14*day) {
		t.Errorf("booted C recordings must not be preselected: %+v", c)
	}

	logs := r.one(t, "ios-simulator-logs")
	if logs.Name != "Simulator unified logs (1 simulators)" || len(logs.Paths) != 2 || logs.Size < 3<<19 {
		t.Errorf("logs: %+v", logs)
	}
	for _, p := range logs.Targets() {
		if !strings.HasPrefix(p, f.dev(udidA)) || !strings.HasSuffix(p, ".tracev3") {
			t.Errorf("logs must only list tracev3 files of shut-down simulators: %s", p)
		}
	}

	caches := r.one(t, "ios-simulator-caches")
	if caches.Risk != core.RiskModerate || caches.Path != filepath.Join(f.dev(udidA), "data/Library/Caches/com.apple.mediaanalysisd") ||
		!caches.LastUsed.Equal(ago(90*day)) || !core.Recommend(caches, now, 14*day) {
		t.Errorf("caches (A only, whitelisted dirs only): %+v", caches)
	}
}

func TestSimulatorOrphans(t *testing.T) {
	f := newSimFixture(t)
	f.build()
	r := f.scan()
	if n := len(r.byKind("ios-simulator-orphan")); n != 2 {
		t.Fatalf("want 2 orphans (O1, O4), got %v", names(r.byKind("ios-simulator-orphan")))
	}
	o := r.final["apple:ios-simulator-orphan:"+f.dev(udidO1)]
	if o == nil || o.Path != f.dev(udidO1) || o.Method != core.MethodDelete || o.Risk != core.RiskModerate || !o.Recommended ||
		o.Meta["apps"] != "Legacy" || o.Meta["device_plist"] != "missing" {
		t.Errorf("orphan: %+v", o)
	}
	o4 := r.final["apple:ios-simulator-orphan:"+f.dev(udidO4)]
	if o4 == nil || o4.Name != "Orphan simulator · iPhone 8 · iOS 15.0" || !strings.Contains(o4.Note, "iOS 15.0 is not installed") {
		t.Errorf("orphan of a removed runtime: %+v", o4)
	}
	if _, ok := r.final["apple:ios-simulator-orphan:"+f.dev(udidO3)]; ok {
		t.Error("a valid device of an installed runtime must never be called an orphan")
	}

	// Without evidence that simctl describes this folder, nothing is an orphan.
	f2 := newSimFixture(t)
	f2.file(filepath.Join(f2.dev(udidO1), "data/x"), 1<<20, ago(400*day))
	ageTree(t, f2.dev(udidO1), ago(400*day))
	f2.runner.out["xcrun simctl list -j"] = `{"devicetypes":[],"runtimes":[],"devices":{},"pairs":{}}`
	if items := f2.scan().byKind("ios-simulator-orphan"); len(items) != 0 {
		t.Errorf("empty simctl answer must not turn folders into orphans: %v", names(items))
	}
}

func TestSimulatorRuntimes(t *testing.T) {
	f := newSimFixture(t)
	f.build()
	r := f.scan()
	rts := r.byKind("ios-simulator-runtime")
	if len(rts) != 4 {
		t.Fatalf("want 4 runtimes, got %v", names(rts))
	}
	get := func(id string) *core.Item { return r.final["apple:ios-simulator-runtime:"+id] }
	used := get("5EB08DF5-AB86-44F4-9DC7-D09DAF3F9351")
	if used.Recommended || !strings.Contains(used.Warn, "used by 5 simulator(s)") || !strings.Contains(used.Warn, "1 running") ||
		used.Size != 8_000_000_000 || used.Name != "iOS 18.2 runtime (22C150)" ||
		!reflect.DeepEqual(used.Command, []string{"xcrun", "simctl", "runtime", "delete", "5EB08DF5-AB86-44F4-9DC7-D09DAF3F9351"}) {
		t.Errorf("used runtime: %+v", used)
	}
	if !strings.HasSuffix(used.Location, ".asset") {
		t.Errorf("location should show the asset: %s", used.Location)
	}
	old := get("5EB08DF5-AB86-44F4-9DC7-D09DAF3F9352")
	if !old.Recommended || old.Warn != "" || old.Risk != core.RiskModerate || !core.Recommend(old, now, 14*day) {
		t.Errorf("unused older runtime should be recommended: %+v", old)
	}
	newest := get("5EB08DF5-AB86-44F4-9DC7-D09DAF3F9353")
	if newest.Recommended || !strings.Contains(newest.Warn, "newest iOS runtime") || core.Recommend(newest, now, 14*day) {
		t.Errorf("newest runtime must be kept: %+v", newest)
	}
	bundled := get("5EB08DF5-AB86-44F4-9DC7-D09DAF3F9354")
	if bundled.Method != core.MethodReport || bundled.CanClean() || bundled.Name != "watchOS 11.0 runtime (22R349)" {
		t.Errorf("non-deletable runtime must be a report: %+v", bundled)
	}
}

func TestSimulatorDyldCaches(t *testing.T) {
	f := newSimFixture(t)
	f.build()
	r := f.scan()
	items := r.byKind("ios-simulator-dyld-cache")
	if len(items) != 2 {
		t.Fatalf("want 2 stale dyld caches, got %v", names(items))
	}
	for _, it := range items {
		if it.Method != core.MethodReport || it.CanClean() || !strings.Contains(it.Note, "sudo rm -rf") {
			t.Errorf("dyld caches are root-owned reports: %+v", it)
		}
		if strings.Contains(it.Path, "/inc") || strings.Contains(it.Path, "22C150") && strings.Contains(it.Path, "25F80") {
			t.Errorf("current caches must not be reported: %s", it.Path)
		}
	}
}

func TestSimulatorsWithoutXcode(t *testing.T) {
	// No xcrun at all.
	f := newSimFixture(t)
	f.build()
	delete(f.runner.bins, "xcrun")
	r := f.scan()
	for _, it := range r.final {
		if strings.HasPrefix(it.Kind, "ios-simulator") {
			t.Errorf("no xcrun: unexpected %s", it.ID)
		}
	}
	// xcrun present but simctl failing (Command Line Tools only).
	f2 := newSimFixture(t)
	f2.build()
	delete(f2.runner.out, "xcrun simctl list -j")
	r2 := f2.scan()
	for _, it := range r2.final {
		if strings.HasPrefix(it.Kind, "ios-simulator") {
			t.Errorf("simctl failing: unexpected %s", it.ID)
		}
	}
}

func TestSimulatorIDsStable(t *testing.T) {
	f := newSimFixture(t)
	f.build()
	a, b := f.scan().ids(), f.scan().ids()
	if !reflect.DeepEqual(a, b) {
		t.Errorf("ids changed between scans:\n%v\n%v", a, b)
	}
	for _, want := range []string{"apple:ios-simulator:" + udidA, "apple:ios-simulators-unavailable:unavailable", "apple:ios-simulator-logs:unified-logs"} {
		if !contains(a, want) {
			t.Errorf("missing stable id %s", want)
		}
	}
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func TestPrettyRuntime(t *testing.T) {
	for in, want := range map[string]string{
		"com.apple.CoreSimulator.SimRuntime.iOS-17-5":     "iOS 17.5",
		"com.apple.CoreSimulator.SimRuntime.watchOS-11-0": "watchOS 11.0",
		"com.apple.CoreSimulator.SimRuntime.xrOS-2-0":     "visionOS 2.0",
		"weird": "weird",
	} {
		if got := prettyRuntime(in); got != want {
			t.Errorf("prettyRuntime(%q) = %q, want %q", in, got, want)
		}
	}
	if got := prettyDyld("com.apple.CoreSimulator.SimRuntime.iOS-26-4.23E254a"); got != "iOS 26.4 23E254a" {
		t.Errorf("prettyDyld = %q", got)
	}
}
