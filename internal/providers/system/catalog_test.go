package system

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
	"github.com/ludwig-pro/lu-cleaner/internal/providers/catalog"
)

// systemEntry reports whether a catalog entry belongs to this domain
// (internal/providers/catalog/data_system.go).
func systemEntry(id string) bool {
	for _, pre := range []string{"browser-", "app-", "system-", "ide-", "containers-", "langs-"} {
		if strings.HasPrefix(id, pre) {
			return true
		}
	}
	return false
}

// TestCatalogEntriesStayInDomain checks that the static entries never cover
// what the dynamic provider (or another domain) handles.
func TestCatalogEntriesStayInDomain(t *testing.T) {
	dynamic := []string{
		"~/.Trash", "~/Downloads", "~/Library/Caches/go-build", "~/go", "~/Library/Caches/Homebrew",
		"~/Library/Application Support/Code", "~/.vscode", "~/.vscode-insiders", "~/.vscode-oss",
		"~/Library/Application Support/Zed", "~/Library/Caches/JetBrains", "~/Library/Application Support/JetBrains",
		"~/.colima", "~/.rustup/toolchains", "~/Library/Application Support/MobileSync",
		"~/Library/Application Support/Caches", "~/Library/Application Support/com.apple.container",
		// other domains
		"~/Library/Application Support/Cursor", "~/.cursor", "~/Library/Application Support/Claude", "~/.claude",
		"~/.codex", "~/Library/Caches/Yarn", "~/Library/Caches/ms-playwright", "~/Library/Caches/CocoaPods",
		"~/Library/Developer", "~/.gradle", "~/.android", "~/.m2", "~/Library/Application Support/com.raycast.macos",
	}
	env := &core.Env{Home: "/Users/tester"}
	kinds := map[string]bool{}
	for _, e := range catalog.Entries() {
		if !systemEntry(e.ID) {
			continue
		}
		kinds[e.ID] = true
		for _, p := range e.Paths {
			abs := env.Expand(p)
			for _, d := range dynamic {
				d = env.Expand(d)
				if abs == d || strings.HasPrefix(abs, d+"/") || strings.HasPrefix(d, abs+"/") {
					t.Errorf("%s: path %s overlaps %s", e.ID, p, d)
				}
			}
			if strings.HasSuffix(abs, "/Library/Caches/*") || abs == env.Expand("~/Library/Logs") {
				t.Errorf("%s: blanket path %s", e.ID, p)
			}
		}
	}
	if len(kinds) < 20 {
		t.Fatalf("only %d system entries registered", len(kinds))
	}
	// Kinds of the provider must not collide with catalog ids.
	for _, k := range []string{"trash", "app-update-downloads", "homebrew-cache", "go-build-cache", "vscode-caches", "zed-caches", "jetbrains-logs"} {
		if kinds[k] {
			t.Errorf("kind %s used by both the catalog and the provider", k)
		}
	}
}

// TestCatalogBrowserAndLogs evaluates the static entries on a fixture home:
// only named cache folders of a browser profile are proposed, never the
// profile itself; logs of other domains and fresh logs are left alone.
func TestCatalogBrowserAndLogs(t *testing.T) {
	f := newFixture(t)
	chrome := "Library/Application Support/Google/Chrome/"
	f.file("Library/Caches/Google/Chrome/Default/Cache/Cache_Data/f_1", 5000, 0)
	f.file("Library/Caches/Google/Chrome/Default/Code Cache/js/x", 5000, 0)
	f.file(chrome+"Default/GPUCache/data_1", 5000, 0)
	f.file(chrome+"Default/Cookies", 5000, 0)
	f.file(chrome+"Default/History", 5000, 0)
	f.file(chrome+"Default/Login Data", 5000, 0)
	f.file(chrome+"Default/Extensions/abc/1.0/manifest.json", 5000, 0)
	f.file(chrome+"Default/Service Worker/CacheStorage/abc/x", 5000, 0)
	f.file(chrome+"Default/Service Worker/ScriptCache/x", 5000, 0)
	f.file(chrome+"component_crx_cache/x", 5000, 0)
	f.file(chrome+"Local State", 500, 0)
	f.file("Library/Logs/SomeApp/app.log", 1000, 0)
	f.ageTree("Library/Logs/SomeApp", 30*day)
	f.file("Library/Logs/Fresh/app.log", 1000, 0)
	f.file("Library/Logs/CoreSimulator/x.log", 1000, 0)
	f.ageTree("Library/Logs/CoreSimulator", 30*day)
	f.file("Library/Logs/Zed/Zed.log", 1000, 0)
	f.ageTree("Library/Logs/Zed", 30*day)
	f.file("Library/Logs/old.log", 1000, 0)
	f.age("Library/Logs/old.log", 30*day)

	var mu sync.Mutex
	got := map[string]*core.Item{}
	if err := catalog.New().Scan(context.Background(), f.env, func(it *core.Item) {
		mu.Lock()
		got[it.Kind] = it
		mu.Unlock()
	}); err != nil {
		t.Fatal(err)
	}
	for kind, it := range got {
		if !systemEntry(kind) {
			continue
		}
		f.checkGuard(it)
		for _, p := range it.Targets() {
			for _, bad := range []string{"Cookies", "History", "Login Data", "Extensions", "ScriptCache", "Local State"} {
				if strings.Contains(p, bad) {
					t.Errorf("%s proposes profile data %s", kind, p)
				}
			}
		}
	}
	c := got["browser-chrome-cache"]
	if c == nil || !sameStrings(bases(c), []string{"Cache", "Code Cache", "GPUCache", "component_crx_cache"}) {
		t.Fatalf("chrome caches = %v", c)
	}
	if c.Risk != core.RiskSafe || len(c.ProcessGuard) == 0 {
		t.Errorf("chrome cache item = %+v", c)
	}
	sw := got["browser-chrome-service-worker-cache"]
	if sw == nil || sw.Risk != core.RiskModerate || filepath.Base(sw.Path) != "CacheStorage" {
		t.Errorf("service worker = %+v", sw)
	}
	logs := got["system-user-logs"]
	if logs == nil || !sameStrings(bases(logs), []string{"SomeApp"}) {
		t.Errorf("logs = %v", logs)
	}
	lf := got["system-user-log-files"]
	if lf == nil || !sameStrings(bases(lf), []string{"old.log"}) {
		t.Errorf("log files = %v", lf)
	}
}
