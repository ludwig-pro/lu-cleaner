package android

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
)

// Regression tests for defects found by the adversarial review.

// keep-latest-ignored: config keep_latest (env.KeepLatest) sets how many of
// the newest NDKs, build-tools, platforms and CMake versions are kept.
func TestKeepLatestFromConfig(t *testing.T) {
	f := machine(t)
	// Default (keep 1): the older unused versions are recommended.
	r1 := f.scan(opts{arm64: true})
	for _, c := range []struct{ kind, at string }{
		{"android-ndk", "ndk/25.1.8937393"},
		{"android-build-tools", "build-tools/33.0.1"},
		{"android-platform", "platforms/android-33"},
		{"android-cmake", "cmake/3.18.1"},
	} {
		if it := r1.at(t, c.kind, c.at); !r1.recommended(it) {
			t.Errorf("keep 1: %s should be recommended", c.at)
		}
	}
	// keep_latest = 3: the three newest of each package are kept.
	r3 := f.scan(opts{arm64: true, keep: 3})
	for _, c := range []struct{ kind, at string }{
		{"android-ndk", "ndk/25.1.8937393"},
		{"android-build-tools", "build-tools/33.0.1"},
		{"android-platform", "platforms/android-33"},
		{"android-cmake", "cmake/3.18.1"},
	} {
		it := r3.at(t, c.kind, c.at)
		if r3.recommended(it) || it.Recommended {
			t.Errorf("keep 3: %s is one of the 3 newest and must be kept", c.at)
		}
		if !strings.Contains(it.Meta["kept"], "keep_latest") {
			t.Errorf("keep 3: %s meta kept = %q", c.at, it.Meta["kept"])
		}
	}
}

// With no Android Studio bundle found, keep_latest settings directories are
// treated as current.
func TestStudioFallbackKeepLatest(t *testing.T) {
	f := newFx(t)
	day := 24 * time.Hour
	for i, v := range []string{"2024.2", "2023.3", "2022.3"} {
		rel := "Library/Application Support/Google/AndroidStudio" + v
		f.blob(rel+"/options/a.xml", 100, 0)
		ts := f.now.Add(-time.Duration(i*100) * day)
		os.Chtimes(f.p(rel), ts, ts)
	}
	r := f.scan(opts{keep: 2})
	r.none(t, "android-studio-config", "AndroidStudio2024.2")
	r.none(t, "android-studio-config", "AndroidStudio2023.3")
	r.at(t, "android-studio-config", "AndroidStudio2022.3")
}

// With an installed Android Studio, keep_latest > 1 also keeps the most
// recently used older versions (caches and settings) until N are kept; the
// default (1) keeps only the installed one.
func TestStudioInstalledKeepLatest(t *testing.T) {
	f := newFx(t)
	day := 24 * time.Hour
	f.file("Applications/Android Studio.app/Contents/Resources/product-info.json", `{"name":"Android Studio","dataDirectoryName":"AndroidStudio2025.1.1"}`, 0)
	for i, v := range []string{"2025.1.1", "2024.3", "2023.1"} {
		ts := f.now.Add(-time.Duration(i*100) * day)
		for _, root := range []string{"Library/Application Support/Google/", "Library/Caches/Google/"} {
			rel := root + "AndroidStudio" + v
			f.blob(rel+"/options/a.xml", 1000, time.Duration(i*100)*day)
			os.Chtimes(f.p(rel), ts, ts)
		}
	}
	r1 := f.scan(opts{})
	if it := r1.at(t, "android-studio-cache", "AndroidStudio2024.3"); !r1.recommended(it) {
		t.Errorf("keep 1: caches of the previous version should be recommended: %+v", it)
	}
	r1.at(t, "android-studio-config", "AndroidStudio2024.3")

	r2 := f.scan(opts{keep: 2})
	if it := r2.at(t, "android-studio-cache", "AndroidStudio2024.3"); r2.recommended(it) || it.Risk != core.RiskModerate {
		t.Errorf("keep 2: caches of the previous version must be kept: %+v", it)
	}
	r2.none(t, "android-studio-config", "AndroidStudio2024.3")
	if it := r2.at(t, "android-studio-cache", "AndroidStudio2023.1"); !r2.recommended(it) {
		t.Errorf("keep 2: caches of the third version should be recommended: %+v", it)
	}
	r2.at(t, "android-studio-config", "AndroidStudio2023.1")
}

// reclaim-zero-semantics: a tree entirely hardlinked elsewhere frees ~0
// bytes, not its full size (Reclaim 0 means "same as Size").
func TestAddFullyHardlinkedReclaim(t *testing.T) {
	f := newFx(t)
	orig := f.blob("Documents/repo.xml", 100_000, 0)
	os.MkdirAll(f.p(".android/cache"), 0o755)
	if err := os.Link(orig, f.p(".android/cache/repo.xml")); err != nil {
		t.Fatal(err)
	}
	r := f.scan(opts{})
	uc := r.byKind("android-user-cache")
	if len(uc) != 1 {
		t.Fatalf("user cache items = %d", len(uc))
	}
	it := uc[0]
	if it.Size < 100_000 || it.Freed() >= it.Size {
		t.Errorf("fully hardlinked item: size %d, freed %d (want ~0)", it.Size, it.Freed())
	}
}

// An installed Android Studio without a settings folder yet has never been
// started: it imports the settings of an older version at first launch, so
// those are never preselected (provider veto), however old they are.
func TestStudioOldSettingsKeptUntilFirstLaunch(t *testing.T) {
	f := newFx(t)
	f.file("Applications/Android Studio.app/Contents/Resources/product-info.json", `{"name":"Android Studio","dataDirectoryName":"AndroidStudio2025.1.1"}`, 0)
	f.blob("Library/Application Support/Google/AndroidStudio2024.3/options/other.xml", 100, 400*24*time.Hour)
	old := f.now.Add(-400 * 24 * time.Hour)
	os.Chtimes(f.p("Library/Application Support/Google/AndroidStudio2024.3"), old, old)

	r := f.scan(opts{})
	cfg := r.at(t, "android-studio-config", "AndroidStudio2024.3")
	if !cfg.NoRecommend || cfg.Recommended || r.recommended(cfg) {
		t.Errorf("settings to import at first launch must not be recommended: %+v", cfg)
	}
	if !strings.Contains(cfg.Note, "2025.1.1") || !strings.Contains(cfg.Note, "first launch") {
		t.Errorf("note = %q", cfg.Note)
	}

	// Once the installed version has its own settings, the old ones are recommended again.
	f.blob("Library/Application Support/Google/AndroidStudio2025.1.1/options/other.xml", 100, 0)
	r2 := f.scan(opts{})
	cfg2 := r2.at(t, "android-studio-config", "AndroidStudio2024.3")
	if cfg2.NoRecommend || !r2.recommended(cfg2) {
		t.Errorf("old settings after the first launch: %+v", cfg2)
	}
}

// Android Studio keeps its Local History (past versions of the user's files)
// in the caches folder of each version: it is never part of a caches item.
func TestStudioCachesKeepLocalHistory(t *testing.T) {
	f := newFx(t)
	f.file("Applications/Android Studio.app/Contents/Resources/product-info.json", `{"name":"Android Studio","dataDirectoryName":"AndroidStudio2025.1.1"}`, 0)
	const c = "Library/Caches/Google/"
	f.blob(c+"AndroidStudio2025.1.1/index/x", 1000, 0)
	f.blob(c+"AndroidStudio2025.1.1/LocalHistory/changes.storageData", 400_000, 0)
	f.blob(c+"AndroidStudio2023.1/caches/x", 1000, 0)
	f.blob(c+"AndroidStudio2023.1/LocalHistory/changes.storageData", 400_000, 0)
	f.blob(c+"AndroidStudio2022.3/LocalHistory/changes.storageData", 400_000, 0) // nothing else left
	f.blob(c+"AndroidStudio2021.1/index/x", 1000, 0)                             // no local history: whole folder

	r := f.scan(opts{})
	r.checkSafety(t)
	for _, it := range r.byKind("android-studio-cache") {
		for _, p := range it.Targets() {
			if strings.Contains(strings.ToLower(p+"/"), "/localhistory/") {
				t.Errorf("%s proposes the local history: %s", it.ID, p)
			}
			if _, err := os.Stat(filepath.Join(p, "LocalHistory")); err == nil {
				t.Errorf("%s target %s contains the local history", it.ID, p)
			}
		}
		if it.Size >= 400_000 {
			t.Errorf("%s size %d includes the local history", it.ID, it.Size)
		}
	}
	cur := r.at(t, "android-studio-cache", "AndroidStudio2025.1.1/…")
	if len(cur.Paths) != 1 || filepath.Base(cur.Paths[0]) != "index" || cur.Path != "" || cur.Risk != core.RiskModerate {
		t.Errorf("current caches: %+v", cur)
	}
	if !strings.Contains(cur.Note, "LocalHistory") {
		t.Errorf("note = %q", cur.Note)
	}
	old := r.at(t, "android-studio-cache", "AndroidStudio2023.1/…")
	if len(old.Paths) != 1 || filepath.Base(old.Paths[0]) != "caches" || !r.recommended(old) {
		t.Errorf("old caches: %+v", old)
	}
	r.none(t, "android-studio-cache", "AndroidStudio2022.3")
	r.none(t, "android-studio-cache", "AndroidStudio2022.3/…")
	if whole := r.at(t, "android-studio-cache", "AndroidStudio2021.1"); whole.Path != f.p(c+"AndroidStudio2021.1") {
		t.Errorf("caches without local history: %+v", whole)
	}
}
