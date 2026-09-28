package android

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/fsx"
	"github.com/ludwig-pro/lu-cleaner/internal/safety"
)

// ------------------------------------------------------------------ fixtures

type fakeRunner struct{ out map[string]string }

func (f fakeRunner) Output(_ context.Context, _, name string, args ...string) ([]byte, error) {
	if o, ok := f.out[name+" "+strings.Join(args, " ")]; ok {
		return []byte(o), nil
	}
	return nil, errors.New("exit status 1")
}

func (f fakeRunner) LookPath(name string) (string, error) { return "", errors.New("not found") }

type fx struct {
	t    *testing.T
	home string
	tmp  string
	now  time.Time
}

func newFx(t *testing.T) *fx {
	t.Helper()
	return &fx{t: t, home: t.TempDir(), tmp: t.TempDir(), now: time.Now()}
}

func (f *fx) p(rel string) string { return filepath.Join(f.home, rel) }

// file writes rel (relative to home) and sets its mtime age ago (0 = now).
func (f *fx) file(rel, content string, age time.Duration) string {
	f.t.Helper()
	p := f.p(rel)
	if filepath.IsAbs(rel) {
		p = rel
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
	if age > 0 {
		ts := f.now.Add(-age)
		os.Chtimes(p, ts, ts)
	}
	return p
}

func (f *fx) blob(rel string, size int, age time.Duration) string {
	return f.file(rel, strings.Repeat("x", size), age)
}

func (f *fx) link(rel, target string) {
	f.t.Helper()
	p := f.p(rel)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.Symlink(target, p); err != nil {
		f.t.Fatal(err)
	}
}

type opts struct {
	env     map[string]string
	arm64   bool
	running []string
	runner  map[string]string
	roots   []string // relative to home; nil = ["src"]
	devOf   func(string) (uint64, error)
}

func (f *fx) provider(o opts) *Provider {
	return &Provider{
		getenv: func(k string) string { return o.env[k] },
		arm64:  func() bool { return o.arm64 },
		running: func(names ...string) []string {
			var hit []string
			for _, n := range names {
				for _, r := range o.running {
					if n == r {
						hit = append(hit, n)
					}
				}
			}
			return hit
		},
		devOf:        o.devOf,
		appDirs:      []string{f.p("Applications")},
		systemJVMDir: f.p("SystemJVM"),
		extraSDKs:    []string{},
	}
}

type result struct {
	items map[string]*core.Item
	guard *safety.Guard
	env   *core.Env
}

func (f *fx) scan(o opts) *result {
	f.t.Helper()
	roots := o.roots
	if roots == nil {
		roots = []string{"src"}
	}
	env := &core.Env{Home: f.home, TmpDir: f.tmp, Now: f.now, MaxDepth: 8,
		Runner: fakeRunner{out: o.runner}, Logf: f.t.Logf}
	for _, r := range roots {
		env.Roots = append(env.Roots, f.p(r))
	}
	g := safety.New(f.home, f.tmp, env.Roots, nil)
	env.Protected = g.Protected
	res := &result{items: map[string]*core.Item{}, guard: g, env: env}
	var mu sync.Mutex
	seen := map[string]int{}
	err := f.provider(o).Scan(context.Background(), env, func(it *core.Item) {
		mu.Lock()
		defer mu.Unlock()
		seen[it.ID]++
		res.items[it.ID] = it
	})
	if err != nil {
		f.t.Fatal(err)
	}
	for id, it := range res.items {
		if it.Sizing {
			f.t.Errorf("%s: still sizing at the end of the scan", id)
		}
		if it.Provider != "android" || it.Category != core.CatAndroid || it.Kind == "" || it.Name == "" || it.Note == "" {
			f.t.Errorf("%s: incomplete item %+v", id, it)
		}
		if it.Path != "" && len(it.Paths) > 0 {
			f.t.Errorf("%s: group item must not set Path", id)
		}
	}
	return res
}

// byKind returns the items of a kind, sorted by where.
func (r *result) byKind(kind string) []*core.Item {
	var out []*core.Item
	for _, it := range r.items {
		if it.Kind == kind {
			out = append(out, it)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Where() < out[j].Where() })
	return out
}

// at returns the item of kind whose Where() ends with suffix.
func (r *result) at(t *testing.T, kind, suffix string) *core.Item {
	t.Helper()
	for _, it := range r.byKind(kind) {
		if strings.HasSuffix(it.Where(), suffix) {
			return it
		}
	}
	var got []string
	for _, it := range r.byKind(kind) {
		got = append(got, it.Where())
	}
	t.Fatalf("no %s item at *%s (have %v)", kind, suffix, got)
	return nil
}

func (r *result) none(t *testing.T, kind, suffix string) {
	t.Helper()
	for _, it := range r.byKind(kind) {
		if strings.HasSuffix(it.Where(), suffix) {
			t.Errorf("unexpected %s item at %s", kind, it.Where())
		}
	}
}

func (r *result) recommended(it *core.Item) bool {
	return core.Recommend(it, r.env.Now, 14*24*time.Hour)
}

// checkSafety asserts that every cleanable target passes the guard and is
// not protected, and that no target is a symlink.
func (r *result) checkSafety(t *testing.T) {
	t.Helper()
	for id, it := range r.items {
		if !it.CanClean() {
			continue
		}
		for _, p := range it.Targets() {
			if r.env.IsProtected(p) {
				t.Errorf("%s: proposes protected path %s", id, p)
			}
			if err := r.guard.Check(p, safety.Options{AllowGitRepo: it.AllowGitRepo}); err != nil {
				t.Errorf("%s: guard refuses %s: %v", id, p, err)
			}
			if fi, err := os.Lstat(p); err == nil && fi.Mode()&os.ModeSymlink != 0 {
				t.Errorf("%s: proposes deleting symlink %s", id, p)
			}
		}
	}
}

// ------------------------------------------------------------------ machine fixture

const (
	img34 = "Library/Android/sdk/system-images/android-34/google_apis/arm64-v8a"
	img33 = "Library/Android/sdk/system-images/android-33/google_apis_playstore/arm64-v8a"
	img30 = "Library/Android/sdk/system-images/android-30/google_apis/x86_64"
)

// machine builds a realistic home: SDK, AVDs, projects, ~/.gradle, ~/.android.
func machine(t *testing.T) *fx {
	f := newFx(t)
	day := 24 * time.Hour
	sdk := "Library/Android/sdk/"
	f.file(sdk+"platform-tools/adb", "adb", 0)
	f.file(sdk+"licenses/android-sdk-license", "x", 0)
	f.blob(img34+"/system.img", 4000, 0)
	f.file(img34+"/package.xml", "<x/>", 0)
	f.blob(img33+"/system.img", 3000, 0)
	f.file(img33+"/package.xml", "<x/>", 0)
	f.blob(img30+"/system.img", 2000, 0)
	f.file(img30+"/package.xml", "<x/>", 0)
	for _, v := range []string{"25.1.8937393", "26.1.10909125", "27.1.12297006"} {
		f.file(sdk+"ndk/"+v+"/source.properties", "Pkg.Desc = Android NDK\nPkg.Revision = "+v+"\n", 0)
	}
	for _, v := range []string{"33.0.1", "34.0.0", "35.0.0-2", "36.0.0"} {
		f.blob(sdk+"build-tools/"+v+"/aapt2", 500, 0)
	}
	for _, v := range []string{"android-33", "android-34", "android-35"} {
		f.blob(sdk+"platforms/"+v+"/android.jar", 500, 0)
	}
	for _, v := range []string{"3.18.1", "3.22.1"} {
		f.blob(sdk+"cmake/"+v+"/bin/cmake", 500, 0)
	}
	f.blob(sdk+"sources/android-33/Foo.java", 100, 0)
	f.blob(sdk+"sources/android-35/Foo.java", 100, 0)
	f.blob(sdk+"extras/intel/Hardware_Accelerated_Execution_Manager/haxm", 300, 0)
	f.blob(sdk+"cmdline-tools/latest/bin/sdkmanager", 100, 0)
	f.blob(sdk+"cmdline-tools/9.0/bin/sdkmanager", 100, 0)
	f.blob(sdk+"tools/emulator", 100, 0)
	f.blob(sdk+".temp/PackageOperation01/x.zip", 700, 2*day)

	// AVDs.
	avd := f.p(".android/avd")
	f.file(".android/avd/Pixel_8.ini", "avd.ini.encoding=UTF-8\npath="+avd+"/Pixel_8.avd\npath.rel=avd/Pixel_8.avd\ntarget=android-34\n", 0)
	f.file(".android/avd/Pixel_8.avd/config.ini", "avd.ini.displayname=Pixel 8\nabi.type=arm64-v8a\nimage.sysdir.1=system-images/android-34/google_apis/arm64-v8a/\ntag.id=google_apis\nhw.device.name=pixel_8\n", 90*day)
	f.file(".android/avd/Pixel_8.avd/hardware-qemu.ini", "x", 3*day)
	f.blob(".android/avd/Pixel_8.avd/userdata-qemu.img", 5000, 3*day)
	f.blob(".android/avd/Pixel_8.avd/snapshots/default_boot/ram.bin", 3000, 3*day)
	f.file(".android/avd/Pixel_8.avd/snapshots/default_boot/snapshot.pb", "x", 3*day)

	f.file(".android/avd/Old_x86.ini", "path="+avd+"/Old_x86.avd\ntarget=android-30\n", 0)
	f.file(".android/avd/Old_x86.avd/config.ini", "abi.type=x86_64\nimage.sysdir.1=system-images/android-30/google_apis/x86_64/\n", 400*day)
	f.blob(".android/avd/Old_x86.avd/userdata-qemu.img", 2000, 400*day)

	f.file(".android/avd/Gone.ini", "path="+avd+"/Gone.avd\ntarget=android-29\n", 0)

	f.file(".android/avd/Stray.avd/config.ini", "abi.type=arm64-v8a\nimage.sysdir.1=system-images/android-34/google_apis/arm64-v8a/\n", 0)
	f.blob(".android/avd/Stray.avd/userdata-qemu.img", 1000, 0)

	f.file(".android/avd/Named.ini", "path="+avd+"/Named.avd\n", 0)
	f.file(".android/avd/Named.avd/config.ini", "abi.type=arm64-v8a\nimage.sysdir.1=system-images/android-35/google_apis/arm64-v8a/\n", 0)
	f.blob(".android/avd/Named.avd/snapshots/default_boot/ram.bin", 100, 0)
	f.blob(".android/avd/Named.avd/snapshots/before_login/ram.bin", 100, 0)

	// ~/.android identity & caches.
	f.file(".android/debug.keystore", "KEY", 0)
	f.file(".android/adbkey", "KEY", 0)
	f.blob(".android/cache/sdkbin-1_abc-repository.xml", 400, 0)
	f.blob(".android/breakpad/dump.dmp", 400, 0)

	// Projects.
	f.file("src/app1/package.json", `{"dependencies":{"react-native":"0.76.3"}}`, 0)
	f.file("src/app1/android/build.gradle", "buildscript {\n ext {\n  ndkVersion = \"26.1.10909125\"\n  buildToolsVersion = findProperty('android.buildToolsVersion') ?: '34.0.0'\n  compileSdkVersion = Integer.parseInt(findProperty('android.compileSdkVersion') ?: '34')\n }\n}\n", 0)
	f.file("src/app1/android/gradle/wrapper/gradle-wrapper.properties", "distributionBase=GRADLE_USER_HOME\ndistributionUrl=https\\://services.gradle.org/distributions/gradle-8.10.2-all.zip\n", 0)
	f.file("src/app1/node_modules/react-native/gradle/libs.versions.toml", "[versions]\ncompileSdk = \"34\"\nbuildTools = \"34.0.0\"\nndkVersion = \"27.1.12297006\"\n", 0)
	// Library wrapper inside node_modules: never counts.
	f.file("src/app1/node_modules/some-lib/android/gradle/wrapper/gradle-wrapper.properties", "distributionUrl=https\\://services.gradle.org/distributions/gradle-7.5-all.zip\n", 0)
	// A worktree-ish nested project inside .claude/worktrees.
	f.file("src/app1/.claude/worktrees/feat/android/gradle/wrapper/gradle-wrapper.properties", "distributionUrl=https\\://services.gradle.org/distributions/gradle-8.13-bin.zip\n", 0)
	// local.properties pointing at the default SDK.
	f.file("src/app1/android/local.properties", "sdk.dir="+f.p("Library/Android/sdk")+"\n", 0)

	// ~/.gradle
	dists := ".gradle/wrapper/dists/"
	f.blob(dists+"gradle-8.10.2-all/abc/gradle-8.10.2/lib/core.jar", 3000, 0)
	f.file(dists+"gradle-8.10.2-all/abc/gradle-8.10.2-all.zip.ok", "", 20*day)
	f.blob(dists+"gradle-8.13-bin/def/gradle-8.13/lib/core.jar", 3000, 0)
	f.file(dists+"gradle-8.13-bin/def/gradle-8.13-bin.zip.ok", "", 20*day)
	f.blob(dists+"gradle-7.5-all/ghi/gradle-7.5/lib/core.jar", 3000, 0)
	f.file(dists+"gradle-7.5-all/ghi/gradle-7.5-all.zip.ok", "", 300*day)
	f.blob(dists+"gradle-8.0-bin/jkl/gradle-8.0-bin.zip.part", 2000, 30*day)
	f.blob(".gradle/caches/8.10.2/kotlin-dsl/x", 1000, 0)
	f.blob(".gradle/caches/7.5/kotlin-dsl/x", 1000, 300*day)
	f.blob(".gradle/caches/transforms-3/x", 1000, 300*day)
	f.blob(".gradle/caches/transforms-4/x", 1000, 0)
	f.blob(".gradle/caches/build-cache-1/x", 1000, 0)
	f.blob(".gradle/caches/modules-2/files-2.1/lib.jar", 5000, 0)
	f.file(".gradle/caches/modules-2/modules-2.lock", "", 2*day)
	f.blob(".gradle/caches/journal-1/file-access.bin", 100, 0)
	f.blob(".gradle/caches/kotlin-dsl/accessors/x", 1000, 0)
	f.blob(".gradle/daemon/8.10.2/daemon-1.out.log", 800, 5*day)
	f.blob(".gradle/daemon/8.10.2/daemon-2.out.log", 800, time.Minute)
	f.file(".gradle/daemon/8.10.2/registry.bin", "r", 0)
	f.blob(".gradle/.tmp/gradle_download123.bin", 900, 2*day)
	f.blob(".gradle/.tmp/gradle_download999.bin", 900, 0) // fresh: in progress
	f.blob(".gradle/jdks/eclipse_adoptium-17-aarch64-os_x.2/bin/java", 700, 0)
	f.blob(".gradle/native/jansi/libjansi.jnilib", 300, 0)
	f.blob(".gradle/kotlin-profile/a.profile", 300, 0)
	f.file(".gradle/gradle.properties", "MYAPP_UPLOAD_STORE_PASSWORD=secret\n", 0)
	return f
}

// ------------------------------------------------------------------ tests

func TestSDKPackages(t *testing.T) {
	f := machine(t)
	r := f.scan(opts{arm64: true})
	r.checkSafety(t)

	// System images: used one kept, unused one recommended, x86 recommended with warning.
	used := r.at(t, "android-system-image", "android-34/google_apis/arm64-v8a")
	if used.Selectable || used.Recommended || !strings.Contains(used.Note, "Pixel_8") {
		t.Errorf("used image: %+v", used)
	}
	unused := r.at(t, "android-system-image", "android-33/google_apis_playstore/arm64-v8a")
	if !unused.CanClean() || !r.recommended(unused) || unused.Risk != core.RiskModerate || unused.Size == 0 {
		t.Errorf("unused image should be recommended: %+v", unused)
	}
	x86 := r.at(t, "android-system-image", "android-30/google_apis/x86_64")
	if !r.recommended(x86) || !strings.Contains(x86.Note, "Apple Silicon") || !x86.CanClean() {
		t.Errorf("x86 image on arm64: %+v", x86)
	}

	// NDK: project-referenced (26.1 via build.gradle, 27.1 via RN catalog) and newest kept.
	for _, v := range []string{"26.1.10909125", "27.1.12297006"} {
		if it := r.at(t, "android-ndk", "ndk/"+v); r.recommended(it) {
			t.Errorf("NDK %s is used and must not be recommended", v)
		}
	}
	if it := r.at(t, "android-ndk", "ndk/26.1.10909125"); !strings.Contains(it.Meta["used_by"], "src/app1") {
		t.Errorf("NDK used_by = %q", it.Meta["used_by"])
	}
	if it := r.at(t, "android-ndk", "ndk/25.1.8937393"); !r.recommended(it) || it.Meta["revision"] != "25.1.8937393" {
		t.Errorf("unused NDK: %+v", it)
	}

	// build-tools: 34 used, 36 newest, 33 unused, 35.0.0-2 leftover.
	if it := r.at(t, "android-build-tools", "build-tools/34.0.0"); r.recommended(it) {
		t.Error("build-tools 34 is used")
	}
	if it := r.at(t, "android-build-tools", "build-tools/36.0.0"); r.recommended(it) || it.Meta["kept"] == "" {
		t.Errorf("newest build-tools must be kept: %+v", it)
	}
	if it := r.at(t, "android-build-tools", "build-tools/33.0.1"); !r.recommended(it) {
		t.Error("build-tools 33 should be recommended")
	}
	if it := r.at(t, "android-build-tools", "build-tools/35.0.0-2"); it.Risk != core.RiskSafe || !r.recommended(it) {
		t.Errorf("leftover build-tools: %+v", it)
	}
	// platforms: 34 used (compileSdk), 35 newest, 33 unused.
	if it := r.at(t, "android-platform", "platforms/android-34"); r.recommended(it) {
		t.Error("platform 34 is used")
	}
	if it := r.at(t, "android-platform", "platforms/android-35"); r.recommended(it) {
		t.Error("platform 35 is the newest")
	}
	if it := r.at(t, "android-platform", "platforms/android-33"); !r.recommended(it) {
		t.Error("platform 33 should be recommended")
	}
	// cmake: 3.22.1 is AGP's default.
	if it := r.at(t, "android-cmake", "cmake/3.22.1"); r.recommended(it) {
		t.Error("cmake 3.22.1 must be kept")
	}
	if it := r.at(t, "android-cmake", "cmake/3.18.1"); !r.recommended(it) {
		t.Error("cmake 3.18.1 should be recommended")
	}
	if it := r.at(t, "android-sources", "sources/android-33"); it.Risk != core.RiskSafe {
		t.Errorf("old sources should be safe: %+v", it)
	}
	// Legacy bits.
	for _, k := range []string{"android-haxm", "android-sdk-legacy-tools", "android-cmdline-tools-old"} {
		if its := r.byKind(k); len(its) != 1 || !r.recommended(its[0]) {
			t.Errorf("%s: want 1 recommended item, got %d", k, len(its))
		}
	}
	if its := r.byKind("android-sdk-temp"); len(its) != 1 || its[0].Risk != core.RiskSafe {
		t.Errorf("sdk temp: %v", its)
	}
	// Core tooling is never proposed.
	for _, it := range r.items {
		for _, p := range it.Targets() {
			for _, keep := range []string{"platform-tools", "licenses", "cmdline-tools/latest", "emulator"} {
				if strings.HasSuffix(p, "/"+keep) {
					t.Errorf("%s proposes %s", it.ID, p)
				}
			}
		}
	}
	// HAXM is only dead weight on Apple Silicon.
	r2 := f.scan(opts{arm64: false})
	if len(r2.byKind("android-haxm")) != 0 {
		t.Error("HAXM must not be proposed on Intel")
	}
	if it := r2.at(t, "android-system-image", "android-30/google_apis/x86_64"); it.Warn != "" || it.Selectable || it.Meta["used_by"] != "Old_x86" {
		t.Errorf("x86 image used by an AVD on Intel is kept: %+v", it)
	}
}

func TestAVDs(t *testing.T) {
	f := machine(t)
	r := f.scan(opts{arm64: true})

	px := r.at(t, "android-avd", "Pixel_8.avd")
	if px.Risk != core.RiskCaution || px.Method != core.MethodDelete || len(px.Paths) != 2 ||
		!strings.HasSuffix(px.Paths[1], "Pixel_8.ini") || r.recommended(px) {
		t.Errorf("Pixel_8 AVD: %+v", px)
	}
	if px.Meta["api"] != "34" || px.Meta["abi"] != "arm64-v8a" || px.Meta["device"] != "pixel_8" {
		t.Errorf("Pixel_8 meta: %v", px.Meta)
	}
	if want := f.now.Add(-3 * 24 * time.Hour); px.LastUsed.Sub(want).Abs() > time.Minute {
		t.Errorf("Pixel_8 last used %v, want ~%v (hardware-qemu.ini)", px.LastUsed, want)
	}
	if len(px.ProcessGuard) == 0 || px.ProcessGuard[0] != "qemu-system-aarch64" {
		t.Errorf("AVD process guard: %v", px.ProcessGuard)
	}
	if !strings.Contains(px.Name, "Pixel 8") || px.Size == 0 {
		t.Errorf("Pixel_8 name/size: %q %d", px.Name, px.Size)
	}

	snap := r.at(t, "android-avd-snapshots", "Pixel_8.avd/snapshots")
	if snap.Risk != core.RiskModerate || snap.Path == "" || !snap.CanClean() || snap.Warn != "" {
		t.Errorf("Pixel_8 snapshots: %+v", snap)
	}
	named := r.at(t, "android-avd-snapshots", "Named.avd/snapshots")
	if named.Risk != core.RiskCaution || !strings.Contains(named.Warn, "before_login") {
		t.Errorf("named snapshots: %+v", named)
	}

	x86 := r.at(t, "android-avd", "Old_x86.avd")
	if x86.Risk != core.RiskModerate || !r.recommended(x86) || !strings.Contains(x86.Note, "Apple Silicon") {
		t.Errorf("x86 AVD on arm64: %+v", x86)
	}
	orphan := r.at(t, "android-avd-orphan-ini", "Gone.ini")
	if orphan.Path == "" || !r.recommended(orphan) {
		t.Errorf("orphan ini: %+v", orphan)
	}
	stray := r.at(t, "android-avd", "Stray.avd")
	if len(stray.Paths) != 1 || !strings.Contains(stray.Warn, "Stray.ini") || r.recommended(stray) {
		t.Errorf("AVD dir without ini: %+v", stray)
	}
	namedAVD := r.at(t, "android-avd", "Named.avd")
	if !strings.Contains(namedAVD.Warn, "not installed") {
		t.Errorf("AVD whose image is missing should warn: %+v", namedAVD)
	}
	r.checkSafety(t)
}

func TestRunningEmulator(t *testing.T) {
	f := machine(t)
	r := f.scan(opts{arm64: true, running: []string{"qemu-system-aarch64"},
		runner: map[string]string{"pgrep -lf qemu-system": "4242 /sdk/emulator/qemu/darwin-aarch64/qemu-system-aarch64 -netdelay none -avd Pixel_8 -no-window\n"}})
	px := r.at(t, "android-avd", "Pixel_8.avd")
	if !strings.Contains(px.Warn, "running") || r.recommended(px) || px.Meta["api"] != "34" {
		t.Errorf("running AVD: %+v", px)
	}
	if snap := r.at(t, "android-avd-snapshots", "Pixel_8.avd/snapshots"); !strings.Contains(snap.Warn, "running") || r.recommended(snap) {
		t.Errorf("running AVD snapshots: %+v", snap)
	}
	// Another AVD is not affected.
	if x86 := r.at(t, "android-avd", "Old_x86.avd"); strings.Contains(x86.Warn, "running") {
		t.Errorf("Old_x86 is not running: %+v", x86)
	}
}

func TestGradle(t *testing.T) {
	f := machine(t)
	r := f.scan(opts{arm64: true})
	r.checkSafety(t)

	used := r.at(t, "android-gradle-dist", "gradle-8.10.2-all")
	if r.recommended(used) || !strings.Contains(used.Meta["used_by"], "src/app1") || used.Risk != core.RiskModerate {
		t.Errorf("used dist: %+v", used)
	}
	// Daemon log mtime is the last-use signal.
	if used.LastUsed.Before(f.now.Add(-time.Hour)) {
		t.Errorf("dist last used should come from daemon logs, got %v", used.LastUsed)
	}
	if wt := r.at(t, "android-gradle-dist", "gradle-8.13-bin"); r.recommended(wt) {
		t.Error("gradle 8.13 is used by a .claude worktree and must not be recommended")
	}
	unused := r.at(t, "android-gradle-dist", "gradle-7.5-all")
	if !r.recommended(unused) || unused.Meta["used_by"] != "" {
		t.Errorf("gradle 7.5 is only referenced inside node_modules: %+v", unused)
	}
	partial := r.at(t, "android-gradle-dist", "gradle-8.0-bin")
	if partial.Risk != core.RiskSafe || !r.recommended(partial) || !strings.Contains(partial.Name, "interrupted") {
		t.Errorf("interrupted dist: %+v", partial)
	}

	if it := r.at(t, "android-gradle-version-cache", "caches/8.10.2"); r.recommended(it) {
		t.Error("caches/8.10.2 is used")
	}
	if it := r.at(t, "android-gradle-version-cache", "caches/7.5"); !r.recommended(it) {
		t.Errorf("caches/7.5 unused: %+v", it)
	}
	if it := r.at(t, "android-gradle-transforms", "transforms-3"); it.Risk != core.RiskSafe || !r.recommended(it) {
		t.Errorf("old transforms gen: %+v", it)
	}
	if it := r.at(t, "android-gradle-transforms", "transforms-4"); it.Risk != core.RiskModerate {
		t.Errorf("current transforms gen: %+v", it)
	}
	if it := r.at(t, "android-gradle-modules", "modules-2"); it.Risk != core.RiskModerate || it.Warn != "" {
		t.Errorf("modules-2: %+v", it)
	}
	other := r.byKind("android-gradle-caches-other")
	if len(other) != 1 || len(other[0].Paths) != 1 || !strings.HasSuffix(other[0].Paths[0], "caches/kotlin-dsl") {
		t.Errorf("other caches: %+v", other)
	}
	for _, it := range r.items {
		for _, p := range it.Targets() {
			if strings.Contains(p, "journal-1") || strings.HasSuffix(p, "gradle.properties") {
				t.Errorf("%s must never be proposed (%s)", p, it.ID)
			}
		}
	}
	logs := r.at(t, "android-gradle-daemon-logs", "daemon/8.10.2")
	if logs.Path == "" || logs.Risk != core.RiskSafe {
		t.Errorf("daemon logs: %+v", logs)
	}
	tmp := r.byKind("android-gradle-tmp")
	if len(tmp) != 1 || len(tmp[0].Paths) != 1 || !strings.HasSuffix(tmp[0].Paths[0], "gradle_download123.bin") || !r.recommended(tmp[0]) {
		t.Errorf(".tmp: %+v", tmp)
	}
	if it := r.byKind("android-gradle-jdk"); len(it) != 1 || it[0].Risk != core.RiskModerate || r.recommended(it[0]) {
		t.Errorf("toolchain jdk: %+v", it)
	}
	if it := r.byKind("android-gradle-misc"); len(it) != 1 || len(it[0].Paths) != 2 {
		t.Errorf("misc: %+v", it)
	}
}

func TestGradleDaemonRunning(t *testing.T) {
	f := machine(t)
	r := f.scan(opts{arm64: true, runner: map[string]string{
		"pgrep -lf GradleDaemon": "777 /Library/Java/zulu-17.jdk/bin/java -cp " + f.p(".gradle/wrapper/dists/gradle-8.10.2-all/abc/gradle-8.10.2/lib/gradle-daemon-main-8.10.2.jar") + " org.gradle.launcher.daemon.bootstrap.GradleDaemon 8.10.2\n",
	}})
	if it := r.at(t, "android-gradle-dist", "gradle-8.10.2-all"); it.Warn != daemonWarn {
		t.Errorf("running version dist should warn: %+v", it)
	}
	if it := r.at(t, "android-gradle-dist", "gradle-7.5-all"); it.Warn != "" || !r.recommended(it) {
		t.Errorf("other versions are not affected: %+v", it)
	}
	if it := r.at(t, "android-gradle-modules", "modules-2"); it.Warn != daemonWarn || r.recommended(it) {
		t.Errorf("modules-2 with a daemon: %+v", it)
	}
	if it := r.at(t, "android-gradle-transforms", "transforms-3"); r.recommended(it) {
		t.Errorf("caches must not be recommended while a daemon runs: %+v", it)
	}
	// Only the old log of the running version, registry and fresh log kept.
	logs := r.at(t, "android-gradle-daemon-logs", "daemon/8.10.2")
	if logs.Path != "" || len(logs.Paths) != 1 || !strings.HasSuffix(logs.Paths[0], "daemon-1.out.log") {
		t.Errorf("daemon logs while running: %+v", logs)
	}
}

func TestNoProjectRootsNoGuessing(t *testing.T) {
	f := machine(t)
	r := f.scan(opts{arm64: true, roots: []string{}})
	if it := r.at(t, "android-gradle-dist", "gradle-7.5-all"); it.Recommended || it.Meta["note"] == "" {
		t.Errorf("without project roots, unused cannot be asserted: %+v", it)
	}
	if it := r.at(t, "android-ndk", "ndk/25.1.8937393"); it.Recommended {
		t.Errorf("without project roots, NDK usage is unknown: %+v", it)
	}
	// Interrupted downloads and orphaned temp files stay recommended.
	if it := r.at(t, "android-gradle-dist", "gradle-8.0-bin"); !r.recommended(it) {
		t.Error("interrupted download should stay recommended")
	}
}

func TestExternalAndDanglingLocations(t *testing.T) {
	t.Run("dangling symlinks to an unmounted volume", func(t *testing.T) {
		f := newFx(t)
		f.link("Library/Android/sdk", "/Volumes/LU_TEST_NOT_MOUNTED/Android/sdk")
		f.link(".android/avd", "/Volumes/LU_TEST_NOT_MOUNTED/Android/avd")
		f.file(".android/debug.keystore", "KEY", 0)
		r := f.scan(opts{arm64: true})
		sdk := r.at(t, "android-sdk", "Library/Android/sdk")
		if sdk.Method != core.MethodReport || sdk.CanClean() || !strings.Contains(sdk.Warn, "not mounted") {
			t.Errorf("sdk: %+v", sdk)
		}
		home := r.at(t, "android-avd-home", ".android/avd")
		if home.CanClean() || !strings.Contains(home.Warn, "LU_TEST_NOT_MOUNTED") {
			t.Errorf("avd home: %+v", home)
		}
		for id, it := range r.items {
			if it.CanClean() {
				t.Errorf("nothing may be cleanable here, got %s", id)
			}
		}
		// The links are untouched and still there.
		for _, l := range []string{"Library/Android/sdk", ".android/avd"} {
			if fi, err := os.Lstat(f.p(l)); err != nil || fi.Mode()&os.ModeSymlink == 0 {
				t.Errorf("%s link changed", l)
			}
		}
	})

	t.Run("dangling symlink outside /Volumes", func(t *testing.T) {
		f := newFx(t)
		f.link(".android/avd", filepath.Join(f.tmp, "gone", "avd"))
		r := f.scan(opts{})
		home := r.at(t, "android-avd-home", ".android/avd")
		if home.CanClean() || !strings.Contains(home.Warn, "dangling") {
			t.Errorf("avd home: %+v", home)
		}
	})

	t.Run("SDK and AVDs on another volume", func(t *testing.T) {
		f := newFx(t)
		ext := t.TempDir()
		extReal, _ := filepath.EvalSymlinks(ext)
		f.file(filepath.Join(ext, "sdk/platform-tools/adb"), "adb", 0)
		f.blob(filepath.Join(ext, "sdk/system-images/android-34/default/arm64-v8a/system.img"), 1000, 0)
		f.blob(filepath.Join(ext, "sdk/ndk/25.1.8937393/x"), 1000, 0)
		f.file(filepath.Join(ext, "avd/Ext.ini"), "path="+filepath.Join(ext, "avd/Ext.avd")+"\n", 0)
		f.file(filepath.Join(ext, "avd/Ext.avd/config.ini"), "image.sysdir.1=system-images/android-34/default/arm64-v8a/\n", 0)
		f.link("Library/Android/sdk", filepath.Join(ext, "sdk"))
		f.link(".android/avd", filepath.Join(ext, "avd"))
		devOf := func(p string) (uint64, error) {
			if fsx.Within(p, extReal) || fsx.Within(p, ext) {
				return 99, nil
			}
			return statDev(p)
		}
		r := f.scan(opts{arm64: true, devOf: devOf})
		sdk := r.at(t, "android-sdk", "Library/Android/sdk")
		if sdk.CanClean() || sdk.Warn != "on external volume — no internal gain" {
			t.Errorf("external sdk: %+v", sdk)
		}
		home := r.at(t, "android-avd-home", ".android/avd")
		if home.CanClean() || home.Warn != "on external volume — no internal gain" || home.Meta["avds"] != "Ext" {
			t.Errorf("external avd home: %+v", home)
		}
		for id, it := range r.items {
			if it.CanClean() {
				t.Errorf("nothing on the external volume may be cleanable, got %s", id)
			}
		}
	})

	t.Run("internal SDK but unreachable AVD folder", func(t *testing.T) {
		f := newFx(t)
		f.file("Library/Android/sdk/platform-tools/adb", "adb", 0)
		f.blob(img33+"/system.img", 1000, 0)
		f.link(".android/avd", "/Volumes/LU_TEST_NOT_MOUNTED/avd")
		r := f.scan(opts{arm64: true})
		img := r.at(t, "android-system-image", "android-33/google_apis_playstore/arm64-v8a")
		if r.recommended(img) || !strings.Contains(img.Warn, "not reachable") || !img.CanClean() {
			t.Errorf("image usage unknown: %+v", img)
		}
	})

	t.Run("SDK outside home on the same volume", func(t *testing.T) {
		f := newFx(t)
		out := t.TempDir()
		f.file(filepath.Join(out, "sdk/platform-tools/adb"), "adb", 0)
		f.blob(filepath.Join(out, "sdk/ndk/25.1.8937393/x"), 5000, 0)
		r := f.scan(opts{env: map[string]string{"ANDROID_HOME": filepath.Join(out, "sdk")}})
		sdk := r.at(t, "android-sdk", "sdk")
		if sdk.CanClean() || !strings.Contains(sdk.Warn, "outside your home") || sdk.Size == 0 {
			t.Errorf("outside sdk: %+v", sdk)
		}
		if len(r.byKind("android-ndk")) != 0 {
			t.Error("packages of an SDK outside home are not proposed")
		}
	})

	t.Run("symlinked package inside an internal SDK", func(t *testing.T) {
		f := newFx(t)
		f.file("Library/Android/sdk/platform-tools/adb", "adb", 0)
		f.link("Library/Android/sdk/ndk/25.1.8937393", "/Volumes/LU_TEST_NOT_MOUNTED/ndk/25.1.8937393")
		f.blob("Library/Android/sdk/ndk/26.1.10909125/x", 100, 0)
		r := f.scan(opts{})
		// dirNames skips symlinks: the link is neither proposed nor followed.
		r.none(t, "android-ndk", "25.1.8937393")
		r.checkSafety(t)
	})
}

func TestSDKDetection(t *testing.T) {
	f := newFx(t)
	custom := f.p("dev/android-sdk")
	f.file("dev/android-sdk/platform-tools/adb", "adb", 0)
	f.blob("dev/android-sdk/ndk/25.1.8937393/x", 100, 0)
	// Exported only in the shell profile: GUI apps and agents do not see it.
	f.file(".zshrc", "# android\nexport ANDROID_HOME=\"$HOME/dev/android-sdk\"\nexport PATH=$PATH:$ANDROID_HOME/emulator\n", 0)
	r := f.scan(opts{})
	if it := r.at(t, "android-ndk", "ndk/25.1.8937393"); !strings.HasPrefix(it.Path, custom) {
		t.Errorf("ndk found at %s", it.Path)
	}

	// Android Studio settings + local.properties + duplicates by real path.
	f2 := newFx(t)
	f2.file("sdk2/platform-tools/adb", "adb", 0)
	f2.blob("sdk2/build-tools/30.0.3/aapt2", 100, 0)
	f2.blob("sdk2/build-tools/34.0.0/aapt2", 100, 0)
	f2.file("Library/Application Support/Google/AndroidStudio2025.1.1/options/android.sdk.path.xml",
		`<application><component name="AndroidSdkPathStore"><option name="androidSdkAbsolutePath" value="$USER_HOME$/sdk2" /></component></application>`, 0)
	f2.link("Library/Android/sdk", f2.p("sdk2"))
	f2.file("src/p/android/local.properties", "sdk.dir="+strings.ReplaceAll(f2.p("sdk2"), ":", "\\:")+"\n", 0)
	r2 := f2.scan(opts{})
	if n := len(r2.byKind("android-build-tools")); n != 2 {
		t.Errorf("same SDK reached 3 ways must be scanned once: %d build-tools items", n)
	}
}

func TestStudio(t *testing.T) {
	f := newFx(t)
	f.file("Applications/Android Studio.app/Contents/Resources/product-info.json", `{"name":"Android Studio","dataDirectoryName":"AndroidStudio2025.1.1"}`, 0)
	f.blob("Library/Caches/Google/AndroidStudio2025.1.1/index/x", 1000, 0)
	f.blob("Library/Caches/Google/AndroidStudio2023.1/index/x", 1000, 0)
	f.blob("Library/Logs/Google/AndroidStudio2025.1.1/idea.log", 1000, 0)
	f.blob("Library/Application Support/Google/AndroidStudio2025.1.1/options/other.xml", 100, 0)
	f.blob("Library/Application Support/Google/AndroidStudio2023.1/options/other.xml", 100, 400*24*time.Hour)

	r := f.scan(opts{running: []string{"studio"}})
	cur := r.at(t, "android-studio-cache", "AndroidStudio2025.1.1")
	if cur.Risk != core.RiskModerate || len(cur.ProcessGuard) == 0 || !strings.Contains(cur.Warn, "running") || r.recommended(cur) {
		t.Errorf("current caches: %+v", cur)
	}
	old := r.at(t, "android-studio-cache", "AndroidStudio2023.1")
	if old.Risk != core.RiskSafe || !r.recommended(old) {
		t.Errorf("old caches: %+v", old)
	}
	if logs := r.at(t, "android-studio-logs", "AndroidStudio2025.1.1"); !strings.Contains(logs.Warn, "running") {
		t.Errorf("logs while running: %+v", logs)
	}
	r.none(t, "android-studio-config", "AndroidStudio2025.1.1")
	cfg := r.at(t, "android-studio-config", "AndroidStudio2023.1")
	if cfg.Risk != core.RiskModerate || !r.recommended(cfg) {
		t.Errorf("old settings: %+v", cfg)
	}

	// No bundle found: the newest settings dir is treated as current.
	f2 := newFx(t)
	f2.blob("Library/Application Support/Google/AndroidStudio2024.2/options/a.xml", 100, 0)
	f2.blob("Library/Application Support/Google/AndroidStudio2022.3/options/a.xml", 100, 800*24*time.Hour)
	os.Chtimes(f2.p("Library/Application Support/Google/AndroidStudio2022.3"), f2.now.Add(-800*24*time.Hour), f2.now.Add(-800*24*time.Hour))
	r2 := f2.scan(opts{})
	r2.none(t, "android-studio-config", "AndroidStudio2024.2")
	r2.at(t, "android-studio-config", "AndroidStudio2022.3")
}

func TestUserCacheKeepsIdentity(t *testing.T) {
	f := machine(t)
	r := f.scan(opts{})
	uc := r.byKind("android-user-cache")
	if len(uc) != 1 || len(uc[0].Paths) != 2 || uc[0].Risk != core.RiskSafe {
		t.Fatalf("user cache: %+v", uc)
	}
	for _, p := range uc[0].Paths {
		if !strings.Contains(p, "/.android/cache/") && !strings.Contains(p, "/.android/breakpad/") {
			t.Errorf("unexpected user cache path %s", p)
		}
	}
	for _, it := range r.items {
		for _, p := range it.Targets() {
			b := filepath.Base(p)
			if b == "debug.keystore" || b == "adbkey" || p == f.p(".android") || p == f.p(".android/avd") {
				t.Errorf("%s proposes %s", it.ID, p)
			}
		}
	}
}

func TestJDKs(t *testing.T) {
	f := newFx(t)
	rel := func(ver, impl, arch string) string {
		return "JAVA_VERSION=\"" + ver + "\"\nIMPLEMENTOR=\"" + impl + "\"\nOS_ARCH=\"" + arch + "\"\n"
	}
	f.file("SystemJVM/zulu-17.jdk/Contents/Home/release", rel("17.0.10", "Azul Systems, Inc.", "aarch64"), 0)
	f.blob("SystemJVM/zulu-17.jdk/Contents/Home/lib/modules", 3000, 0)
	f.file("SystemJVM/temurin-8.jdk/Contents/Home/release", rel("1.8.0_442", "Eclipse Adoptium", "x86_64"), 0)
	f.file("Library/Java/JavaVirtualMachines/corretto-21/Contents/Home/release", rel("21.0.2", "Amazon.com Inc.", "aarch64"), 0)
	f.blob("Library/Java/JavaVirtualMachines/corretto-21/Contents/Home/lib/modules", 3000, 0)
	f.file("Library/Java/JavaVirtualMachines/jbr-17/Contents/Home/release", rel("17.0.9", "JetBrains s.r.o.", "aarch64"), 0)
	f.blob("Library/Java/JavaVirtualMachines/jbr-17/Contents/Home/lib/modules", 3000, 0)
	r := f.scan(opts{arm64: true, env: map[string]string{
		"JAVA_HOME": f.p("Library/Java/JavaVirtualMachines/jbr-17/Contents/Home"),
	}})
	sys := r.at(t, "android-jdk-system", "zulu-17.jdk")
	if sys.Method != core.MethodReport || sys.CanClean() || !strings.Contains(sys.Name, "Zulu") || sys.Size == 0 {
		t.Errorf("system jdk: %+v", sys)
	}
	if t8 := r.at(t, "android-jdk-system", "temurin-8.jdk"); !strings.Contains(t8.Warn, "Rosetta") || t8.Meta["note"] == "" {
		t.Errorf("x86 JDK 8: %+v", t8)
	}
	if c := r.at(t, "android-jdk", "corretto-21"); !c.CanClean() || r.recommended(c) || c.Meta["installed_by"] != "IDE download" {
		t.Errorf("unused home JDK: %+v", c)
	}
	if j := r.at(t, "android-jdk", "jbr-17"); j.CanClean() || j.Meta["in_use"] != "JAVA_HOME" {
		t.Errorf("JAVA_HOME jdk: %+v", j)
	}
	r.checkSafety(t)
}

func TestIDStability(t *testing.T) {
	f := machine(t)
	a := f.scan(opts{arm64: true})
	b := f.scan(opts{arm64: true})
	if len(a.items) != len(b.items) || len(a.items) < 30 {
		t.Fatalf("item count %d vs %d", len(a.items), len(b.items))
	}
	for id, it := range a.items {
		if b.items[id] == nil {
			t.Errorf("id %s not stable across scans", id)
		}
		if want := "android:" + it.Kind + ":"; !strings.HasPrefix(id, want) {
			t.Errorf("id %s should start with %s", id, want)
		}
	}
	if it := a.items["android:android-ndk:"+f.p("Library/Android/sdk/ndk/25.1.8937393")]; it == nil {
		t.Error("NDK id format changed")
	}
	if it := a.items["android:android-avd:"+f.p(".android/avd/Pixel_8.avd")]; it == nil {
		t.Error("AVD id format changed")
	}
}

func TestCancelledScan(t *testing.T) {
	f := machine(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	env := &core.Env{Home: f.home, TmpDir: f.tmp, Now: f.now, Roots: []string{f.p("src")}, Runner: fakeRunner{}, Logf: t.Logf}
	err := f.provider(opts{}).Scan(ctx, env, func(*core.Item) {})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("want context.Canceled, got %v", err)
	}
}

// ------------------------------------------------------------------ parsers

func TestParseShellExports(t *testing.T) {
	home := "/Users/me"
	vars := parseShellExports(home, `
export JAVA_HOME=/Library/Java/JavaVirtualMachines/zulu-17.jdk/Contents/Home
export ANDROID_HOME="/Volumes/SSD/Android/sdk"
export ANDROID_AVD_HOME="$ANDROID_HOME/../avd"   # comment
ANDROID_SDK_ROOT=${HOME}/Library/Android/sdk
export GRADLE_USER_HOME=~/gradle-home
export ANDROID_USER_HOME=$(brew --prefix)/android
export ANDROID_EMULATOR_HOME=$UNKNOWN/emu
export PATH=$PATH:$ANDROID_HOME/emulator
`, `export ANDROID_HOME='/opt/sdk'`)
	want := map[string][]string{
		"JAVA_HOME":        {"/Library/Java/JavaVirtualMachines/zulu-17.jdk/Contents/Home"},
		"ANDROID_HOME":     {"/Volumes/SSD/Android/sdk", "/opt/sdk"},
		"ANDROID_AVD_HOME": {"/Volumes/SSD/Android/avd"},
		"ANDROID_SDK_ROOT": {"/Users/me/Library/Android/sdk"},
		"GRADLE_USER_HOME": {"/Users/me/gradle-home"},
	}
	for k, v := range want {
		if strings.Join(vars[k], "|") != strings.Join(v, "|") {
			t.Errorf("%s = %v, want %v", k, vars[k], v)
		}
	}
	for _, k := range []string{"ANDROID_USER_HOME", "ANDROID_EMULATOR_HOME", "PATH"} {
		if len(vars[k]) != 0 {
			t.Errorf("%s must be ignored, got %v", k, vars[k])
		}
	}
}

func TestParsers(t *testing.T) {
	tests := []struct{ in, want string }{
		{"distributionUrl=https\\://services.gradle.org/distributions/gradle-9.3.1-bin.zip\n", "gradle-9.3.1-bin"},
		{"distributionBase=X\ndistributionUrl=https\\://mirror.corp/gradle/gradle-8.10.2-all.zip\n", "gradle-8.10.2-all"},
		{"distributionUrl=https\\://services.gradle.org/distributions-snapshots/gradle-8.12-rc-1-bin.zip", "gradle-8.12-rc-1-bin"},
		{"distributionUrl=file\\:/tmp/custom.zip", ""},
		{"# nothing", ""},
	}
	for _, tt := range tests {
		if got := wrapperDist(tt.in); got != tt.want {
			t.Errorf("wrapperDist(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	props := parseProperties("# c\nsdk.dir=/Users/me/Library/Android/sdk\nndk.dir=C\\:\\\\ndk\n org.gradle.java.home = /jdk \n")
	if props["sdk.dir"] != "/Users/me/Library/Android/sdk" || props["ndk.dir"] != `C:\ndk` || props["org.gradle.java.home"] != "/jdk" {
		t.Errorf("parseProperties = %v", props)
	}
	for v, want := range map[string]string{"0.76.3": "27.1.12297006", "^0.74.5": "26.1.10909125", "~0.73.0": "25.1.8937393",
		"0.72.4": "23.1.7779620", "0.86.0": "27.1.12297006", "0.60.0": "", "latest": ""} {
		if got := rnDefaultNDK(v); got != want {
			t.Errorf("rnDefaultNDK(%s) = %q, want %q", v, got, want)
		}
	}
	if compareVersions("27.1.12297006", "26.3.11579264") <= 0 || compareVersions("android-9", "android-34") >= 0 ||
		compareVersions("3.22.1", "3.31.6") >= 0 {
		t.Error("compareVersions")
	}
	if n := newest([]string{"33.0.1", "36.0.0", "34.0.0"}, 1); !n["36.0.0"] || len(n) != 1 {
		t.Errorf("newest = %v", n)
	}
	if normImage(`system-images\android-34\google_apis\arm64-v8a\`) != "system-images/android-34/google_apis/arm64-v8a" {
		t.Error("normImage backslashes")
	}
	if normImage("/sdk/system-images/android-34/default/x86_64/") != "system-images/android-34/default/x86_64" {
		t.Error("normImage absolute")
	}
}

func TestProjectVersions(t *testing.T) {
	f := newFx(t)
	f.file("src/a/android/build.gradle.kts", "android {\n  compileSdk = 35\n  ndkVersion = \"26.3.11579264\"\n  externalNativeBuild {\n    cmake {\n      path = file(\"CMakeLists.txt\")\n      version = \"3.31.6\"\n    }\n  }\n}\n", 0)
	f.file("src/b/app.json", `{"expo":{"plugins":[["expo-build-properties",{"android":{"compileSdkVersion":36,"buildToolsVersion":"36.0.0","ndkVersion":"27.0.12077973"}}]]}}`, 0)
	f.file("src/b/android/gradle.properties", "android.ndkVersion=28.0.13004108\norg.gradle.java.home=/jdk17\n", 0)
	f.file("src/c/package.json", `{"dependencies":{"react-native":"0.74.5"}}`, 0)
	f.file("src/c/android/settings.gradle", "", 0)
	f.file("src/deep/1/2/3/4/5/6/7/8/9/android/gradle/wrapper/gradle-wrapper.properties", "distributionUrl=https\\://x/gradle-6.0-bin.zip", 0)
	env := &core.Env{Home: f.home, TmpDir: f.tmp, Now: f.now, Roots: []string{f.p("src")}, MaxDepth: 8, Runner: fakeRunner{}, Logf: t.Logf}
	p := f.provider(opts{})
	p.defaults(env)
	s := newScan(context.Background(), p, env, func(*core.Item) {})
	pi := s.scanProjects()
	if !pi.known() {
		t.Fatal("walk should be complete")
	}
	for _, v := range []string{"26.3.11579264", "27.0.12077973", "28.0.13004108", "26.1.10909125"} {
		if pi.ndk[v] == nil {
			t.Errorf("ndk %s not detected (%v)", v, pi.ndk)
		}
	}
	if pi.compileSdk["35"] == nil || pi.compileSdk["36"] == nil || pi.buildTools["36.0.0"] == nil || pi.cmake["3.31.6"] == nil {
		t.Errorf("versions: sdk=%v bt=%v cmake=%v", pi.compileSdk, pi.buildTools, pi.cmake)
	}
	if !pi.javaHomes["/jdk17"] {
		t.Errorf("java homes %v", pi.javaHomes)
	}
	if len(pi.wrappers) != 0 {
		t.Errorf("wrapper beyond MaxDepth must not be read: %v", pi.wrappers)
	}
}

func TestGradleDaemonVersions(t *testing.T) {
	f := machine(t)
	tests := []struct {
		name, line string
		warn75     bool
	}{
		{"homebrew launcher jar", "12 /opt/homebrew/opt/openjdk/bin/java -cp /opt/homebrew/Cellar/gradle/8.12.1/libexec/lib/gradle-launcher-8.12.1.jar org.gradle.launcher.daemon.bootstrap.GradleDaemon 8.12.1", false},
		{"unknown version: every dist warns", "12 java -cp /somewhere/custom.jar org.gradle.launcher.daemon.bootstrap.GradleDaemon", true},
		{"old wrapper daemon", "12 java -cp " + f.p(".gradle/wrapper/dists/gradle-7.5-all/ghi/gradle-7.5/lib/gradle-launcher-7.5.jar") + " org.gradle.launcher.daemon.bootstrap.GradleDaemon 7.5", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := f.scan(opts{arm64: true, runner: map[string]string{"pgrep -lf GradleDaemon": tt.line + "\n"}})
			it := r.at(t, "android-gradle-dist", "gradle-7.5-all")
			if (it.Warn == daemonWarn) != tt.warn75 || (tt.warn75 && r.recommended(it)) {
				t.Errorf("gradle 7.5 dist warn=%q recommended=%v", it.Warn, r.recommended(it))
			}
			if tmp := r.byKind("android-gradle-tmp"); len(tmp) != 1 || !r.recommended(tmp[0]) {
				t.Error("files older than a day in .tmp stay recommended while a daemon runs")
			}
		})
	}
}

func TestAVDHomeBehindInternalSymlink(t *testing.T) {
	f := newFx(t)
	real := f.p("Emulators/avd")
	f.file("Emulators/avd/Pixel.ini", "path="+real+"/Pixel.avd\n", 0)
	f.file("Emulators/avd/Pixel.avd/config.ini", "abi.type=arm64-v8a\nimage.sysdir.1=system-images/android-35/google_apis/arm64-v8a/\n", 0)
	f.blob("Emulators/avd/Pixel.avd/userdata-qemu.img", 2000, 0)
	f.link(".android/avd", real)
	// ini pointing to an unmounted volume: not an orphan, just unreachable.
	f.file("Emulators/avd/Ext.ini", "path=/Volumes/LU_TEST_NOT_MOUNTED/avd/Ext.avd\n", 0)
	r := f.scan(opts{arm64: true})
	px := r.at(t, "android-avd", "Pixel.avd")
	if !px.CanClean() || px.Size == 0 {
		t.Errorf("AVD in an internal home reached through a symlink: %+v", px)
	}
	if n := len(r.byKind("android-avd")); n != 2 {
		t.Errorf("AVD listed once per real folder, got %d items", n)
	}
	ext := r.at(t, "android-avd", "Ext.avd")
	if ext.CanClean() || !strings.Contains(ext.Warn, "not mounted") {
		t.Errorf("AVD on an unmounted volume: %+v", ext)
	}
	if len(r.byKind("android-avd-orphan-ini")) != 0 {
		t.Error("an ini pointing to an unmounted volume is not an orphan")
	}
	r.checkSafety(t)
}

func TestLocalPropertiesOfAnotherMachine(t *testing.T) {
	f := newFx(t)
	f.file("src/p/android/local.properties", "sdk.dir=/Volumes/LU_TEST_NOT_MOUNTED/sdk\n", 0)
	f.file("src/q/android/local.properties", "sdk.dir=/Users/somebody-else/Library/Android/sdk\n", 0)
	r := f.scan(opts{})
	if n := len(r.byKind("android-sdk")); n != 0 {
		t.Errorf("SDK paths only known from local.properties that do not exist must be ignored, got %d", n)
	}
}
