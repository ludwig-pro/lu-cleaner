package aitools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ludwig-pro/lu-cleaner/internal/core"
)

// transcript returns a minimal Claude Code JSONL transcript whose messages ran in cwd.
func transcript(cwd string) string {
	b, _ := json.Marshal(cwd)
	return `{"type":"mode","mode":"normal","sessionId":"x"}` + "\n" +
		`{"parentUuid":null,"cwd":` + string(b) + `,"type":"user","message":{"content":"hi"}}` + "\n"
}

func TestClaudeProjects(t *testing.T) {
	f := newFixture(t)
	existing := f.dir("work/app", 0)
	gone := f.path("work/.codex/worktrees/ab12/app") // never created
	projects := ".claude/projects/"

	// A: existing folder, one old session (with subagents) and one recent.
	a := projects + claudeEncode(existing)
	f.text(a+"/s-old.jsonl", transcript(existing), 0)
	f.file(a+"/s-old/subagents/agent-1.jsonl", 5000, 0)
	f.text(a+"/s-new.jsonl", transcript(existing), 0)
	f.ageTree(a, 40*day)
	f.age(a+"/s-new.jsonl", 2*day)

	// B: deleted worktree with auto-memory: everything but memory/ goes.
	b := projects + claudeEncode(gone)
	f.text(b+"/s-b.jsonl", transcript(gone), 0)
	f.file(b+"/s-b/tool-results/big.txt", 8000, 0)
	f.text(b+"/memory/MEMORY.md", "remember", 0)
	f.ageTree(b, 5*day)

	// C: deleted folder, no memory: the whole dir goes.
	goneC := f.path("gone/c")
	c := projects + claudeEncode(goneC)
	f.text(c+"/s-c.jsonl", transcript(goneC), 0)
	f.ageTree(c, 3*day)

	// D: deleted folder but the session is live (registry + pid alive).
	goneD := f.path("gone/d")
	d := projects + claudeEncode(goneD)
	f.text(d+"/s-live.jsonl", transcript(goneD), 0)
	f.ageTree(d, 3*day)
	f.text(".claude/sessions/"+fmt.Sprint(os.Getpid())+".json",
		fmt.Sprintf(`{"pid":%d,"sessionId":"s-live","cwd":%q}`, os.Getpid(), goneD), 0)
	f.text(".claude/sessions/99999999.json", `{"pid":99999999,"sessionId":"s-c","cwd":"/nope"}`, 0) // dead pid

	// E: only memory/: never touched.
	e := projects + claudeEncode(f.path("gone/e"))
	f.text(e+"/memory/MEMORY.md", "keep", 0)
	f.ageTree(e, 90*day)

	// F: no transcript cwd: folder decoded from the dir name, existing -> old sessions only.
	fdir := projects + claudeEncode(existing) + "-x"
	f.dir("work/app-x", 0)
	f.file(fdir+"/s-f/subagents/a.jsonl", 100, 0)
	f.ageTree(fdir, 60*day)

	// G: sessions-index.json fallback pointing to a deleted folder.
	goneG := f.path("gone/g")
	g := projects + "-weird-name"
	f.text(g+"/sessions-index.json", fmt.Sprintf(`{"version":1,"entries":[{"sessionId":"s-g","projectPath":%q}]}`, goneG), 0)
	f.file(g+"/s-g/tool-results/x.txt", 100, 0)
	f.ageTree(g, 10*day)

	// H: deleted folder, untouched for 45 days: preselected.
	goneH := f.path("gone/h")
	h := projects + claudeEncode(goneH)
	f.text(h+"/s-h.jsonl", transcript(goneH), 0)
	f.ageTree(h, 45*day)

	// never-delete files
	creds := f.text(".claude/.credentials.json", "{}", 0)
	settings := f.text(".claude/settings.json", "{}", 0)

	r := f.scan()
	f.checkInvariants(r, creds, settings, f.path(b+"/memory"), f.path(e+"/memory"))

	old := r.byKind("claude-code-old-sessions")
	var oldA, oldF *core.Item
	for _, it := range old {
		switch it.ID {
		case itemID("claude-code-old-sessions", f.path(a)):
			oldA = it
		case itemID("claude-code-old-sessions", f.path(fdir)):
			oldF = it
		}
	}
	if oldA == nil {
		t.Fatalf("old sessions of A missing: %v", names(old))
	}
	if oldA.Risk != core.RiskCaution || oldA.Recommended {
		t.Errorf("old sessions must be caution, not recommended: %v %v", oldA.Risk, oldA.Recommended)
	}
	if !hasTarget(oldA, f.path(a+"/s-old.jsonl")) || !hasTarget(oldA, f.path(a+"/s-old")) || hasTarget(oldA, f.path(a+"/s-new.jsonl")) {
		t.Errorf("A targets = %v", oldA.Targets())
	}
	if !strings.Contains(oldA.Name, "Claude Code sessions > 30d · ~/work/app") || oldA.Project != existing {
		t.Errorf("A name/project = %q / %q", oldA.Name, oldA.Project)
	}
	if !r.placeholder[oldA.ID] || oldA.Size == 0 || oldA.LastUsed.IsZero() {
		t.Errorf("A: placeholder=%v size=%d lastUsed=%v", r.placeholder[oldA.ID], oldA.Size, oldA.LastUsed)
	}
	if oldF == nil || oldF.Meta["cwd_source"] != "dir-name" {
		t.Errorf("F (dir-name resolution) = %+v", oldF)
	}

	orphans := map[string]*core.Item{}
	for _, it := range r.byKind("claude-code-orphan-project") {
		orphans[it.ID] = it
	}
	ob := orphans[itemID("claude-code-orphan-project", f.path(b))]
	if ob == nil {
		t.Fatalf("orphan B missing: %v", orphans)
	}
	// B was deleted 5 days ago: the folder may come back, never preselected.
	if ob.Risk != core.RiskCaution || ob.Recommended || !ob.NoRecommend || ob.Path != "" || ob.Meta["kept"] != "memory/" {
		t.Errorf("B = risk %v rec %v norec %v path %q meta %v", ob.Risk, ob.Recommended, ob.NoRecommend, ob.Path, ob.Meta)
	}
	if core.Recommend(ob, f.now, 14*day) {
		t.Errorf("B: a recent orphan must not be smart-selected")
	}
	if !hasTarget(ob, f.path(b+"/s-b.jsonl")) || !hasTarget(ob, f.path(b+"/s-b")) {
		t.Errorf("B targets = %v", ob.Targets())
	}
	oc := orphans[itemID("claude-code-orphan-project", f.path(c))]
	if oc == nil || oc.Path != f.path(c) {
		t.Errorf("orphan C should remove the whole dir: %+v", oc)
	}
	if _, ok := orphans[itemID("claude-code-orphan-project", f.path(d))]; ok {
		t.Errorf("D has a live session: must not be an orphan")
	}
	for _, it := range r.items {
		if strings.Contains(it.ID, claudeEncode(f.path("gone/e"))) {
			t.Errorf("E (memory only) must not produce items: %s", it.ID)
		}
	}
	og := orphans[itemID("claude-code-orphan-project", f.path(g))]
	if og == nil || og.Meta["cwd_source"] != "sessions-index" || og.Recommended || og.Risk != core.RiskCaution {
		t.Errorf("G (sessions-index fallback, 10 days) = %+v", og)
	}
	oh := orphans[itemID("claude-code-orphan-project", f.path(h))]
	if oh == nil || oh.Risk != core.RiskModerate || !oh.Recommended || oh.NoRecommend || !core.Recommend(oh, f.now, 14*day) {
		t.Errorf("H (deleted 45 days ago) must be moderate and preselected: %+v", oh)
	}
	if len(orphans) != 4 {
		t.Errorf("orphans = %v", names(r.byKind("claude-code-orphan-project")))
	}
}

func TestClaudeOrphanOnUnmountedVolumeIsNotOrphan(t *testing.T) {
	f := newFixture(t)
	cwd := "/Volumes/lu-cleaner-test-not-mounted-42/src/app"
	p := ".claude/projects/" + claudeEncode(cwd)
	f.text(p+"/s1.jsonl", transcript(cwd), 0)
	f.ageTree(p, 45*day)
	r := f.scan()
	if len(r.byKind("claude-code-orphan-project")) != 0 {
		t.Fatalf("a cwd on an unmounted volume is unknown, not deleted")
	}
	it := r.one(t, "claude-code-old-sessions")
	if it.Meta["cwd_status"] != "volume not mounted" {
		t.Errorf("meta = %v", it.Meta)
	}
}

func TestClaudeConfigBackups(t *testing.T) {
	f := newFixture(t)
	newest := f.text(".claude/backups/.claude.json.backup.300", "{}", 8*day)
	old1 := f.text(".claude/backups/.claude.json.backup.100", "{}", 20*day)
	old2 := f.text(".claude.json.backup.200", "{}", 10*day)
	plain := f.text(".claude.json.backup", "{}", 30*day)
	cfg := f.text(".claude.json", "{}", 30*day)
	r := f.scan()
	f.checkInvariants(r, plain, cfg, newest)
	it := r.one(t, "claude-code-config-backups")
	if !hasTarget(it, old1) || !hasTarget(it, old2) || len(it.Targets()) != 2 || it.Risk != core.RiskModerate {
		t.Errorf("backups = %v (%v)", it.Targets(), it.Risk)
	}
}

func TestEncoders(t *testing.T) {
	cases := []struct{ in, claude, cursor string }{
		{"/Users/me/.codex/worktrees/ab12/app", "-Users-me--codex-worktrees-ab12-app", "Users-me-codex-worktrees-ab12-app"},
		{"/Users/me/local_sources/my.site", "-Users-me-local-sources-my-site", "Users-me-local-sources-my-site"},
		{"/", "-", ""},
		{"/Users/me/Café", "-Users-me-Caf-", "Users-me-Caf"},
	}
	for _, c := range cases {
		if got := claudeEncode(c.in); got != c.claude {
			t.Errorf("claudeEncode(%q) = %q, want %q", c.in, got, c.claude)
		}
		if got := cursorEncode(c.in); got != c.cursor {
			t.Errorf("cursorEncode(%q) = %q, want %q", c.in, got, c.cursor)
		}
	}
	if got := decodeNaive("-Users-me--codex-worktrees", true); got != "/Users/me/.codex/worktrees" {
		t.Errorf("decodeNaive = %q", got)
	}
}

func TestResolver(t *testing.T) {
	base := t.TempDir()
	if r, err := filepath.EvalSymlinks(base); err == nil {
		base = r
	}
	for _, d := range []string{"a/.codex/worktrees/x1/app", "a/foo-bar/baz", "a/foo/bar", "a/local_sources/my.site"} {
		if err := os.MkdirAll(filepath.Join(base, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, enc := range []func(string) string{claudeEncode, cursorEncode} {
		r := newResolver(enc)
		r.root = base
		for _, d := range []string{"a/.codex/worktrees/x1/app", "a/foo-bar/baz", "a/foo/bar", "a/local_sources/my.site", "a"} {
			want := filepath.Join(base, d)
			got, ex := r.resolve(context.Background(), enc(want))
			if ex != existYes {
				t.Errorf("resolve(%q) = %v, want existYes", enc(want), ex)
			}
			// "a/foo-bar/baz" and "a/foo/bar/baz" would collide; here only one exists
			if enc(got) != enc(want) {
				t.Errorf("resolve(%q) = %q", enc(want), got)
			}
		}
		for _, d := range []string{"a/.codex/worktrees/x2/app", "a/nope"} {
			if _, ex := r.resolve(context.Background(), enc(filepath.Join(base, d))); ex != existNo {
				t.Errorf("resolve(missing %s) = %v, want existNo", d, ex)
			}
		}
		// Nothing of the name matches below the root: it may not be a path
		// at all (Cursor's "empty-window", a chat id...).
		for _, name := range []string{enc(filepath.Join(base, "b")), "empty-window"} {
			if _, ex := r.resolve(context.Background(), name); ex != existUnknown {
				t.Errorf("resolve(%s) = %v, want existUnknown", name, ex)
			}
		}
		// APFS is case-insensitive: a folder opened with another case exists.
		if got, ex := r.resolve(context.Background(), enc(filepath.Join(base, "A/FOO/Bar"))); ex != existYes || !strings.EqualFold(enc(got), enc(filepath.Join(base, "a/foo/bar"))) {
			t.Errorf("case-insensitive resolve = %q %v", got, ex)
		}
		if _, ex := r.resolve(context.Background(), strings.Repeat("x", 300)); ex != existUnknown {
			t.Errorf("over-long (truncated) names must be unknown")
		}
	}
	// A directory holding non-ASCII names cannot prove absence.
	if err := os.MkdirAll(filepath.Join(base, "u", "Café"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := newResolver(claudeEncode)
	r.root = base
	if _, ex := r.resolve(context.Background(), claudeEncode(filepath.Join(base, "u", "Cafe-x"))); ex != existUnknown {
		t.Errorf("non-ASCII sibling: want unknown, got %v", ex)
	}
}

func TestFindJSONString(t *testing.T) {
	for in, want := range map[string]string{
		`{"a":1,"cwd":"/Users/me/x"}`:         "/Users/me/x",
		`{"cwd": "/Users/me/with \"quote\""}`: `/Users/me/with "quote"`,
		`{"cwd":"/Users/me/café"}`:            "/Users/me/café",
		`{"nocwd":1}`:                         "",
		`{"cwd":"/unterminated`:               "",
	} {
		if got := findJSONString([]byte(in), "cwd"); got != want {
			t.Errorf("findJSONString(%s) = %q, want %q", in, got, want)
		}
	}
}

func TestOrphanRecheck(t *testing.T) {
	f := newFixture(t)
	gone := f.path("gone/wt")
	p := ".claude/projects/" + claudeEncode(gone)
	f.text(p+"/s.jsonl", transcript(gone), 0)
	f.ageTree(p, 3*day)
	ws := cursorSupport + "/User/workspaceStorage/w1"
	f.text(ws+"/workspace.json", `{"folder":"`+fileURI(gone)+`"}`, 0)
	f.ageTree(ws, 3*day)

	r := f.scan()
	claude := r.one(t, "claude-code-orphan-project")
	cursor := r.one(t, "cursor-workspace-storage-orphans")
	for _, it := range []*core.Item{claude, cursor} {
		if it.Recheck == nil {
			t.Fatalf("%s: orphan items must carry a Recheck", it.Kind)
		}
		if err := it.Recheck(context.Background()); err != nil {
			t.Errorf("%s: recheck before recreation: %v", it.Kind, err)
		}
	}
	f.dir("gone/wt", 0) // the worktree is recreated between scan and clean
	for _, it := range []*core.Item{claude, cursor} {
		if err := it.Recheck(context.Background()); err == nil {
			t.Errorf("%s: recheck must refuse once the folder exists again", it.Kind)
		}
	}
}
