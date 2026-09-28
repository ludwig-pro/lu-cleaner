package system

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
)

// ------------------------------------------------------------------ Trash

func TestTrash(t *testing.T) {
	f := newFixture(t, "trash")
	f.file(".Trash/report.pdf", 50_000, 0)
	f.file(".Trash/old-project/src/main.go", 10_000, 0)
	f.dir(".Trash/old-project/.git", 0) // a trashed repository must still be removable
	f.file(".Trash/.DS_Store", 100, 0)
	items := f.scan()
	it := one(t, items, "trash")
	if it.ID != "system:trash:"+f.abs(".Trash") {
		t.Errorf("unstable id %s", it.ID)
	}
	if !sameStrings(bases(it), []string{"old-project", "report.pdf"}) {
		t.Errorf("targets = %v", bases(it))
	}
	if !it.AllowGitRepo || it.Risk != core.RiskModerate || it.Method != core.MethodDelete || it.Recommended {
		t.Errorf("trash item = %+v", it)
	}
	if it.Size < 60_000 {
		t.Errorf("size = %d", it.Size)
	}
	if it.LastUsed.IsZero() {
		t.Errorf("LastUsed should be the time items were trashed")
	}
}

func TestTrashEmptyOrUnreadable(t *testing.T) {
	f := newFixture(t, "trash")
	f.dir(".Trash", 0)
	f.file(".Trash/.DS_Store", 10, 0)
	if items := f.scan(); len(items) != 0 {
		t.Fatalf("empty trash: %v", items)
	}
	if os.Getuid() == 0 {
		t.Skip("root can read anything")
	}
	os.Chmod(f.abs(".Trash"), 0o000)
	defer os.Chmod(f.abs(".Trash"), 0o700)
	items := f.scan()
	it := one(t, items, "trash-finder")
	if it.Method != core.MethodCommand || it.Risk != core.RiskCaution || it.Warn == "" || !strings.Contains(strings.Join(it.Command, " "), "empty trash") {
		t.Errorf("unreadable trash item = %+v", it)
	}
	if core.Recommend(it, f.now, 14*day) {
		t.Errorf("emptying an unmeasured trash must never be preselected")
	}
}

// ------------------------------------------------------------------ Downloads

func TestDownloads(t *testing.T) {
	f := newFixture(t, "downloads")
	f.file("Downloads/Old App.dmg", 40_000, 40*day)
	f.file("Downloads/fresh.dmg", 40_000, 5*day)                      // too recent
	f.file("Downloads/opened.pkg", 40_000, 90*day)                    // old mtime but opened recently
	f.file("Downloads/sorted/backup.tar.gz", 30_000, 60*day)          // in a sub-folder
	f.file("Downloads/build.apk", 20_000, 31*day)                     //
	f.file("Downloads/notes.txt", 20_000, 400*day)                    // not an installer
	f.file("Downloads/Tool.app/Contents/payload.zip", 20_000, 90*day) // inside an app bundle
	f.file("Downloads/a/b/c/deep.dmg", 20_000, 90*day)                // too deep
	f.file("Downloads/.hidden.zip", 20_000, 90*day)                   // hidden
	sparse := f.abs("Downloads/icloud.zip")                           // dataless: 0 allocated block
	fh, _ := os.Create(sparse)
	fh.Truncate(10 << 20)
	fh.Close()
	f.age("Downloads/icloud.zip", 90*day)
	os.Symlink(f.abs("Downloads/Old App.dmg"), f.abs("Downloads/link.dmg"))
	opened := f.abs("Downloads/opened.pkg")
	f.p.lastUsed = func(p string) (time.Time, bool) {
		if p == opened {
			return f.now.Add(-2 * day), true
		}
		return time.Time{}, false
	}
	items := f.scan()

	inst := byKind(items, "downloads-installers")
	if len(inst) != 1 || !sameStrings(bases(inst[0]), []string{"Old App.dmg"}) {
		t.Fatalf("installers = %v", inst)
	}
	arch := one(t, items, "downloads-archives")
	if !sameStrings(bases(arch), []string{"backup.tar.gz"}) || !strings.Contains(arch.Name, "sub-folders") {
		t.Errorf("archives = %s %v", arch.Name, bases(arch))
	}
	if arch.ID == inst[0].ID || arch.ID != "system:downloads-archives:"+f.abs("Downloads")+"/*/" {
		t.Errorf("unexpected archive id %s", arch.ID)
	}
	apk := one(t, items, "downloads-mobile-builds")
	if !sameStrings(bases(apk), []string{"build.apk"}) {
		t.Errorf("mobile builds = %v", bases(apk))
	}
	for _, it := range []*core.Item{inst[0], arch, apk} {
		if it.Risk != core.RiskCaution || it.Recommended || core.Recommend(it, f.now, 14*day) {
			t.Errorf("%s must be caution and never preselected", it.Name)
		}
		if it.Size <= 0 || it.LastUsed.IsZero() || it.Meta["largest"] == "" {
			t.Errorf("%s: size/lastUsed/meta missing: %+v", it.Name, it)
		}
	}
}

func TestDownloadsRespectsExcludeAndProtect(t *testing.T) {
	f := newFixture(t, "downloads")
	f.file("Downloads/keep/a.dmg", 1000, 90*day)
	f.file("Downloads/x/b.zip", 1000, 90*day)
	f.file("Downloads/c.iso", 1000, 90*day)
	f.env.Exclude = []string{f.abs("Downloads/keep")}
	prot := f.abs("Downloads/c.iso")
	base := f.env.Protected
	f.env.Protected = func(p string) bool { return p == prot || base(p) }
	items := f.scan()
	if l := byKind(items, "downloads-installers"); len(l) != 0 {
		t.Errorf("excluded / protected files proposed: %v", bases(l[0]))
	}
	if len(byKind(items, "downloads-archives")) != 1 {
		t.Errorf("b.zip should be proposed")
	}
}

// ------------------------------------------------------------------ updaters

func TestUpdaters(t *testing.T) {
	f := newFixture(t, "updaters")
	f.file("Library/Application Support/Caches/foo-updater/pending/foo-2.0.zip", 5000, 0)
	f.ageTree("Library/Application Support/Caches/foo-updater", 10*day)
	f.file("Library/Application Support/Caches/cursor-updater/pending/cursor.zip", 5000, 0) // aitools
	f.ageTree("Library/Application Support/Caches/cursor-updater", 10*day)
	f.file("Library/Caches/com.example.app.ShipIt/update.zip", 5000, 0)
	f.ageTree("Library/Caches/com.example.app.ShipIt", 10*day)
	f.file("Library/Caches/com.todesktop.230313.ShipIt/update.zip", 5000, 0) // Cursor (aitools)
	f.ageTree("Library/Caches/com.todesktop.230313.ShipIt", 10*day)
	f.file("Library/Caches/com.example.other/org.sparkle-project.Sparkle/PersistentDownloads/x.zip", 5000, 0) // recent
	items := f.scan()
	it := one(t, items, "app-update-downloads")
	if !sameStrings(bases(it), []string{"com.example.app.ShipIt", "pending"}) {
		t.Errorf("targets = %v", it.Targets())
	}
	if !it.Recommended || it.Risk != core.RiskSafe || it.Meta["apps"] != "com.example.app, foo" {
		t.Errorf("item = %+v", it)
	}
}

// ------------------------------------------------------------------ macOS facts

func TestSnapshotsAndSwap(t *testing.T) {
	f := newFixture(t, "snapshots", "macos")
	f.runner.bins["tmutil"] = true
	f.runner.out["tmutil listlocalsnapshots /"] = "Snapshots for disk /:\ncom.apple.TimeMachine.2026-09-27-101010.local\ncom.apple.TimeMachine.2026-09-28-101010.local\n"
	f.p.swapUsage = func() (int64, int64, bool) { return 10 << 30, 8 << 30, true }
	f.p.updatesDir = f.dir("sys/Updates", 0)
	f.file("sys/Updates/042-12345/pkg.pkg", 30_000, 0)
	items := f.scan()
	snap := one(t, items, "time-machine-snapshots")
	if snap.Method != core.MethodReport || snap.CanClean() || snap.Meta["count"] != "2" || snap.Warn == "" {
		t.Errorf("snapshots = %+v", snap)
	}
	swap := one(t, items, "macos-swap")
	if swap.Size != 10<<30 || swap.CanClean() || swap.Risk != core.RiskNever {
		t.Errorf("swap = %+v", swap)
	}
	upd := one(t, items, "macos-staged-updates")
	if upd.Size < 30_000 || upd.CanClean() {
		t.Errorf("updates = %+v", upd)
	}

	f2 := newFixture(t, "snapshots")
	f2.runner.bins["tmutil"] = true
	f2.runner.out["tmutil listlocalsnapshots /"] = "Snapshots for disk /:\n"
	if items := f2.scan(); len(items) != 0 {
		t.Errorf("no snapshot, no item: %v", items)
	}
}

// ------------------------------------------------------------------ Docker

func TestParseHumanSize(t *testing.T) {
	for _, c := range []struct {
		in     string
		binary bool
		want   int64
	}{
		{"1.089GB (60%)", false, 1_089_000_000},
		{"0B", false, 0},
		{"12kB", false, 12_000},
		{"512.5MB", false, 512_500_000},
		{"1.5GiB", false, 1_610_612_736},
		{"2GB", true, 2 << 30},
		{"45.6MB", true, 47_815_066},
		{"12KB", true, 12 * 1024},
		{"931B", true, 931},
		{"n/a", false, -1},
		{"", false, -1},
		{"12XB", false, -1},
	} {
		if got := parseHumanSize(c.in, c.binary); got != c.want {
			t.Errorf("parseHumanSize(%q, %v) = %d, want %d", c.in, c.binary, got, c.want)
		}
	}
}

const dockerDFOut = `{"Active":"2","Reclaimable":"1.2GB (60%)","Size":"2GB","TotalCount":"5","Type":"Images"}
{"Active":"0","Reclaimable":"0B","Size":"0B","TotalCount":"0","Type":"Containers"}
{"Active":"1","Reclaimable":"300MB (50%)","Size":"600MB","TotalCount":"3","Type":"Local Volumes"}
{"Active":"0","Reclaimable":"512.5MB","Size":"512.5MB","TotalCount":"30","Type":"Build Cache"}
`

func TestDocker(t *testing.T) {
	f := newFixture(t, "docker")
	f.runner.bins["docker"] = true
	f.runner.bins["colima"] = true
	f.runner.out["docker version --format {{.Server.Version}}"] = "27.3.1\n"
	f.runner.out["docker system df --format json"] = dockerDFOut
	f.runner.out["docker context show"] = "colima-work\n"
	items := f.scan()

	bc := one(t, items, "docker-build-cache")
	if bc.Method != core.MethodCommand || strings.Join(bc.Command, " ") != "docker builder prune -a -f" ||
		bc.Size != 512_500_000 || bc.Risk != core.RiskSafe || !core.Recommend(bc, f.now, 14*day) {
		t.Errorf("build cache = %+v", bc)
	}
	if len(bc.PostCommands) != 1 || strings.Join(bc.PostCommands[0], " ") != "colima ssh -p work -- sudo fstrim -av" {
		t.Errorf("colima fstrim post command missing: %v", bc.PostCommands)
	}
	img := one(t, items, "docker-unused-images")
	if strings.Join(img.Command, " ") != "docker image prune -a -f" || img.Size != 1_200_000_000 || img.Risk != core.RiskModerate {
		t.Errorf("images = %+v", img)
	}
	if core.Recommend(img, f.now, 14*day) {
		t.Errorf("images must not be preselected (unknown age)")
	}
	if l := byKind(items, "docker-stopped-containers"); len(l) != 0 {
		t.Errorf("0B containers must not be proposed")
	}
	vol := one(t, items, "docker-unused-volumes")
	if vol.CanClean() || vol.Risk != core.RiskCaution || vol.Size != 300_000_000 {
		t.Errorf("volumes must be report only: %+v", vol)
	}
	if bc.ID != "system:docker-build-cache:docker context colima-work" {
		t.Errorf("unstable id %s", bc.ID)
	}
}

func TestDockerUnreachable(t *testing.T) {
	f := newFixture(t, "docker")
	f.runner.bins["docker"] = true
	f.runner.fail["docker version --format {{.Server.Version}}"] = true
	f.runner.out["docker system df --format json"] = dockerDFOut
	if items := f.scan(); len(items) != 0 {
		t.Errorf("daemon down: no item expected, got %v", items)
	}
	for _, c := range f.runner.calls {
		if strings.Contains(c, "system df") {
			t.Errorf("df must not run when the daemon does not answer")
		}
	}
}

func TestColimaAndVMs(t *testing.T) {
	f := newFixture(t, "vms")
	f.runner.bins["colima"] = true
	f.runner.out["colima list -j"] = `{"name":"default","status":"Stopped","arch":"aarch64","disk":107374182400}
{"name":"work","status":"Running","arch":"aarch64","disk":64424509440}`
	f.file(".colima/_lima/colima/basedisk", 40_000, 90*day)
	f.file(".colima/_lima/colima/diffdisk", 20_000, 90*day)
	f.write(".colima/_lima/colima/lima.yaml", "x")
	dd := f.abs(".colima/_lima/_disks/colima/datadisk")
	os.MkdirAll(filepath.Dir(dd), 0o755)
	fh, _ := os.Create(dd) // sparse: 1 GiB apparent, nothing allocated
	fh.Truncate(1 << 30)
	fh.Close()
	f.file(".colima/_lima/colima-work/basedisk", 10_000, 0)
	os.Symlink("/Volumes/lu-cleaner-test-offline/disks/diffdisk", f.abs(".colima/_lima/colima-work/diffdisk"))
	f.dir(".colima/_lima/_config", 0)
	f.file("Library/Application Support/com.apple.container/content/blobs/x", 30_000, 0)
	items := f.scan()

	vms := byKind(items, "colima-vm")
	if len(vms) != 2 {
		t.Fatalf("colima VMs = %d", len(vms))
	}
	def, work := vms[0], vms[1]
	if !strings.HasSuffix(def.Path, "/colima") {
		def, work = work, def
	}
	for _, it := range vms {
		if it.CanClean() || it.Method != core.MethodReport || it.Risk != core.RiskCaution {
			t.Errorf("colima VMs are report only: %+v", it)
		}
	}
	if def.Meta["status"] != "stopped" || def.Meta["disks_apparent"] == "" || def.Size < 60_000 || def.Size > 1<<20 {
		t.Errorf("default VM = %+v (allocated size expected, not apparent)", def)
	}
	if !strings.Contains(work.Warn, "not mounted") || work.Meta["status"] != "running" {
		t.Errorf("work VM must warn about its offline disk: %+v", work)
	}
	ac := one(t, items, "apple-container-data")
	if ac.CanClean() || ac.Size < 30_000 {
		t.Errorf("apple container = %+v", ac)
	}
}

// ------------------------------------------------------------------ Homebrew

func TestParseBrewCleanup(t *testing.T) {
	out := `Would remove: /Users/x/Library/Caches/Homebrew/downloads/abc--foo-1.0.bottle.tar.gz (10MB)
Would remove: /opt/homebrew/Cellar/foo/1.0 (1,234 files, 2.5MB)
Would remove: /opt/homebrew/Caskroom/bar (old) (3 files, 1KB)
Warning: Skipping baz: most recent version 3.0 not installed
==> This operation would free approximately 12.5MB of disk space.
`
	c := parseBrewCleanup(out, "/Users/x/Library/Caches/Homebrew")
	if c.other != 2_621_440+1024 || c.kegs != 2 || len(c.removed) != 3 {
		t.Errorf("cleanup = %+v", c)
	}
	if !c.removed["/opt/homebrew/Caskroom/bar (old)"] || !c.removed["/opt/homebrew/Cellar/foo/1.0"] {
		t.Errorf("paths = %v", c.removed)
	}
	if len(c.skipped) != 1 || c.skipped[0] != "baz" {
		t.Errorf("skipped = %v", c.skipped)
	}
}

func TestHomebrew(t *testing.T) {
	f := newFixture(t, "homebrew")
	cache := f.abs("Library/Caches/Homebrew")
	prefix := f.abs("brew")
	f.runner.bins["brew"] = true
	f.runner.out["brew --cache"] = cache + "\n"
	f.runner.out["brew --prefix"] = prefix + "\n"
	f.runner.out["brew cleanup -n --prune=all"] = "Would remove: " + cache + "/downloads/x.tar.gz (10MB)\n" +
		"Would remove: " + prefix + "/Cellar/foo/1.0 (12 files, 2.5MB)\n" +
		"Warning: Skipping bar: most recent version 3.0 not installed\n"
	f.file("Library/Caches/Homebrew/downloads/x.tar.gz", 50_000, 0)
	f.file("Library/Caches/Homebrew/api/formula.jws.json", 50_000, 0)
	f.file("Library/Caches/Homebrew/bootsnap/x", 10_000, 0)
	for _, keg := range []string{"foo/1.0", "foo/2.0", "bar/1.0", "bar/2.0", "baz/1.0", "baz/2.0", "solo/1.0"} {
		f.file("brew/Cellar/"+keg+"/bin/x", 5_000, 0)
	}
	os.MkdirAll(f.abs("brew/opt"), 0o755)
	os.Symlink("../Cellar/foo/2.0", f.abs("brew/opt/foo"))
	os.Symlink("../Cellar/bar/2.0", f.abs("brew/opt/bar"))
	os.Symlink("../Cellar/solo/1.0", f.abs("brew/opt/solo"))
	// baz has no opt link: unknown which one is used, never reported
	items := f.scan()

	c := one(t, items, "homebrew-cache")
	if !sameStrings(bases(c), []string{"downloads"}) || c.Risk != core.RiskSafe {
		t.Errorf("cache must keep api/ and bootsnap/: %v", c.Targets())
	}
	cl := one(t, items, "homebrew-cleanup")
	if cl.Size != 2_621_440 || strings.Join(cl.Command, " ") != "brew cleanup --prune=all" || !cl.Recommended || cl.Method != core.MethodCommand {
		t.Errorf("cleanup = %+v", cl)
	}
	old := one(t, items, "homebrew-old-kegs")
	if old.CanClean() || len(old.Paths) != 1 || !strings.HasSuffix(old.Paths[0], "/Cellar/bar/1.0") {
		t.Errorf("old kegs = %+v", old.Paths)
	}
}

func TestHomebrewCleanupTimeout(t *testing.T) {
	old := brewCleanupTimeout
	brewCleanupTimeout = 50 * time.Millisecond
	defer func() { brewCleanupTimeout = old }()
	f := newFixture(t, "homebrew")
	f.runner.bins["brew"] = true
	f.runner.out["brew --cache"] = f.abs("Library/Caches/Homebrew") + "\n"
	f.runner.out["brew --prefix"] = f.abs("brew") + "\n"
	f.runner.block["brew cleanup -n --prune=all"] = true
	items := f.scan()
	cl := one(t, items, "homebrew-cleanup")
	if cl.Size != 0 || cl.Recommended || cl.Meta["size"] == "" || core.Recommend(cl, f.now, 14*day) {
		t.Errorf("unknown-size cleanup must be offered but never preselected: %+v", cl)
	}
}

// ------------------------------------------------------------------ Go / Rust / Ruby

func TestGo(t *testing.T) {
	f := newFixture(t, "go")
	f.runner.bins["go"] = true
	f.runner.out["go env GOCACHE GOMODCACHE"] = f.abs("Library/Caches/go-build") + "\n" + f.abs("go/pkg/mod") + "\n"
	f.file("Library/Caches/go-build/00/abc-d", 20_000, 0)
	f.file("go/pkg/mod/golang.org/x/sys@v0.1.0/unix/a.go", 20_000, 0)
	items := f.scan()
	bc := one(t, items, "go-build-cache")
	if bc.Method != core.MethodCommand || strings.Join(bc.Command, " ") != "go clean -cache" || bc.Risk != core.RiskSafe || bc.Size < 20_000 {
		t.Errorf("build cache = %+v", bc)
	}
	mc := one(t, items, "go-module-cache")
	if strings.Join(mc.Command, " ") != "go clean -modcache" || mc.Risk != core.RiskModerate {
		t.Errorf("mod cache = %+v", mc)
	}

	// No go binary: default locations are deleted directly.
	f2 := newFixture(t, "go")
	f2.file("Library/Caches/go-build/00/abc-d", 20_000, 0)
	items = f2.scan()
	bc = one(t, items, "go-build-cache")
	if bc.Method != core.MethodDelete || bc.Path != f2.abs("Library/Caches/go-build") {
		t.Errorf("fallback = %+v", bc)
	}
}

func TestGoModCacheOnExternalVolume(t *testing.T) {
	f := newFixture(t, "go")
	ext := f.dir("ext/mod", 0)
	f.file("ext/mod/cache/x", 10_000, 0)
	f.file("Library/Caches/go-build/00/x", 10_000, 0)
	f.runner.bins["go"] = true
	f.runner.out["go env GOCACHE GOMODCACHE"] = f.abs("Library/Caches/go-build") + "\n" + ext + "\n"
	f.p.devOf = func(p string) (uint64, error) {
		if strings.HasPrefix(p, ext) {
			return 999, nil
		}
		return statDev(p)
	}
	items := f.scan()
	mc := one(t, items, "go-module-cache")
	if mc.CanClean() || mc.Warn != warnExternal {
		t.Errorf("external module cache must be report only: %+v", mc)
	}
	if bc := one(t, items, "go-build-cache"); !bc.CanClean() {
		t.Errorf("internal build cache stays cleanable")
	}
}

func TestRustupAndRbenv(t *testing.T) {
	f := newFixture(t, "rust", "ruby")
	f.write(".rustup/settings.toml", "default_toolchain = \"stable-aarch64-apple-darwin\"\n[overrides]\n\"/p\" = \"1.70.0-aarch64-apple-darwin\"\n")
	f.file(".rustup/toolchains/stable-aarch64-apple-darwin/bin/rustc", 1000, 0)
	f.file(".rustup/toolchains/nightly-2024-01-01-aarch64-apple-darwin/bin/rustc", 1000, 0)
	f.file(".rustup/toolchains/1.70.0-aarch64-apple-darwin/bin/rustc", 1000, 0)
	f.write(".rbenv/version", "2.7.6\n")
	for _, v := range []string{"2.7.6", "3.0.0", "3.2.2", "3.3.0"} {
		f.file(".rbenv/versions/"+v+"/bin/ruby", 1000, 0)
	}
	root := f.dir("code", 0)
	f.write("code/app/.ruby-version", "3.2.2\n")
	f.write("code/org/other/.tool-versions", "nodejs 20.1.0\nruby 3.3.0\n")
	f.env.Roots = []string{root}
	items := f.scan()

	tc := one(t, items, "rustup-toolchain")
	if !strings.HasPrefix(filepath.Base(tc.Path), "nightly") || tc.CanClean() {
		t.Errorf("toolchain = %+v", tc)
	}
	rb := one(t, items, "rbenv-ruby")
	if filepath.Base(rb.Path) != "3.0.0" || rb.CanClean() || rb.Meta["command"] != "rbenv uninstall -f 3.0.0" {
		t.Errorf("ruby = %+v", rb)
	}
}

// ------------------------------------------------------------------ misc

func TestParseXMLPlist(t *testing.T) {
	data := `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>Device Name</key><string>Test iPhone</string>
<key>Applications</key><dict><key>x</key><string>nested</string></dict>
<key>Last Backup Date</key><date>2026-01-02T03:04:05Z</date>
<key>Installed Applications</key><array><string>a</string></array>
<key>Encrypted</key><true/>
<key>Product Type</key><string>iPhone15,2</string>
</dict></plist>`
	m := parseXMLPlist([]byte(data))
	if m["Device Name"] != "Test iPhone" || m["Last Backup Date"] != "2026-01-02T03:04:05Z" ||
		m["Product Type"] != "iPhone15,2" || m["Encrypted"] != "true" || m["x"] != "" {
		t.Errorf("plist = %v", m)
	}
}

func TestDeviceBackups(t *testing.T) {
	f := newFixture(t, "backups")
	f.write("Library/Application Support/MobileSync/Backup/00008110-AAAA/Info.plist",
		`<plist><dict><key>Device Name</key><string>Test Phone</string><key>Last Backup Date</key><date>2026-01-02T03:04:05Z</date></dict></plist>`)
	f.file("Library/Application Support/MobileSync/Backup/00008110-AAAA/ab/abcdef", 30_000, 0)
	items := f.scan()
	b := one(t, items, "ios-device-backup")
	if b.CanClean() || b.Name != "Device backup · Test Phone" || b.Size < 30_000 || b.LastUsed.Year() != 2026 {
		t.Errorf("backup = %+v", b)
	}
}

func TestIDsAreStable(t *testing.T) {
	f := newFixture(t, "trash", "downloads", "vscode", "jetbrains")
	f.file(".Trash/a", 1000, 0)
	f.file("Downloads/a.dmg", 1000, 60*day)
	f.file("Library/Caches/JetBrains/WebStorm2024.1/x", 1000, 0)
	f.file("Library/Caches/JetBrains/WebStorm2025.1/x", 1000, 0)
	f.file("Library/Application Support/Code/CachedData/aaa/x", 1000, 0)
	f.file("Library/Application Support/Code/CachedData/bbb/x", 1000, 0)
	a, b := f.scan(), f.scan()
	if len(a) == 0 || len(a) != len(b) {
		t.Fatalf("scans differ: %d vs %d", len(a), len(b))
	}
	for id := range a {
		if b[id] == nil {
			t.Errorf("id %s not stable", id)
		}
		if !strings.HasPrefix(id, "system:") {
			t.Errorf("id %s must start with system:", id)
		}
	}
}

func TestCancelledScan(t *testing.T) {
	f := newFixture(t)
	f.p.only = nil
	f.file(".Trash/a", 1000, 0)
	ctx, cancel := contextCancelled()
	defer cancel()
	if err := f.p.Scan(ctx, f.env, func(*core.Item) {}); err == nil {
		t.Errorf("a cancelled scan must report ctx.Err()")
	}
}
