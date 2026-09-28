package aitools

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
)

const cursorSupport = "Library/Application Support/Cursor"

// fakeCursorApp installs ~/Applications/Cursor.app with a product.json commit
// and a few built-in extensions.
func (f *fixture) fakeCursorApp(commit string) {
	app := "Applications/Cursor.app/Contents/Resources/app"
	f.text(app+"/product.json", `{"commit":"`+commit+`"}`, 0)
	f.text(app+"/extensions/cursor-retrieval/package.json", `{"publisher":"anysphere","name":"cursor-retrieval"}`, 0)
	f.text(app+"/extensions/git/package.json", `{"publisher":"vscode","name":"git"}`, 0)
}

func fileURI(p string) string { return (&url.URL{Scheme: "file", Path: p}).String() }

func TestCursorCachedData(t *testing.T) {
	for _, withApp := range []bool{true, false} {
		f := newFixture(t)
		if withApp {
			f.fakeCursorApp("bbb")
		}
		f.file(cursorSupport+"/CachedData/aaa/x", 3000, 0)
		f.file(cursorSupport+"/CachedData/bbb/x", 3000, 0)
		f.file(cursorSupport+"/CachedData/ccc/x", 3000, 0)
		f.ageTree(cursorSupport+"/CachedData/aaa", 40*day)
		f.ageTree(cursorSupport+"/CachedData/bbb", 20*day)
		f.ageTree(cursorSupport+"/CachedData/ccc", 5*day) // newest by mtime
		r := f.scan()
		f.checkInvariants(r)
		it := r.one(t, "cursor-cached-data-old-builds")
		keep := "ccc"
		if withApp {
			keep = "bbb" // the installed build wins over mtime
		}
		if it.Meta["kept"] != keep || len(it.Paths) != 2 || hasTarget(it, f.path(cursorSupport+"/CachedData/"+keep)) {
			t.Errorf("withApp=%v: kept %s, targets %v", withApp, it.Meta["kept"], it.Paths)
		}
		if it.Category != core.CatIDE || it.Risk != core.RiskSafe || !it.Recommended || len(it.ProcessGuard) == 0 {
			t.Errorf("CachedData item = %+v", it)
		}
	}
}

func TestCursorWorkspaceStorage(t *testing.T) {
	f := newFixture(t)
	f.fakeCursorApp("c1")
	f.dir(".cursor/extensions/acme.installed-1.0.0", 0)
	ws := cursorSupport + "/User/workspaceStorage/"
	existing := f.dir("src/my app", 0) // space: URI-encoded as %20

	f.text(ws+"live/workspace.json", `{"folder":"`+fileURI(existing)+`"}`, 0)
	f.file(ws+"live/state.vscdb", 1000, 0)
	f.file(ws+"live/redhat.java/jdt_ws/index.bin", 50_000, 0)
	f.file(ws+"live/anysphere.cursor-retrieval/x", 1000, 0) // built-in
	f.file(ws+"live/acme.installed/x", 1000, 0)             // installed
	f.file(ws+"live/images/x.png", 1000, 0)                 // not an extension

	f.text(ws+"gone/workspace.json", `{"folder":"`+fileURI(f.path("deleted/worktree"))+`"}`, 0)
	f.file(ws+"gone/state.vscdb", 2000, 0)
	f.file(ws+"gone/redhat.java/x", 2000, 0)
	f.ageTree(ws+"gone", 10*day)

	f.text(ws+"multi/workspace.json", `{"workspace":"`+fileURI(f.path("deleted/proj.code-workspace"))+`"}`, 0)
	f.ageTree(ws+"multi", 10*day)
	f.text(ws+"remote/workspace.json", `{"folder":"vscode-remote://ssh-remote+box/home/me"}`, 0)
	f.text(ws+"volume/workspace.json", `{"folder":"file:///Volumes/lu-cleaner-test-not-mounted-42/src"}`, 0)
	f.dir(ws+"empty-window", 0)

	// 40 days old, no chat data: the only preselected orphans.
	f.text(ws+"old/workspace.json", `{"folder":"`+fileURI(f.path("deleted/old"))+`"}`, 0)
	f.text(ws+"old/state.vscdb", "SQLite format 3\x00 workbench.explorer.treeViewState memento/workbench.parts.editor", 0)
	f.ageTree(ws+"old", 40*day)
	// 40 days old but its state DB holds composer / legacy AI chat data.
	f.text(ws+"chat/workspace.json", `{"folder":"`+fileURI(f.path("deleted/chat"))+`"}`, 0)
	f.text(ws+"chat/state.vscdb", "SQLite format 3\x00 ... composer.composerData ... workbench.panel.aichat.view.aichat.chatdata", 0)
	f.ageTree(ws+"chat", 40*day)
	// An archived Conductor workspace may be restored.
	f.text(ws+"conductor/workspace.json", `{"folder":"`+fileURI(f.path("conductor/workspaces/repo/lagos"))+`"}`, 0)
	f.ageTree(ws+"conductor", 40*day)
	// A folder of a logged-out cloud provider is not known to be gone.
	f.text(ws+"cloud/workspace.json", `{"folder":"`+fileURI(f.path("Library/CloudStorage/Dropbox/src/app"))+`"}`, 0)
	f.ageTree(ws+"cloud", 40*day)
	settings := f.text(cursorSupport+"/User/settings.json", "{}", 0)

	r := f.scan()
	f.checkInvariants(r, settings, f.path(ws+"live/state.vscdb"))

	var orph, review *core.Item
	for _, it := range r.byKind("cursor-workspace-storage-orphans") {
		if strings.HasSuffix(it.ID, "#review") {
			review = it
		} else {
			orph = it
		}
	}
	if orph == nil || len(orph.Paths) != 1 || !hasTarget(orph, f.path(ws+"old")) {
		t.Fatalf("orphans = %+v", orph)
	}
	if orph.Risk != core.RiskModerate || !orph.Recommended || !core.Recommend(orph, f.now, 14*day) {
		t.Errorf("orphans = %v %v", orph.Risk, orph.Recommended)
	}
	if review == nil || len(review.Paths) != 4 || !hasTarget(review, f.path(ws+"gone")) || !hasTarget(review, f.path(ws+"multi")) ||
		!hasTarget(review, f.path(ws+"chat")) || !hasTarget(review, f.path(ws+"conductor")) {
		t.Fatalf("orphans to review = %+v", review)
	}
	if review.Risk != core.RiskCaution || core.Recommend(review, f.now, 14*day) || review.Meta["with_chat_data"] != "1" {
		t.Errorf("review = %v %v %v", review.Risk, review.NoRecommend, review.Meta)
	}
	dead := r.byKind("cursor-workspace-dead-extension-data")
	if len(dead) != 1 || dead[0].Meta["extension"] != "redhat.java" || len(dead[0].Paths) != 1 ||
		dead[0].Paths[0] != f.path(ws+"live/redhat.java") {
		t.Errorf("dead extension data = %v", names(dead))
	}
}

func TestCursorWorkspaceStorageWithoutAppKeepsExtensionData(t *testing.T) {
	f := newFixture(t) // no Cursor.app: built-ins unknown
	ws := cursorSupport + "/User/workspaceStorage/"
	existing := f.dir("src/app", 0)
	f.text(ws+"live/workspace.json", `{"folder":"`+fileURI(existing)+`"}`, 0)
	f.file(ws+"live/redhat.java/x", 5000, 0)
	r := f.scan()
	if n := len(r.byKind("cursor-workspace-dead-extension-data")); n != 0 {
		t.Errorf("without the app's built-in list nothing can be called uninstalled (%d items)", n)
	}
}

func TestCursorExtensions(t *testing.T) {
	cases := []struct {
		name     string
		registry string
		obsolete string
		want     []string
	}{
		{
			name:     "extensions.json is authoritative",
			registry: `[{"identifier":{"id":"foo.bar"},"relativeLocation":"foo.bar-1.2.0"},{"identifier":{"id":"anthropic.claude-code"},"relativeLocation":"anthropic.claude-code-2.1.9-darwin-arm64"}]`,
			obsolete: `{"old.one-0.1.0":true}`,
			want:     []string{"anthropic.claude-code-2.1.10-darwin-arm64", "foo.bar-1.0.0", "old.one-0.1.0"},
		},
		{
			name: "no registry: keep the highest version",
			want: []string{"anthropic.claude-code-2.1.9-darwin-arm64", "foo.bar-1.0.0"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			for _, d := range []string{"foo.bar-1.0.0", "foo.bar-1.2.0", "anthropic.claude-code-2.1.9-darwin-arm64",
				"anthropic.claude-code-2.1.10-darwin-arm64", "old.one-0.1.0", "solo.ext-3.0.0-universal"} {
				f.file(".cursor/extensions/"+d+"/package.json", 100, 0)
			}
			if c.registry != "" {
				f.text(".cursor/extensions/extensions.json", c.registry, 0)
			}
			if c.obsolete != "" {
				f.text(".cursor/extensions/.obsolete", c.obsolete, 0)
			}
			r := f.scan()
			f.checkInvariants(r)
			it := r.one(t, "cursor-extension-old-versions")
			var got []string
			for _, p := range it.Paths {
				got = append(got, p[strings.LastIndex(p, "/")+1:])
			}
			if strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Errorf("stale = %v, want %v", got, c.want)
			}
			if !it.Recommended || it.Risk != core.RiskModerate || it.Category != core.CatIDE {
				t.Errorf("item = %v %v %v", it.Recommended, it.Risk, it.Category)
			}
		})
	}
}

func TestParseExtensionDir(t *testing.T) {
	for in, want := range map[string][2]string{
		"anthropic.claude-code-2.1.260-darwin-arm64":      {"anthropic.claude-code", "2.1.260"},
		"graphql.vscode-graphql-0.13.6-alpha.0-universal": {"graphql.vscode-graphql", "0.13.6"},
		"ms-python.vscode-pylance-2024.8.1":               {"ms-python.vscode-pylance", "2024.8.1"},
		"openai.openai-chatgpt-adhoc-0.0.1731981761":      {"openai.openai-chatgpt-adhoc", "0.0.1731981761"},
	} {
		id, ver, ok := parseExtensionDir(in)
		if !ok || id != want[0] || ver != want[1] {
			t.Errorf("parseExtensionDir(%q) = %q %q %v", in, id, ver, ok)
		}
	}
	if _, _, ok := parseExtensionDir("extensions.json"); ok {
		t.Errorf("extensions.json is not an extension dir")
	}
}

func TestCursorProjects(t *testing.T) {
	f := newFixture(t)
	existing := f.dir("src/alive", 0)
	p := ".cursor/projects/"
	alive := p + cursorEncode(existing)
	f.file(alive+"/mcps/server/tools.json", 3000, 0)
	f.file(alive+"/agent-transcripts/old.jsonl", 3000, 0)
	f.file(alive+"/terminals/new.txt", 3000, 0)
	f.ageTree(alive, 2*day)
	f.ageTree(alive+"/agent-transcripts", 40*day)

	gone := p + cursorEncode(f.path(".cursor/worktrees/app/x1"))
	f.file(gone+"/agent-transcripts/t.jsonl", 3000, 0)
	f.file(gone+"/mcps/s/t.json", 3000, 0)
	f.ageTree(gone, 3*day)

	numeric := p + "1778161984420"
	f.file(numeric+"/agent-transcripts/t.jsonl", 3000, 0)
	f.ageTree(numeric, 100*day)

	trusted := p + "some-weird-name"
	f.text(trusted+"/.workspace-trusted", `{"workspacePath":"`+f.path("deleted/trusted")+`"}`, 0)
	f.file(trusted+"/canvases/c.json", 1000, 0)
	f.ageTree(trusted, 3*day)

	// Folder recorded by Cursor, gone for 40 days: the only preselected kind.
	trustedOld := p + "old-trusted"
	f.text(trustedOld+"/.workspace-trusted", `{"workspacePath":"`+f.path("deleted/old")+`"}`, 0)
	f.file(trustedOld+"/agent-transcripts/t.jsonl", 1000, 0)
	f.ageTree(trustedOld, 40*day)

	// Decoded from the lossy name only, 40 days: to review, never preselected.
	lossyOld := p + cursorEncode(f.path(".cursor/worktrees/app/x2"))
	f.file(lossyOld+"/agent-transcripts/t.jsonl", 1000, 0)
	f.ageTree(lossyOld, 40*day)

	// Folder-less chats: not a path encoding, never an orphan.
	empty := p + "empty-window"
	f.file(empty+"/agent-transcripts/t.jsonl", 1000, 0)
	f.ageTree(empty, 40*day)

	// Opened as ~/src/work/app, on disk ~/src/Work/App (case-insensitive APFS).
	f.dir("src/Work/App", 0)
	cased := p + cursorEncode(f.path("src/work/app"))
	f.file(cased+"/agent-transcripts/t.jsonl", 1000, 0)
	f.ageTree(cased, 2*day)

	mcpCfg := f.text(".cursor/mcp.json", "{}", 0)
	rules := f.text(".cursor/rules/r.mdc", "x", 0)

	r := f.scan()
	f.checkInvariants(r, mcpCfg, rules)

	var orph, review *core.Item
	for _, it := range r.byKind("cursor-agent-orphan-projects") {
		if it.ID == itemID(it.Kind, f.path(".cursor/projects")) {
			orph = it
		} else {
			review = it
		}
	}
	if orph == nil || len(orph.Paths) != 1 || !hasTarget(orph, f.path(trustedOld)) || !orph.Recommended ||
		orph.Risk != core.RiskModerate || len(orph.ProcessGuard) == 0 || orph.Recheck == nil {
		t.Fatalf("orphans = %+v", orph)
	}
	if review == nil || len(review.Paths) != 3 || !hasTarget(review, f.path(gone)) || !hasTarget(review, f.path(trusted)) ||
		!hasTarget(review, f.path(lossyOld)) || review.Risk != core.RiskCaution || core.Recommend(review, f.now, 14*day) ||
		len(review.ProcessGuard) == 0 || review.Recheck == nil {
		t.Fatalf("orphans to review = %+v", review)
	}
	if !core.Recommend(orph, f.now, 14*day) {
		t.Errorf("an old orphan recorded by Cursor is preselected")
	}
	if err := orph.Recheck(context.Background()); err != nil {
		t.Errorf("recheck: %v", err)
	}
	f.dir("deleted/old", 0)
	if err := orph.Recheck(context.Background()); err == nil {
		t.Errorf("recheck must refuse once the folder exists again")
	}
	if err := review.Recheck(context.Background()); err != nil {
		t.Errorf("review recheck: %v", err)
	}
	f.file(lossyOld+"/agent-transcripts/sub/new.jsonl", 10, 0) // deep write, within liveGrace of the real clock
	_ = os.Chtimes(f.path(lossyOld+"/agent-transcripts/sub/new.jsonl"), time.Now(), time.Now())
	if err := review.Recheck(context.Background()); err == nil {
		t.Errorf("recheck must refuse after a deep write")
	}
	mcps := r.one(t, "cursor-agent-mcp-caches")
	if len(mcps.Paths) != 1 || !hasTarget(mcps, f.path(alive+"/mcps")) || mcps.Risk != core.RiskSafe {
		t.Errorf("mcps = %v", mcps.Paths)
	}
	old := r.one(t, "cursor-agent-old-transcripts")
	if !hasTarget(old, f.path(alive+"/agent-transcripts")) || !hasTarget(old, f.path(numeric+"/agent-transcripts")) ||
		!hasTarget(old, f.path(empty+"/agent-transcripts")) ||
		hasTarget(old, f.path(alive+"/terminals")) || old.Risk != core.RiskCaution {
		t.Errorf("old transcripts = %v", old.Paths)
	}
	for _, it := range r.items {
		if hasTarget(it, f.path(cased)) || hasTarget(it, f.path(empty)) {
			t.Errorf("%s must never be proposed as a whole: %s", it.ID, strings.Join(it.Targets(), ","))
		}
	}
}
