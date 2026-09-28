package apple

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
)

const dd = "Library/Developer/Xcode/DerivedData"

func ddInfo(ws string, accessed time.Time) string {
	return `<key>WorkspacePath</key><string>` + ws + `</string>
<key>LastAccessedDate</key><date>` + accessed.UTC().Format(time.RFC3339) + `</date>`
}

func TestDerivedData(t *testing.T) {
	f := newFixture(t)
	// Live project: workspace exists, last opened 20 days ago, built 3 days ago.
	ws := f.dir("code/shop/ios/Shop.xcworkspace", ago(400*day))
	live := dd + "/Shop-abcdefghijklmnopqrstuvwxyzab"
	f.plist(live+"/info.plist", ddInfo(ws, ago(20*day)))
	f.file(live+"/Build/Products/Debug-iphonesimulator/Shop.app/Shop", 300_000, ago(3*day))
	ageTree(t, f.p(live), ago(20*day))
	ageTree(t, f.p(live+"/Build/Products"), ago(3*day))
	// Orphan: its worktree was removed.
	orphan := dd + "/Shop-zyxwvutsrqponmlkjihgfedcbazy"
	f.plist(orphan+"/info.plist", ddInfo(f.p(".codex/worktrees/a1b2/shop/ios/Shop.xcworkspace"), ago(40*day)))
	f.file(orphan+"/Index.noindex/DataStore/v5/units/x", 100_000, ago(40*day))
	ageTree(t, f.p(orphan), ago(40*day))
	// Workspace on an unmounted volume: unknown, not an orphan.
	ext := dd + "/Game-aaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	f.plist(ext+"/info.plist", ddInfo("/Volumes/NoSuchDisk-lu-cleaner-test/game/Game.xcodeproj", ago(10*day)))
	// No info.plist at all.
	f.file(dd+"/Weird/Logs/Build/x.xcactivitylog", 10_000, ago(5*day))
	ageTree(t, f.p(dd+"/Weird"), ago(5*day))
	// Shared caches belong to the catalog.
	f.file(dd+"/ModuleCache.noindex/Foundation.pcm", 50_000, ago(time.Hour))
	f.file(dd+"/SymbolCache.noindex/x", 50_000, ago(time.Hour))
	f.alive = []string{"xcodebuild"}

	r := f.scan()
	items := r.byKind("xcode-derived-data")
	if len(items) != 4 {
		t.Fatalf("want 4 DerivedData items, got %v", names(items))
	}
	get := func(rel string) *core.Item {
		t.Helper()
		it := r.final["apple:xcode-derived-data:"+f.p(rel)]
		if it == nil {
			t.Fatalf("no item for %s", rel)
		}
		return it
	}

	l := get(live)
	if l.Name != "Shop" || l.Risk != core.RiskSafe || l.Method != core.MethodDelete || l.Recommended {
		t.Errorf("live item: %+v", l)
	}
	if l.Project != f.p("code/shop") || l.Meta["workspace"] != ws {
		t.Errorf("live project=%q meta=%v", l.Project, l.Meta)
	}
	if want := ago(3 * day); !l.LastUsed.Equal(want) {
		t.Errorf("live LastUsed = %v, want the last build %v", l.LastUsed, want)
	}
	if !reflect.DeepEqual(l.ProcessGuard, []string{"Xcode", "xcodebuild"}) || !strings.Contains(l.Warn, "xcodebuild is running") {
		t.Errorf("process guard / warn: %v %q", l.ProcessGuard, l.Warn)
	}
	if l.Size < 300_000 {
		t.Errorf("live size = %d", l.Size)
	}

	o := get(orphan)
	if !o.Recommended || o.Meta["orphan"] != "true" || o.Meta["worktree"] != "true" || !strings.Contains(o.Name, "workspace gone") {
		t.Errorf("orphan item: %+v", o)
	}
	if !o.LastUsed.Equal(ago(40 * day)) {
		t.Errorf("orphan LastUsed = %v", o.LastUsed)
	}
	if e := get(ext); e.Recommended || e.Meta["orphan"] != "" {
		t.Errorf("unmounted workspace must not be an orphan: %+v", e)
	}
	if w := get(dd + "/Weird"); w.Name != "Weird" || !w.LastUsed.Equal(ago(5*day)) {
		t.Errorf("no-info item: %+v", w)
	}
	for id := range r.final {
		if strings.Contains(id, ".noindex") {
			t.Errorf("shared *.noindex caches are the catalog's: %s", id)
		}
	}

	// Placeholders first: every DerivedData item is emitted sizing, then measured.
	if em := r.emits["apple:xcode-derived-data:"+f.p(live)]; len(em) != 2 || !em[0].Sizing || em[1].Sizing {
		t.Errorf("want placeholder then measured item, got %d emits", len(em))
	}

	// IDs are stable across scans.
	if a, b := r.ids(), f.scan().ids(); !reflect.DeepEqual(a, b) {
		t.Errorf("ids changed between scans:\n%v\n%v", a, b)
	}
}

func TestDerivedDataCustomLocation(t *testing.T) {
	f := newFixture(t, "defaults")
	custom := f.dir("Builds/DD", ago(day))
	f.runner.out["defaults read com.apple.dt.Xcode IDECustomDerivedDataLocation"] = "~/Builds/DD\n"
	f.plist("Builds/DD/App-abcdefghijklmnopqrstuvwxyzab/info.plist", ddInfo(f.p("gone/App.xcworkspace"), ago(day)))
	f.file("Builds/DD/ModuleCache.noindex/x.pcm", 20_000, ago(day))
	r := f.scan()
	if it := r.one(t, "xcode-derived-data"); !strings.HasPrefix(it.Path, custom) || !it.Recommended {
		t.Errorf("custom DerivedData item: %+v", it)
	}
	mc := r.one(t, "xcode-module-cache")
	if mc.Path != filepath.Join(custom, "ModuleCache.noindex") || mc.Risk != core.RiskSafe {
		t.Errorf("custom module cache: %+v", mc)
	}
}

func TestDerivedDataProtectedAndExcluded(t *testing.T) {
	f := newFixture(t)
	a := f.p(dd + "/A-abcdefghijklmnopqrstuvwxyzab")
	b := f.p(dd + "/B-abcdefghijklmnopqrstuvwxyzab")
	f.file(a+"/Build/x", 1000, ago(day))
	f.file(b+"/Build/x", 1000, ago(day))
	f.env.Exclude = []string{a}
	prot := f.env.Protected
	f.env.Protected = func(p string) bool { return p == b || prot(p) }
	if items := f.scan().byKind("xcode-derived-data"); len(items) != 0 {
		t.Errorf("excluded/protected folders must not be proposed: %v", names(items))
	}
}

func TestArchives(t *testing.T) {
	f := newFixture(t)
	arch := func(rel, version, build string, created time.Time) string {
		p := f.p("Library/Developer/Xcode/Archives/" + rel)
		f.plist(p+"/Info.plist", `<key>Name</key><string>Shop</string>
<key>SchemeName</key><string>Shop</string>
<key>CreationDate</key><date>`+created.Format(time.RFC3339)+`</date>
<key>ApplicationProperties</key><dict>
  <key>CFBundleIdentifier</key><string>dev.example.shop</string>
  <key>CFBundleShortVersionString</key><string>`+version+`</string>
  <key>CFBundleVersion</key><string>`+build+`</string>
  <key>Team</key><string>ABCDE12345</string>
</dict>`)
		f.file(p+"/dSYMs/Shop.app.dSYM/Contents/Resources/DWARF/Shop", 200_000, created)
		return p
	}
	old := arch("2025-01-10/Shop 2025-01-10 10.00.00.xcarchive", "1.0.0", "1", ago(600*day))
	newer := arch("2026-09-01/Shop 2026-09-01 10.00.00.xcarchive", "1.2.0", "7", ago(27*day))
	f.dir("Library/Developer/Xcode/Archives/2024-10-21", ago(700*day)) // empty date folder

	r := f.scan()
	items := r.byKind("xcode-archive")
	if len(items) != 2 {
		t.Fatalf("want 2 archives, got %v", names(items))
	}
	o, n := r.final["apple:xcode-archive:"+old], r.final["apple:xcode-archive:"+newer]
	if o == nil || n == nil {
		t.Fatal("archives missing")
	}
	for _, it := range []*core.Item{o, n} {
		if it.Risk != core.RiskCaution || it.Method != core.MethodTrash || it.Recommended {
			t.Errorf("%s: archives are caution/trash/never recommended: %+v", it.Name, it)
		}
		if core.Recommend(it, now, 14*day) {
			t.Errorf("%s: smart select must never pick an archive", it.Name)
		}
	}
	if n.Name != "Shop 1.2.0 (7)" || n.Meta["latest"] != "true" || n.Warn == "" || n.Meta["bundle_id"] != "dev.example.shop" {
		t.Errorf("newest archive: %+v", n)
	}
	if o.Meta["latest"] != "" || o.Warn != "" || !o.LastUsed.Equal(ago(600*day)) {
		t.Errorf("old archive: %+v", o)
	}
}

func TestDeviceSupportKeepsNewest(t *testing.T) {
	f := newFixture(t)
	ds := func(dir, name string, age time.Duration) string {
		p := f.p("Library/Developer/Xcode/" + dir + "/" + name)
		f.file(p+"/Symbols/usr/lib/dyld", 100_000, ago(age))
		ageTree(t, p, ago(age))
		return p
	}
	newest := ds("iOS DeviceSupport", "iPhone15,2 17.10 (21H100)", 300*day) // highest version, even if old
	stale := ds("iOS DeviceSupport", "16.4 (20E247) arm64e", 200*day)
	recent := ds("iOS DeviceSupport", "17.9 (21G80)", 30*day)
	watch := ds("watchOS DeviceSupport", "10.0 (21R356)", 400*day)

	r := f.scan()
	if n := len(r.byKind("xcode-device-support")); n != 4 {
		t.Fatalf("want 4 DeviceSupport items, got %d", n)
	}
	get := func(p string) *core.Item { return r.final["apple:xcode-device-support:"+p] }
	cases := []struct {
		path        string
		name        string
		recommended bool
		warn        bool
	}{
		{newest, "iOS 17.10 (21H100) · iPhone15,2", false, true},
		{stale, "iOS 16.4 (20E247)", true, false},
		{recent, "iOS 17.9 (21G80)", false, false},
		{watch, "watchOS 10.0 (21R356)", false, true},
	}
	for _, c := range cases {
		it := get(c.path)
		if it == nil {
			t.Fatalf("missing %s", c.path)
		}
		if it.Name != c.name || it.Recommended != c.recommended || (it.Warn != "") != c.warn || it.Risk != core.RiskModerate {
			t.Errorf("%s: name=%q rec=%v warn=%q risk=%v", c.name, it.Name, it.Recommended, it.Warn, it.Risk)
		}
	}
	// The newest version of a platform is never preselected by smart select.
	if core.Recommend(get(newest), now, 14*day) || core.Recommend(get(watch), now, 14*day) {
		t.Error("newest DeviceSupport must stay out of smart select")
	}
}

func TestParseDeviceSupportAndVersions(t *testing.T) {
	for _, c := range []struct{ in, model, ver, build, arch string }{
		{"17.0 (21A329)", "", "17.0", "21A329", ""},
		{"iPhone15,2 17.0.3 (21A360)", "iPhone15,2", "17.0.3", "21A360", ""},
		{"16.4 (20E247) arm64e", "", "16.4", "20E247", "arm64e"},
	} {
		m, v, b, a, ok := parseDeviceSupport(c.in)
		if !ok || m != c.model || v != c.ver || b != c.build || a != c.arch {
			t.Errorf("%q → %q %q %q %q %v", c.in, m, v, b, a, ok)
		}
	}
	if _, _, _, _, ok := parseDeviceSupport("Info.plist"); ok {
		t.Error("garbage must not parse")
	}
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"17.10", "17.9", 1}, {"17.0", "17", 0}, {"16.4.1", "16.4", 1}, {"26.5", "26.5", 0},
		{"9", "10", -1}, {"", "1", -1}, {"18.0b1", "18.0b2", -1},
	} {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%q,%q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestDeviceSets(t *testing.T) {
	f := newFixture(t, "xcrun")
	ib := f.p("Library/Developer/Xcode/UserData/IB Support/Simulator Devices")
	f.file(ib+"/76DB897C-CA4B-4A54-A774-635C631F69D4/data/Library/x", 2<<20, ago(3*day))
	f.plist(ib+"/device_set.plist", "")
	// A never-booted Playground clone weighs nothing: no line.
	f.dir("Library/Developer/XCPGDevices/FE88FD50-3776-4D02-9D69-8234E2EA4578/data", ago(100*day))
	// No device in the Previews set.
	f.plist("Library/Developer/Xcode/UserData/Previews/Simulator Devices/device_set.plist", "")

	r := f.scan()
	it := r.one(t, "ios-simulator-device-set")
	if it.Name != "Interface Builder simulators (1)" || it.Risk != core.RiskSafe || it.Method != core.MethodCommand {
		t.Errorf("IB set: %+v", it)
	}
	if want := []string{"xcrun", "simctl", "--set", ib, "delete", "all"}; !reflect.DeepEqual(it.Command, want) {
		t.Errorf("command = %v", it.Command)
	}
	if it.Size < 2<<20 || !core.Recommend(it, now, 14*day) {
		t.Errorf("IB set should be measured and recommended: size=%d", it.Size)
	}

	// Without xcrun (no Xcode) nothing is proposed.
	f2 := newFixture(t)
	f2.file("Library/Developer/XCTestDevices/76DB897C-CA4B-4A54-A774-635C631F69D4/data/x", 2<<20, ago(day))
	if items := f2.scan().byKind("ios-simulator-device-set"); len(items) != 0 {
		t.Errorf("no xcrun: got %v", names(items))
	}
}

func TestCoreSimulatorCaches(t *testing.T) {
	f := newFixture(t)
	f.dir("Library/Developer/CoreSimulator/Temp/BackgroundDelete", ago(day)) // empty
	if items := f.scan().byKind("coresimulator-caches"); len(items) != 0 {
		t.Fatalf("empty caches must not be shown: %v", names(items))
	}
	f.file("Library/Developer/CoreSimulator/Caches/dyld/21A5/x", 500_000, ago(90*day))
	f.alive = []string{"Simulator"}
	it := f.scan().one(t, "coresimulator-caches")
	if it.Risk != core.RiskSafe || !reflect.DeepEqual(it.ProcessGuard, []string{"Simulator"}) || !strings.Contains(it.Warn, "Simulator is running") {
		t.Errorf("coresim caches: %+v", it)
	}
	if len(it.Paths) != 2 || it.Size < 500_000 {
		t.Errorf("paths=%v size=%d", it.Paths, it.Size)
	}
}

func TestXcodeAppsAndMetal(t *testing.T) {
	f := newFixture(t, "xcode-select", "xcodebuild")
	xcode := func(name, ver, build string) string {
		p := filepath.Join(f.sys, "Applications", name)
		f.dir(p+"/Contents/Developer", ago(day))
		f.plist(p+"/Contents/version.plist", `<key>CFBundleShortVersionString</key><string>`+ver+`</string>
<key>ProductBuildVersion</key><string>`+build+`</string>`)
		f.file(p+"/Contents/MacOS/Xcode", 100_000, ago(day))
		return p
	}
	active := xcode("Xcode-26.6.0.app", "26.6", "17F113")
	old := xcode("Xcode-26.4.1.app", "26.4.1", "17E202")
	f.file(filepath.Join(f.sys, "Applications/Xcodes.app/Contents/MacOS/Xcodes"), 1000, ago(day)) // not an Xcode
	os.Symlink(active, filepath.Join(f.sys, "Applications", "Xcode.app"))                         // alias of the active one
	f.runner.out["xcode-select -p"] = active + "/Contents/Developer\n"

	asset := f.file(filepath.Join(f.sys, "System/Library/AssetsV2/com_apple_MobileAsset_MetalToolchain/abc.asset/AssetData/metal"), 300_000, ago(day))
	f.runner.out["xcodebuild -showComponent metalToolchain -json"] = `{
  "assetPath" : "` + filepath.Dir(asset) + `",
  "buildVersion" : "17F109",
  "status" : "installed"
}`
	r := f.scan()
	x := r.one(t, "xcode-app")
	if x.Path != old || x.Name != "Xcode 26.4.1 (17E202)" || x.Method != core.MethodReport || x.CanClean() {
		t.Errorf("extra Xcode: %+v", x)
	}
	m := r.one(t, "xcode-metal-toolchain")
	if m.Location != filepath.Join(f.sys, "System/Library/AssetsV2/com_apple_MobileAsset_MetalToolchain/abc.asset") ||
		!reflect.DeepEqual(m.Command, []string{"xcodebuild", "-deleteComponent", "metalToolchain"}) || m.Risk != core.RiskModerate || m.Size < 300_000 {
		t.Errorf("metal toolchain: %+v", m)
	}

	// Command Line Tools selected: nobody knows which Xcode is in use → nothing reported.
	f.runner.out["xcode-select -p"] = "/Library/Developer/CommandLineTools\n"
	if items := f.scan().byKind("xcode-app"); len(items) != 0 {
		t.Errorf("CLT selected: got %v", names(items))
	}
}

func TestPlacement(t *testing.T) {
	f := newFixture(t)
	s := newScan(t.Context(), f.prov, f.env, func(*core.Item) {})
	in := f.dir("Library/x", ago(day))
	if got := s.place(in); got != placeInternal {
		t.Errorf("home path: %v", got)
	}
	link := f.p("dangling")
	os.Symlink(f.p("nowhere"), link)
	if got := s.place(link); got != placeMissing {
		t.Errorf("dangling symlink: %v", got)
	}
	if got := s.place("/dev"); got != placeExternal { // devfs: another st_dev
		t.Errorf("/dev: %v", got)
	}
	if got := s.place(f.sys); got != placeOutside {
		t.Errorf("outside home: %v", got)
	}

	it := &core.Item{Method: core.MethodDelete, Selectable: true, Recommended: true}
	if !s.applyPlace(it, "/dev") || it.Method != core.MethodReport || it.CanClean() || it.Recommended ||
		!strings.Contains(it.Warn, "external volume") {
		t.Errorf("external item must become a report: %+v", it)
	}
	if s.applyPlace(&core.Item{Method: core.MethodDelete}, link) {
		t.Error("dangling symlinks must never be proposed")
	}
	out := &core.Item{Method: core.MethodDelete, Selectable: true}
	if !s.applyPlace(out, f.sys) || out.Method != core.MethodReport {
		t.Errorf("paths outside home cannot be deleted by lu-cleaner: %+v", out)
	}
	cmd := &core.Item{Method: core.MethodCommand, Command: []string{"x"}, Selectable: true}
	if !s.applyPlace(cmd, f.sys) || cmd.Method != core.MethodCommand {
		t.Errorf("commands may target system storage: %+v", cmd)
	}
}

func TestScanHonoursCancellation(t *testing.T) {
	f := newSimFixture(t)
	f.build()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() { done <- f.prov.Scan(ctx, f.env, func(*core.Item) {}) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Scan = %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Scan did not stop after cancellation")
	}
}
