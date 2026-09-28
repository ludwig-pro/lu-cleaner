package apple

import (
	"context"
	"encoding/json"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
)

const udidF = "FFFFFFFF-0000-4000-8000-00000000000F" // unavailable, runtime gone, holds a user app

// editList rewrites the fixture's `simctl list -j` answer.
func (f *simFixture) editList(edit func(l *simList)) {
	f.t.Helper()
	var l simList
	if err := json.Unmarshal([]byte(f.runner.out["xcrun simctl list -j"]), &l); err != nil {
		f.t.Fatal(err)
	}
	edit(&l)
	b, err := json.Marshal(l)
	if err != nil {
		f.t.Fatal(err)
	}
	f.runner.out["xcrun simctl list -j"] = string(b)
}

// addRuntimeImage adds an image to the fixture's `simctl runtime list -j` answer.
func (f *simFixture) addRuntimeImage(uuid string, img runtimeImage) {
	f.t.Helper()
	imgs := map[string]runtimeImage{}
	if err := json.Unmarshal([]byte(f.runner.out["xcrun simctl runtime list -j"]), &imgs); err != nil {
		f.t.Fatal(err)
	}
	imgs[uuid] = img
	b, err := json.Marshal(imgs)
	if err != nil {
		f.t.Fatal(err)
	}
	f.runner.out["xcrun simctl runtime list -j"] = string(b)
}

func unavailableOf(t *testing.T, r *scanResult, udid string) *core.Item {
	t.Helper()
	it := r.final["apple:ios-simulators-unavailable:"+udid]
	if it == nil {
		t.Fatalf("missing unavailable item for %s (have %v)", udid, names(r.byKind("ios-simulators-unavailable")))
	}
	return it
}

// Regression: every unavailable simulator used to be grouped into one safe,
// always recommended `xcrun simctl delete unavailable`, even when it held app
// data or was only unusable because another Xcode was selected.
func TestUnavailableSimulatorsNeverBlanketDeleted(t *testing.T) {
	cautious := func(t *testing.T, it *core.Item, warn string) {
		t.Helper()
		if it.Risk != core.RiskCaution || it.Recommended || !it.NoRecommend || core.Recommend(it, now, 14*day) {
			t.Errorf("%s: must be caution and never preselected: %+v", it.Name, it)
		}
		if !strings.Contains(it.Warn, warn) {
			t.Errorf("%s: warn %q should mention %q", it.Name, it.Warn, warn)
		}
		if it.Meta["may_come_back"] != "true" {
			t.Errorf("%s: may_come_back meta missing", it.Name)
		}
	}

	t.Run("per device, never delete unavailable", func(t *testing.T) {
		f := newSimFixture(t)
		f.build()
		f.editList(func(l *simList) {
			l.Devices[rtIOS164] = append(l.Devices[rtIOS164], f.device(udidF, "Old QA", "iPhone-14", "Shutdown", false, ago(200*day)))
		})
		f.container(udidF, "Bundle/Application", "44444444-0000-4000-8000-000000000001", "com.example.legacy")
		r := f.scan()
		for _, it := range r.final {
			if strings.Join(it.Command, " ") == "xcrun simctl delete unavailable" {
				t.Errorf("%s: blanket `simctl delete unavailable` deletes devices that were not scanned", it.ID)
			}
		}
		if n := len(r.byKind("ios-simulators-unavailable")); n != 2 {
			t.Fatalf("want one item per unavailable device, got %d", n)
		}
		e := unavailableOf(t, r, udidE)
		if e.Risk != core.RiskSafe || !core.Recommend(e, now, 14*day) {
			t.Errorf("E (runtime gone, no app) should stay safe and recommended: %+v", e)
		}
		fi := unavailableOf(t, r, udidF)
		if fi.Risk != core.RiskCaution || fi.Recommended || core.Recommend(fi, now, 14*day) ||
			fi.Meta["apps"] != "com.example.legacy" || !strings.Contains(fi.Note, "com.example.legacy") ||
			strings.Join(fi.Command, " ") != "xcrun simctl delete "+udidF || fi.Covers != f.dev(udidF) {
			t.Errorf("F (user app) must be caution, not recommended, and delete only itself: %+v", fi)
		}
	})

	t.Run("runtime still listed (another Xcode selected)", func(t *testing.T) {
		f := newSimFixture(t)
		f.build()
		f.editList(func(l *simList) {
			l.Runtimes = append(l.Runtimes, simRuntime{Identifier: rtIOS164, Name: "iOS 16.4", Version: "16.4", IsAvailable: false})
		})
		cautious(t, unavailableOf(t, f.scan(), udidE), "still installed")
	})

	t.Run("runtime image still on disk (not mounted)", func(t *testing.T) {
		f := newSimFixture(t)
		f.build()
		f.addRuntimeImage("5EB08DF5-AB86-44F4-9DC7-D09DAF3F9355", runtimeImage{
			Identifier: "5EB08DF5-AB86-44F4-9DC7-D09DAF3F9355", RuntimeIdentifier: rtIOS164,
			Version: "16.4", Build: "20E247", Kind: "Disk Image", State: "Unusable", Deletable: true,
		})
		cautious(t, unavailableOf(t, f.scan(), udidE), "still on disk")
	})

	t.Run("runtime bundled with another Xcode", func(t *testing.T) {
		f := newSimFixture(t)
		f.build()
		rt := filepath.Join(f.sys, "Applications/Xcode-14.3.app/Contents/Developer/Platforms/iPhoneOS.platform",
			"Library/Developer/CoreSimulator/Profiles/Runtimes/iOS.simruntime")
		f.plist(filepath.Join(rt, "Contents/Info.plist"), `<key>CFBundleIdentifier</key><string>`+rtIOS164+`</string>`)
		cautious(t, unavailableOf(t, f.scan(), udidE), "Xcode-14.3.app")
	})

	t.Run("runtime bundled with an Xcode in ~/Applications", func(t *testing.T) {
		f := newSimFixture(t)
		f.build()
		rt := f.p("Applications/Xcode-beta.app/Contents/Developer/Platforms/iPhoneOS.platform/Library/Developer/CoreSimulator/Profiles/Runtimes/iOS.simruntime")
		f.plist(filepath.Join(rt, "Contents/Info.plist"), `<key>CFBundleIdentifier</key><string>`+rtIOS164+`</string>`)
		cautious(t, unavailableOf(t, f.scan(), udidE), "Xcode-beta.app")
	})

	t.Run("legacy runtime bundle with an unreadable Info.plist", func(t *testing.T) {
		f := newSimFixture(t)
		f.build()
		f.file(filepath.Join(f.sys, "Library/Developer/CoreSimulator/Profiles/Runtimes/iOS 16.4.simruntime/Contents/Info.plist"), 10, ago(day))
		cautious(t, unavailableOf(t, f.scan(), udidE), "still on disk")
	})

	t.Run("bundles of other runtimes do not count", func(t *testing.T) {
		f := newSimFixture(t)
		f.build()
		rt := filepath.Join(f.sys, "Library/Developer/CoreSimulator/Profiles/Runtimes/iOS 18.2.simruntime")
		f.plist(filepath.Join(rt, "Contents/Info.plist"), `<key>CFBundleIdentifier</key><string>`+rtIOS182+`</string>`)
		if e := unavailableOf(t, f.scan(), udidE); e.Risk != core.RiskSafe || !e.Recommended {
			t.Errorf("E: %+v", e)
		}
	})

	// CoreSimulator's own image database, read directly: `simctl runtime list`
	// may come back incomplete (e.g. while simdiskimaged is broken after a
	// macOS update) and would then prove nothing.
	imageDB := func(f *simFixture, rid, dmg string) {
		u := (&url.URL{Scheme: "file", Path: dmg}).String()
		f.plist(filepath.Join(f.sys, "Library/Developer/CoreSimulator/Images/images.plist"),
			`<key>images</key><array><dict><key>runtimeInfo</key><dict><key>bundleIdentifier</key><string>`+rid+
				`</string></dict><key>path</key><dict><key>relative</key><string>`+u+`</string></dict></dict></array>`)
	}
	t.Run("runtime image registered in the image database", func(t *testing.T) {
		f := newSimFixture(t)
		f.build()
		dmg := f.file(filepath.Join(f.sys, "Library/Developer/CoreSimulator/Images/iOS-16.4.dmg"), 10, ago(day))
		imageDB(f, rtIOS164, dmg)
		cautious(t, unavailableOf(t, f.scan(), udidE), "still on disk")
	})

	t.Run("stale image database entry (image file gone)", func(t *testing.T) {
		f := newSimFixture(t)
		f.build()
		imageDB(f, rtIOS164, filepath.Join(f.sys, "Library/Developer/CoreSimulator/Images/gone.dmg"))
		if e := unavailableOf(t, f.scan(), udidE); e.Risk != core.RiskSafe || !e.Recommended {
			t.Errorf("E: %+v", e)
		}
	})

	asset := func(f *simFixture, coll, version string) {
		f.plist(filepath.Join(f.sys, "System/Library/AssetsV2", coll, "26c9174130fa5962f3e60f2a49963194dadbae4c.asset/Info.plist"),
			`<key>MobileAssetProperties</key><dict><key>Build</key><string>20E247</string><key>SimulatorVersion</key><string>`+version+`</string></dict>`)
	}
	t.Run("downloaded runtime asset still on disk", func(t *testing.T) {
		f := newSimFixture(t)
		f.build()
		asset(f, "com_apple_MobileAsset_iOSSimulatorRuntime", "16.4")
		cautious(t, unavailableOf(t, f.scan(), udidE), ".asset")
	})

	t.Run("assets of other versions or platforms do not count", func(t *testing.T) {
		f := newSimFixture(t)
		f.build()
		asset(f, "com_apple_MobileAsset_iOSSimulatorRuntime", "17.5")
		asset(f, "com_apple_MobileAsset_watchOSSimulatorRuntime", "16.4")
		if e := unavailableOf(t, f.scan(), udidE); e.Risk != core.RiskSafe || !e.Recommended {
			t.Errorf("E: %+v", e)
		}
	})

	t.Run("runtime images unknown", func(t *testing.T) {
		f := newSimFixture(t)
		f.build()
		delete(f.runner.out, "xcrun simctl runtime list -j")
		cautious(t, unavailableOf(t, f.scan(), udidE), "cannot verify")
	})

	t.Run("availability error hints at an unsupported runtime", func(t *testing.T) {
		f := newSimFixture(t)
		f.build()
		f.editList(func(l *simList) {
			l.Devices[rtIOS164][0].AvailabilityError = "The iOS 16.4 simulator runtime is not supported on macOS 26.5"
		})
		cautious(t, unavailableOf(t, f.scan(), udidE), "not supported")
	})
}

// Regression: the recordings of an unavailable simulator were counted twice
// (their own item plus the `simctl delete unavailable` group, which had no Covers).
func TestUnavailableRecordingsNotDoubleCounted(t *testing.T) {
	f := newSimFixture(t)
	f.build()
	tm := f.container(udidE, "Data/InternalDaemon", "22222222-0000-4000-8000-00000000000E", "com.apple.testmanagerd")
	f.file(filepath.Join(tm, "tmp/Attachments/8A0C7F31-9A3E-4E43-9D59-2E0A1F3A0B11"), 4<<20, ago(300*day))
	r := f.scan()

	var mine, rec []*core.Item
	var recordings *core.Item
	for _, it := range r.final {
		if it.Kind == "ios-simulators-unavailable" || it.Meta["udid"] == udidE {
			mine = append(mine, it)
			if it.Recommended {
				rec = append(rec, it)
			}
			if it.Kind == "ios-simulator-attachments" {
				recordings = it
			}
		}
	}
	if recordings == nil || recordings.Size < 4<<20 {
		t.Fatalf("recordings of the unavailable simulator should still be listed: %v", names(mine))
	}
	st, err := fsx.Size(context.Background(), f.dev(udidE), nil)
	if err != nil {
		t.Fatal(err)
	}
	onDisk := st.Bytes
	if got := core.Total(mine); got != onDisk {
		t.Errorf("total = %d, want the device folder size %d: recordings counted twice", got, onDisk)
	}
	if got := core.Total(rec); got != onDisk {
		t.Errorf("recommended total = %d, want %d", got, onDisk)
	}
	dev := unavailableOf(t, r, udidE)
	if dev.Size != onDisk || dev.Meta["has_recordings"] != "true" {
		t.Errorf("device item must measure its whole folder: %+v", dev)
	}
	// Cleaning both runs the device command only.
	if top := core.TopLevel(mine); len(top) != 1 || top[0] != dev {
		t.Errorf("TopLevel should keep only the device command, got %v", names(top))
	}
}

func TestUnavailableRecheck(t *testing.T) {
	devices := func(rt string, d simDevice) string {
		b, _ := json.Marshal(map[string]any{"devices": map[string][]simDevice{rt: {d}}})
		return string(b)
	}
	gone := simDevice{UDID: udidE, Name: "iPhone 14", State: "Shutdown", IsAvailable: false}
	back := gone
	back.IsAvailable = true
	other := gone
	other.UDID = udidA
	for _, tc := range []struct {
		name, out string
		wantErr   string
	}{
		{"still unavailable", devices(rtIOS164, gone), ""},
		{"lowercase udid", devices(rtIOS164, simDevice{UDID: strings.ToLower(udidE), State: "Shutdown"}), ""},
		{"available again", devices(rtIOS164, back), "available again"},
		{"runtime changed", devices(rtIOS182, gone), "changed runtime"},
		{"no longer listed", devices(rtIOS164, other), "no longer listed"},
		{"garbage", "oops", "cannot verify"},
		{"no devices section", "{}", "cannot verify"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newFakeRunner("xcrun")
			r.out["xcrun simctl list -j devices"] = tc.out
			err := unavailableRecheck(r, udidE, rtIOS164)(context.Background())
			if tc.wantErr == "" && err != nil || tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Errorf("err = %v, want %q", err, tc.wantErr)
			}
		})
	}
	// simctl failing: refuse.
	if err := unavailableRecheck(newFakeRunner("xcrun"), udidE, rtIOS164)(context.Background()); err == nil {
		t.Error("simctl failure must refuse the deletion")
	}
}

func TestAssetPlatformAndVersion(t *testing.T) {
	for coll, want := range map[string]string{
		"com_apple_MobileAsset_iOSSimulatorRuntime":       "iOS",
		"com_apple_MobileAsset_watchOSSimulatorRuntime":   "watchOS",
		"com_apple_MobileAsset_appleTVOSSimulatorRuntime": "tvOS",
		"com_apple_MobileAsset_xrOSSimulatorRuntime":      "visionOS",
		"com_apple_MobileAsset_SoftwareUpdate":            "",
	} {
		if got := assetPlatform(coll); got != want {
			t.Errorf("assetPlatform(%q) = %q, want %q", coll, got, want)
		}
	}
	for v, want := range map[string]string{"26.5": "26.5", "17.0.1": "17.0", "18": "18.0", "": "", "beta": "", "1.x": ""} {
		if got := majorMinor(v); got != want {
			t.Errorf("majorMinor(%q) = %q, want %q", v, got, want)
		}
	}
	// The asset key must match what prettyRuntime derives from identifiers.
	if got := "iOS " + majorMinor("16.4"); got != prettyRuntime(rtIOS164) {
		t.Errorf("%q != %q", got, prettyRuntime(rtIOS164))
	}
}
