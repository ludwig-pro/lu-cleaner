package system

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/safety"
)

func contextCancelled() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx, cancel
}

const codeData = "Library/Application Support/Code"

// installVSCode creates a fake "Visual Studio Code.app" with a product.json.
func installVSCode(f *fixture, commit string) {
	f.write("Applications/Visual Studio Code.app/Contents/Resources/app/product.json", `{"commit":"`+commit+`","nameShort":"Code"}`)
}

func TestVSCodeCachedDataAndCaches(t *testing.T) {
	f := newFixture(t, "vscode")
	installVSCode(f, "cur123")
	f.file(codeData+"/CachedData/cur123/a", 5000, 0)
	f.file(codeData+"/CachedData/old456/a", 5000, 0)
	f.file(codeData+"/CachedData/old789/a", 5000, 0)
	f.file(codeData+"/CachedExtensionVSIXs/ms-python.python-2024.1.0", 9000, 0)
	f.file(codeData+"/CachedExtensionVSIXs/.a1b2c3", 1000, 0) // partial download
	f.file(codeData+"/Cache/Cache_Data/x", 4000, 0)
	f.file(codeData+"/GPUCache/data_0", 4000, 0)
	f.file(codeData+"/logs/20260901T100000/main.log", 4000, 0)
	f.file(codeData+"/logs/20260928T100000/main.log", 4000, 0) // newest session: kept
	f.file(codeData+"/User/History/abc/entries.json", 1000, 0)
	f.write(codeData+"/User/settings.json", "{}")
	f.p.running = func(names ...string) []string {
		for _, n := range names {
			if n == "/Visual Studio Code.app/Contents/MacOS/" {
				return []string{n}
			}
		}
		return nil
	}
	items := f.scan()

	cd := one(t, items, "vscode-cached-data")
	if !sameStrings(bases(cd), []string{"old456", "old789"}) || !cd.Recommended || cd.Meta["kept"] != "cur123" {
		t.Errorf("cached data = %v %v", bases(cd), cd.Meta)
	}
	if cd.Warn != "" || len(cd.ProcessGuard) != 0 {
		t.Errorf("old builds' folders are not used by the running editor: %+v", cd)
	}
	vsix := one(t, items, "vscode-vsix-cache")
	if len(vsix.Paths) != 2 || vsix.Risk != core.RiskSafe {
		t.Errorf("vsix = %v", vsix.Targets())
	}
	c := one(t, items, "vscode-caches")
	if !sameStrings(bases(c), []string{"20260901T100000", "Cache", "GPUCache"}) {
		t.Errorf("caches = %v", bases(c))
	}
	if c.Warn != "Visual Studio Code is running — quit it before cleaning" || c.ProcessGuard[0] != "/Visual Studio Code.app/Contents/MacOS/" {
		t.Errorf("running editor must be flagged: %+v", c)
	}
	h := one(t, items, "vscode-local-history")
	if h.CanClean() || h.Risk != core.RiskCaution {
		t.Errorf("local history must be report only")
	}
}

func TestVSCodeWorkspaceStorage(t *testing.T) {
	f := newFixture(t, "vscode")
	installVSCode(f, "c1")
	ws := codeData + "/User/workspaceStorage/"
	f.dir("projects/live", 0)
	entry := func(id, uri string, age int) {
		if uri != "" {
			f.write(ws+id+"/workspace.json", `{"folder":"`+uri+`"}`)
		}
		f.file(ws+id+"/state.vscdb", 2000, 0)
		f.ageTree(ws+id, dayN(age))
	}
	entry("live", "file://"+f.abs("projects/live"), 30)
	f.file(ws+"live/redhat.java/jdt_ws/index", 50_000, 0)
	f.ageTree(ws+"live/redhat.java", dayN(10))
	f.file(ws+"live/some.extension/state", 100, 0)
	entry("gone", "file://"+strings.ReplaceAll(f.abs("projects/gone worktree"), " ", "%20"), 30)
	entry("gone-recent", "file://"+f.abs("projects/just-deleted"), 2)
	entry("offline", "file:///Volumes/lu-cleaner-test-offline/Sources/app", 60)
	entry("remote", "vscode-remote://ssh-remote%2Bbox/home/me/app", 60)
	entry("1786544695134", "", 60) // empty window: no workspace.json
	items := f.scan()

	orph := one(t, items, "vscode-workspace-orphans")
	if !sameStrings(bases(orph), []string{"gone"}) {
		t.Errorf("orphans = %v", bases(orph))
	}
	if orph.Risk != core.RiskModerate || !orph.Recommended || orph.Meta["kept_offline_volume"] != "1" {
		t.Errorf("orphans item = %+v", orph)
	}
	if !strings.Contains(orph.Meta["examples"], "projects/gone worktree") {
		t.Errorf("examples should show the decoded folder: %v", orph.Meta)
	}
	idx := one(t, items, "vscode-ls-indexes")
	if len(idx.Paths) != 1 || filepath.Base(idx.Paths[0]) != "redhat.java" || idx.Risk != core.RiskSafe {
		t.Errorf("indexes = %v", idx.Paths)
	}
}

func TestClassifyWorkspace(t *testing.T) {
	f := newFixture(t)
	s := &scan{p: f.p, env: f.env}
	f.dir("here", 0)
	for _, c := range []struct {
		uri  string
		want wsState
	}{
		{"file://" + f.abs("here"), wsLive},
		{"file://" + f.abs("nope"), wsOrphan},
		{"file:///Volumes/lu-cleaner-test-offline/x", wsOffline},
		{"file://" + f.abs("Library/CloudStorage/GoogleDrive-me/x"), wsOffline},
		{"vscode-remote://wsl%2Bubuntu/home", wsUnknown},
		{"vscode-vfs://github/me/repo", wsUnknown},
		{"not a uri %%", wsUnknown},
	} {
		if got, _ := s.classifyWorkspace(c.uri); got != c.want {
			t.Errorf("classify(%s) = %d, want %d", c.uri, got, c.want)
		}
	}
}

func TestParseExtFolder(t *testing.T) {
	for _, c := range []struct{ in, id, ver string }{
		{"dbaeumer.vscode-eslint-3.0.34", "dbaeumer.vscode-eslint", "3.0.34"},
		{"ms-vscode.cpptools-1.32.2-darwin-arm64", "ms-vscode.cpptools", "1.32.2"},
		{"anthropic.claude-code-2.1.228-darwin-arm64", "anthropic.claude-code", "2.1.228"},
		{"ms-python.python-2024.1.0", "ms-python.python", "2024.1.0"},
		{"foo.bar-1.0.0-beta.2", "foo.bar", "1.0.0-beta.2"},
		{"extensions.json", "", ""},
		{".obsolete", "", ""},
	} {
		id, ver, ok := parseExtFolder(c.in)
		if id != c.id || ver != c.ver || ok != (c.id != "") {
			t.Errorf("parseExtFolder(%q) = %q %q %v", c.in, id, ver, ok)
		}
	}
}

func TestVSCodeOldExtensions(t *testing.T) {
	f := newFixture(t, "vscode")
	installVSCode(f, "c1")
	ext := ".vscode/extensions/"
	for _, n := range []string{"a.b-1.0.0", "a.b-0.9.0", "c.d-2.0.0-darwin-arm64", "c.d-1.9.0-darwin-arm64", "e.f-1.0.0"} {
		f.file(ext+n+"/package.json", 3000, 0)
	}
	f.write(ext+"extensions.json", `[
 {"identifier":{"id":"a.b"},"version":"1.0.0","location":{"$mid":1,"path":"/x/a.b-1.0.0","scheme":"file"},"relativeLocation":"a.b-1.0.0"},
 {"identifier":{"id":"c.d"},"version":"2.0.0","location":{"$mid":1,"path":"/x/c.d-2.0.0-darwin-arm64","scheme":"file"}}
]`)
	f.write(ext+".obsolete", `{"c.d-1.9.0-darwin-arm64":true}`)
	f.write(".vscode/argv.json", "{}")
	items := f.scan()
	it := one(t, items, "vscode-old-extensions")
	// e.f-1.0.0 is not registered but has no newer version: maybe being installed, kept.
	if !sameStrings(bases(it), []string{"a.b-0.9.0", "c.d-1.9.0-darwin-arm64"}) || !it.Recommended || it.Risk != core.RiskSafe {
		t.Errorf("old extensions = %v", bases(it))
	}
	for _, p := range it.Paths {
		if !strings.HasPrefix(p, f.abs(".vscode/extensions/")) {
			t.Errorf("outside extensions dir: %s", p)
		}
	}
}

func TestVSCodeOldExtensionsWithoutRegistry(t *testing.T) {
	f := newFixture(t, "vscode")
	installVSCode(f, "c1")
	for _, n := range []string{"g.h-1.0.0", "g.h-1.2.0", "g.h-1.10.0", "solo.one-0.1.0"} {
		f.file(".vscode/extensions/"+n+"/package.json", 3000, 0)
	}
	items := f.scan()
	it := one(t, items, "vscode-old-extensions")
	if !sameStrings(bases(it), []string{"g.h-1.0.0", "g.h-1.2.0"}) {
		t.Errorf("duplicates only, newest kept: %v", bases(it))
	}
}

func TestVSCodeNotInstalled(t *testing.T) {
	f := newFixture(t, "vscode")
	f.file(".vscode-oss/extensions/a.b-1.0.0/package.json", 3000, 0)
	f.file(".vscode-oss/extensions/a.b-0.9.0/package.json", 3000, 0)
	items := f.scan()
	it := one(t, items, "vscodium-extensions-leftover")
	if it.Risk != core.RiskCaution || it.Path != f.abs(".vscode-oss/extensions") || core.Recommend(it, f.now.Add(365*day), 14*day) {
		t.Errorf("leftover = %+v", it)
	}
	if len(byKind(items, "vscodium-old-extensions")) != 0 {
		t.Errorf("no nested old-version item when the whole dir is proposed")
	}
}

func TestZed(t *testing.T) {
	f := newFixture(t, "zed")
	f.file("Library/Application Support/Zed/languages/eslint/server.js", 8000, 0)
	f.file("Library/Application Support/Zed/node/node-v22/bin/node", 8000, 0)
	f.file("Library/Application Support/Zed/db/0-stable/db.sqlite", 4000, 0)
	f.file("Library/Application Support/Zed/hang_traces/t1", 1000, 0)
	f.file("Library/Logs/Zed/Zed.log", 1000, 0)
	items := f.scan()
	lo := one(t, items, "zed-leftover")
	if lo.Risk != core.RiskCaution || len(lo.Paths) != 2 {
		t.Errorf("not installed: whole data proposed as caution: %v", lo.Paths)
	}

	f.dir("Applications/Zed.app/Contents/MacOS", 0)
	items = f.scan()
	if len(byKind(items, "zed-leftover")) != 0 {
		t.Fatalf("installed Zed has no leftover item")
	}
	ls := one(t, items, "zed-language-servers")
	if !sameStrings(bases(ls), []string{"languages", "node"}) || ls.Risk != core.RiskModerate {
		t.Errorf("language servers = %v", bases(ls))
	}
	c := one(t, items, "zed-caches")
	if !sameStrings(bases(c), []string{"Zed", "hang_traces"}) || c.ProcessGuard[0] != "zed" {
		t.Errorf("caches = %v", bases(c))
	}
	for _, it := range items {
		for _, p := range it.Targets() {
			if strings.Contains(p, "/db") {
				t.Errorf("Zed database proposed while Zed is installed: %s", p)
			}
		}
	}
}

func TestJetBrains(t *testing.T) {
	f := newFixture(t, "jetbrains")
	f.file("Library/Caches/JetBrains/WebStorm2024.3/index/x", 5000, 0)
	f.file("Library/Caches/JetBrains/WebStorm2025.1/index/x", 5000, 0)
	f.file("Library/Caches/JetBrains/IntelliJIdea2025.2/index/x", 5000, 0)
	f.file("Library/Application Support/JetBrains/WebStorm2024.3/options/x.xml", 1000, 0)
	f.file("Library/Application Support/JetBrains/WebStorm2025.1/options/x.xml", 1000, 0)
	f.file("Library/Application Support/JetBrains/Toolbox/state.json", 1000, 0)
	f.file("Library/Logs/JetBrains/WebStorm2026.1/idea.log", 1000, 0) // logs alone never define "newest"
	items := f.scan()

	old := one(t, items, "jetbrains-old-caches")
	if !sameStrings(bases(old), []string{"WebStorm2024.3"}) || !old.Recommended || old.Risk != core.RiskSafe {
		t.Errorf("old caches = %v", bases(old))
	}
	if len(old.ProcessGuard) != 1 || old.ProcessGuard[0] != "webstorm" {
		t.Errorf("process guard = %v", old.ProcessGuard)
	}
	set := one(t, items, "jetbrains-old-settings")
	if !sameStrings(bases(set), []string{"WebStorm2024.3"}) || set.Risk != core.RiskModerate || set.Recommended {
		t.Errorf("old settings = %v", bases(set))
	}
	cur := one(t, items, "jetbrains-caches")
	if !sameStrings(bases(cur), []string{"IntelliJIdea2025.2", "WebStorm2025.1"}) || cur.Risk != core.RiskModerate {
		t.Errorf("current caches = %v", bases(cur))
	}
	if !sameStrings(cur.ProcessGuard, []string{"idea", "webstorm"}) {
		t.Errorf("guard = %v", cur.ProcessGuard)
	}
	if l := one(t, items, "jetbrains-logs"); len(l.Paths) != 1 {
		t.Errorf("logs = %v", l.Paths)
	}
}

// JetBrains keeps its Local History (past versions of the user's files) in
// the caches folder of each IDE version: it is never part of a cache item.
func TestJetBrainsLocalHistoryKept(t *testing.T) {
	f := newFixture(t, "jetbrains")
	const jb = "Library/Caches/JetBrains/"
	f.file(jb+"WebStorm2024.3/index/x", 5000, 0)
	f.file(jb+"WebStorm2024.3/.hidden", 100, 0)
	f.file(jb+"WebStorm2024.3/LocalHistory/changes.storageData", 400_000, 0)
	f.file(jb+"WebStorm2025.1/caches/x", 5000, 0)
	f.file(jb+"WebStorm2025.1/localhistory/changes.storageData", 400_000, 0) // any case (APFS)
	f.file(jb+"IntelliJIdea2023.1/LocalHistory/changes.storageData", 400_000, 0)
	f.file(jb+"IntelliJIdea2025.2/index/x", 5000, 0)
	items := f.scan()

	for _, kind := range []string{"jetbrains-old-caches", "jetbrains-caches"} {
		it := one(t, items, kind)
		for _, p := range it.Targets() {
			if strings.Contains(strings.ToLower(p+"/"), "/localhistory/") {
				t.Errorf("%s proposes the local history: %s", kind, p)
			}
			if _, err := os.Stat(filepath.Join(p, "LocalHistory")); err == nil {
				t.Errorf("%s target %s contains the local history", kind, p)
			}
		}
		if it.Size >= 400_000 {
			t.Errorf("%s size %d includes the local history", kind, it.Size)
		}
		if !strings.Contains(it.Note, "LocalHistory") {
			t.Errorf("%s note does not say the local history is kept: %q", kind, it.Note)
		}
	}
	old := one(t, items, "jetbrains-old-caches")
	if !sameStrings(bases(old), []string{".hidden", "index"}) {
		t.Errorf("old caches targets = %v", bases(old))
	}
	if old.Meta["versions"] != "WebStorm 2024.3" || !strings.HasSuffix(old.Name, "(1)") {
		t.Errorf("a version with only its local history left must not be listed: %q %q", old.Name, old.Meta["versions"])
	}
	if !sameStrings(old.ProcessGuard, []string{"webstorm"}) {
		t.Errorf("old caches guard = %v", old.ProcessGuard)
	}
	cur := one(t, items, "jetbrains-caches")
	if !sameStrings(bases(cur), []string{"IntelliJIdea2025.2", "caches"}) {
		t.Errorf("current caches targets = %v", bases(cur))
	}
}

func TestProtectedPathsNeverProposed(t *testing.T) {
	// A user-protected folder (config "protect") inside a cache must drop the
	// matching targets, never the guard check.
	f := newFixture(t, "vscode", "trash")
	installVSCode(f, "cur")
	f.file(codeData+"/CachedData/cur/a", 1000, 0)
	f.file(codeData+"/CachedData/old1/a", 1000, 0)
	f.file(codeData+"/CachedData/old2/a", 1000, 0)
	f.file(".Trash/keep-me/a", 1000, 0)
	f.file(".Trash/other", 1000, 0)
	protected := []string{f.abs(codeData + "/CachedData/old1"), f.abs(".Trash/keep-me")}
	base := f.env.Protected
	f.env.Protected = func(p string) bool {
		for _, x := range protected {
			if p == x || strings.HasPrefix(p, x+"/") {
				return true
			}
		}
		return base(p)
	}
	items := f.scan()
	if cd := one(t, items, "vscode-cached-data"); !sameStrings(bases(cd), []string{"old2"}) {
		t.Errorf("cached data = %v", bases(cd))
	}
	if tr := one(t, items, "trash"); !sameStrings(bases(tr), []string{"other"}) {
		t.Errorf("trash = %v", bases(tr))
	}
	// The guard itself refuses the whole ~/.Trash and ~/Downloads folders.
	for _, p := range []string{".Trash", "Downloads", ".vscode", codeData + "/User/settings.json"} {
		if err := f.guard.Check(f.abs(p), safety.Options{}); err == nil {
			t.Errorf("%s must be refused by the guard", p)
		}
	}
}

func dayN(n int) time.Duration { return time.Duration(n) * day }
